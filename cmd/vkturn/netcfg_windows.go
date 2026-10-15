//go:build windows

package main

// The network configuration through the IP Helper API (iphlpapi.dll) — no
// netsh, no route.exe: the structures are x/sys/windows' (their sizes and
// offsets checked against the C headers below), the calls x/sys lacks are
// taken from the DLL by name.

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The layouts of netioapi.h on 64-bit Windows; a wrong one fails the build.
var (
	_ = [1]int{}[unsafe.Sizeof(windows.MibIpForwardRow2{})-104]
	_ = [1]int{}[unsafe.Offsetof(windows.MibIpForwardRow2{}.NextHop)-44]
	_ = [1]int{}[unsafe.Offsetof(windows.MibIpForwardRow2{}.Metric)-84]
	_ = [1]int{}[unsafe.Sizeof(windows.MibUnicastIpAddressRow{})-80]
	_ = [1]int{}[unsafe.Offsetof(windows.MibUnicastIpAddressRow{}.OnLinkPrefixLength)-60]
	_ = [1]int{}[unsafe.Offsetof(windows.MibIpInterfaceRow{}.Metric)-148]
	_ = [1]int{}[unsafe.Offsetof(windows.MibIpInterfaceRow{}.NlMtu)-152]
)

var (
	iphlpapi                            = windows.NewLazySystemDLL("iphlpapi.dll")
	procInitializeIpForwardEntry        = iphlpapi.NewProc("InitializeIpForwardEntry")
	procCreateIpForwardEntry2           = iphlpapi.NewProc("CreateIpForwardEntry2")
	procDeleteIpForwardEntry2           = iphlpapi.NewProc("DeleteIpForwardEntry2")
	procInitializeUnicastIpAddressEntry = iphlpapi.NewProc("InitializeUnicastIpAddressEntry")
	procCreateUnicastIpAddressEntry     = iphlpapi.NewProc("CreateUnicastIpAddressEntry")
	procDeleteUnicastIpAddressEntry     = iphlpapi.NewProc("DeleteUnicastIpAddressEntry")
	procGetUnicastIpAddressTable        = iphlpapi.NewProc("GetUnicastIpAddressTable")
	procGetIpInterfaceEntry             = iphlpapi.NewProc("GetIpInterfaceEntry")
	procSetIpInterfaceEntry             = iphlpapi.NewProc("SetIpInterfaceEntry")

	dnsapi                    = windows.NewLazySystemDLL("dnsapi.dll")
	procDnsFlushResolverCache = dnsapi.NewProc("DnsFlushResolverCache")
	advapi32                  = windows.NewLazySystemDLL("advapi32.dll")
	procRegCreateKeyExW       = advapi32.NewProc("RegCreateKeyExW")
)

// iphlpapi returns its error code; 0 is success.
func ipcall(p *windows.LazyProc, args ...uintptr) error {
	r, _, _ := p.Call(args...)
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

const (
	errObjectExists = syscall.Errno(5010) // ERROR_OBJECT_ALREADY_EXISTS
	errNotFound     = syscall.Errno(1168) // ERROR_NOT_FOUND
	ipDadPreferred  = 4
	tunMetric       = 5
)

type winPlatform struct {
	job windows.Handle
}

func newWinPlatform() *winPlatform {
	p := &winPlatform{}
	// The daemon dies with the service: a service that crashed must not leave
	// a tunnel nobody controls.
	if job, err := windows.CreateJobObject(nil, nil); err == nil {
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err == nil {
			p.job = job
		} else {
			windows.CloseHandle(job)
		}
	}
	return p
}

// --- adapters ---

func adapters() ([]*windows.IpAdapterAddresses, error) {
	size := uint32(16 << 10)
	for i := 0; i < 5; i++ {
		buf := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC,
			windows.GAA_FLAG_INCLUDE_GATEWAYS|windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST,
			0, first, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return nil, err
		}
		var out []*windows.IpAdapterAddresses
		for a := first; a != nil; a = a.Next {
			out = append(out, a)
		}
		return out, nil
	}
	return nil, errors.New("GetAdaptersAddresses: the buffer keeps growing")
}

func addrOf(sa windows.SocketAddress) (netip.Addr, bool) {
	ip := sa.IP()
	if ip == nil {
		return netip.Addr{}, false
	}
	a, ok := netip.AddrFromSlice(ip)
	return a.Unmap(), ok
}

// physical: the up adapter with an IPv4 default gateway and the lowest
// interface metric, ours left out.
func (p *winPlatform) physical() (physNet, bool) {
	as, err := adapters()
	if err != nil {
		return physNet{}, false
	}
	type cand struct {
		a  *windows.IpAdapterAddresses
		gw netip.Addr
	}
	var cs []cand
	for _, a := range as {
		name := windows.UTF16PtrToString(a.FriendlyName)
		if a.OperStatus != windows.IfOperStatusUp || name == tunName || a.IfType == windows.IF_TYPE_SOFTWARE_LOOPBACK {
			continue
		}
		for g := a.FirstGatewayAddress; g != nil; g = g.Next {
			if ip, ok := addrOf(g.Address); ok && ip.Is4() && !ip.IsUnspecified() {
				cs = append(cs, cand{a, ip})
				break
			}
		}
	}
	if len(cs) == 0 {
		return physNet{}, false
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].a.Ipv4Metric < cs[j].a.Ipv4Metric })
	c := cs[0]
	n := physNet{Index: int(c.a.IfIndex), Name: windows.UTF16PtrToString(c.a.FriendlyName)}
	var addrs []string
	for u := c.a.FirstUnicastAddress; u != nil; u = u.Next {
		if ip, ok := addrOf(u.Address); ok && ip.Is4() {
			addrs = append(addrs, ip.String())
		}
	}
	n.ID = fmt.Sprintf("if%d/gw%s/%s", n.Index, c.gw, strings.Join(addrs, ","))
	var v6 []netip.Addr
	for d := c.a.FirstDnsServerAddress; d != nil; d = d.Next {
		if ip, ok := addrOf(d.Address); ok {
			if ip.Is4() {
				n.DNS = append(n.DNS, ip)
			} else if !ip.IsLinkLocalUnicast() && !strings.HasPrefix(ip.String(), "fec0:") {
				v6 = append(v6, ip)
			}
		}
	}
	n.DNS = append(n.DNS, v6...)
	return n, true
}

func (p *winPlatform) tunIndex() (int, error) {
	as, err := adapters()
	if err != nil {
		return 0, err
	}
	for _, a := range as {
		if windows.UTF16PtrToString(a.FriendlyName) == tunName {
			return int(a.IfIndex), nil
		}
	}
	return 0, fmt.Errorf("no adapter %q yet", tunName)
}

// --- addresses, MTU, metric ---

type mibUnicastIpAddressTable struct {
	NumEntries uint32
	Table      [1]windows.MibUnicastIpAddressRow
}

func putAddr(dst *windows.RawSockaddrInet, a netip.Addr) {
	b := (*[28]byte)(unsafe.Pointer(dst))
	*b = [28]byte{}
	if a.Is4() {
		dst.Family = windows.AF_INET
		v := a.As4()
		copy(b[4:8], v[:])
	} else {
		dst.Family = windows.AF_INET6
		v := a.As16()
		copy(b[8:24], v[:])
	}
}

func (p *winPlatform) configure(idx int, addr netip.Prefix, mtu int) error {
	// The adapter is reused from run to run: what it had goes first.
	var tbl *mibUnicastIpAddressTable
	if err := ipcall(procGetUnicastIpAddressTable, windows.AF_INET, uintptr(unsafe.Pointer(&tbl))); err == nil && tbl != nil {
		rows := unsafe.Slice(&tbl.Table[0], tbl.NumEntries)
		for i := range rows {
			if int(rows[i].InterfaceIndex) == idx {
				_ = ipcall(procDeleteUnicastIpAddressEntry, uintptr(unsafe.Pointer(&rows[i])))
			}
		}
		windows.FreeMibTable(unsafe.Pointer(tbl))
	}
	var row windows.MibUnicastIpAddressRow
	procInitializeUnicastIpAddressEntry.Call(uintptr(unsafe.Pointer(&row)))
	putAddr((*windows.RawSockaddrInet)(unsafe.Pointer(&row.Address)), addr.Addr())
	row.InterfaceIndex = uint32(idx)
	row.OnLinkPrefixLength = uint8(addr.Bits())
	row.DadState = ipDadPreferred
	if err := ipcall(procCreateUnicastIpAddressEntry, uintptr(unsafe.Pointer(&row))); err != nil && err != errObjectExists {
		return fmt.Errorf("address %s: %w", addr, err)
	}
	for _, fam := range []uint16{windows.AF_INET, windows.AF_INET6} {
		var ir windows.MibIpInterfaceRow
		ir.Family = fam
		ir.InterfaceIndex = uint32(idx)
		if err := ipcall(procGetIpInterfaceEntry, uintptr(unsafe.Pointer(&ir))); err != nil {
			if fam == windows.AF_INET {
				return fmt.Errorf("interface %d: %w", idx, err)
			}
			continue // IPv6 off on the adapter
		}
		ir.UseAutomaticMetric = 0
		ir.Metric = tunMetric
		if fam == windows.AF_INET || mtu >= 1280 {
			ir.NlMtu = uint32(mtu)
		}
		if fam == windows.AF_INET {
			ir.SitePrefixLength = 0 // SetIpInterfaceEntry refuses IPv4 otherwise
		}
		if err := ipcall(procSetIpInterfaceEntry, uintptr(unsafe.Pointer(&ir))); err != nil && fam == windows.AF_INET {
			return fmt.Errorf("interface %d mtu/metric: %w", idx, err)
		}
	}
	return nil
}

// --- routes ---

func routeRow(idx int, pfx netip.Prefix) windows.MibIpForwardRow2 {
	var row windows.MibIpForwardRow2
	procInitializeIpForwardEntry.Call(uintptr(unsafe.Pointer(&row)))
	row.InterfaceIndex = uint32(idx)
	putAddr(&row.DestinationPrefix.Prefix, pfx.Masked().Addr())
	row.DestinationPrefix.PrefixLength = uint8(pfx.Bits())
	var zero netip.Addr
	if pfx.Addr().Is4() {
		zero = netip.IPv4Unspecified()
	} else {
		zero = netip.IPv6Unspecified()
	}
	putAddr(&row.NextHop, zero) // on-link: into the adapter
	row.Metric = 0
	row.Protocol = windows.MIB_IPPROTO_NETMGMT
	return row
}

func (p *winPlatform) addRoute(idx int, pfx netip.Prefix) error {
	if idx == 0 {
		return errors.New("no adapter")
	}
	row := routeRow(idx, pfx)
	if err := ipcall(procCreateIpForwardEntry2, uintptr(unsafe.Pointer(&row))); err != nil && err != errObjectExists {
		return err
	}
	return nil
}

func (p *winPlatform) delRoute(idx int, pfx netip.Prefix) error {
	if idx == 0 {
		return nil
	}
	row := routeRow(idx, pfx)
	if err := ipcall(procDeleteIpForwardEntry2, uintptr(unsafe.Pointer(&row))); err != nil && err != errNotFound {
		return err
	}
	return nil
}

// --- DNS: the Name Resolution Policy Table ---
//
// A rule for "." sends every name to our listener and to nothing else — what
// Add-DnsClientNrptRule -Namespace "." -NameServers 127.0.0.1 writes, made
// by hand: the rule's key under Dnscache\Parameters\DnsPolicyConfig (as the
// OpenVPN interactive service does it), then a parameter-change control to
// the DNS client to read it. The key is volatile: should the service die
// without taking it away, a reboot does — and the service clears it when it
// starts anyway (cleanStale).

const (
	nrptParent = `SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\DnsPolicyConfig`
	nrptKey    = nrptParent + `\VKTurn`
	nrptGPO    = `SOFTWARE\Policies\Microsoft\Windows NT\DNSClient\DnsPolicyConfig`
)

func (p *winPlatform) setDNSPolicy(server netip.Addr) error {
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, nrptGPO, registry.ENUMERATE_SUB_KEYS); err == nil {
		names, _ := k.ReadSubKeyNames(1)
		k.Close()
		if len(names) > 0 {
			return errors.New("в системе есть правила NRPT групповой политики — локальное правило Windows не применит")
		}
	}
	parent, _, err := registry.CreateKey(registry.LOCAL_MACHINE, nrptParent, registry.CREATE_SUB_KEY)
	if err != nil {
		return err
	}
	parent.Close()
	k, err := createVolatileKey(registry.LOCAL_MACHINE, nrptKey)
	if err != nil {
		return err
	}
	defer k.Close()
	for _, e := range []error{
		k.SetStringsValue("Name", []string{"."}),
		k.SetStringValue("GenericDNSServers", server.String()),
		k.SetDWordValue("ConfigOptions", 0x8), // the generic DNS servers
		k.SetDWordValue("Version", 2),
		k.SetStringValue("IPSECCARestriction", ""),
		k.SetStringValue("Comment", "VK TURN: every name through its DNS while the tunnel is up"),
		k.SetStringValue("DisplayName", "VK TURN"),
	} {
		if e != nil {
			return e
		}
	}
	return reloadDNSClient()
}

func (p *winPlatform) clearDNSPolicy() error {
	err := registry.DeleteKey(registry.LOCAL_MACHINE, nrptKey)
	if err != nil && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return err
	}
	if err == nil {
		return reloadDNSClient()
	}
	return nil
}

func createVolatileKey(root registry.Key, path string) (registry.Key, error) {
	const regOptionVolatile = 1
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var h windows.Handle
	var disp uint32
	r, _, _ := procRegCreateKeyExW.Call(uintptr(root), uintptr(unsafe.Pointer(p)), 0, 0, regOptionVolatile,
		uintptr(registry.ALL_ACCESS), 0, uintptr(unsafe.Pointer(&h)), uintptr(unsafe.Pointer(&disp)))
	if r != 0 {
		return 0, syscall.Errno(r)
	}
	return registry.Key(h), nil
}

// reloadDNSClient: SERVICE_CONTROL_PARAMCHANGE to Dnscache — it reads its
// policy anew.
func reloadDNSClient() error {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(scm)
	name, _ := windows.UTF16PtrFromString("Dnscache")
	svc, err := windows.OpenService(scm, name, windows.SERVICE_PAUSE_CONTINUE)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(svc)
	var st windows.SERVICE_STATUS
	return windows.ControlService(svc, windows.SERVICE_CONTROL_PARAMCHANGE, &st)
}

func (p *winPlatform) flushDNS() { procDnsFlushResolverCache.Call() }

// bindControl: the interceptor's sockets to the physical network's
// resolvers leave by its interface, whatever our routes say (as the
// daemon's do, pkg/proxy/sockopt_windows.go).
func (p *winPlatform) bindControl(idx int) func(network, address string, c syscall.RawConn) error {
	if idx <= 0 {
		return nil
	}
	return func(_, _ string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			h := windows.Handle(fd)
			be := uint32(idx)>>24 | (uint32(idx)>>8)&0xff00 | (uint32(idx)<<8)&0xff0000 | uint32(idx)<<24
			e4 := windows.SetsockoptInt(h, windows.IPPROTO_IP, 31, int(be)) // IP_UNICAST_IF, network order
			e6 := windows.SetsockoptInt(h, windows.IPPROTO_IPV6, 31, idx)   // IPV6_UNICAST_IF
			if e4 != nil && e6 != nil {
				serr = e4
			}
		})
		if err != nil {
			return err
		}
		return serr
	}
}

// --- the daemon's process ---

func (p *winPlatform) prepareCmd(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

func (p *winPlatform) adopt(proc *os.Process) error {
	if p.job == 0 {
		return errors.New("no job object")
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(proc.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.AssignProcessToJobObject(p.job, h)
}
