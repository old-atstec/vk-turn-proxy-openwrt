package main

// The tunnel as the service runs it — the Android app's VkTurnService on
// Windows. The daemon (cmd/vk-turn-proxyd, the app mode of app.go) is the
// service's child process and does the tunnel: TURN, DTLS-SRTP, WireGuard,
// and the wintun adapter it creates itself. The service owns what netifd
// owns on the router and VpnService on a phone:
//
//  1. the daemon starts with its config and reaches VK's relays over the
//     physical network — its sockets are bound to the physical interface
//     (bind_device: IP_UNICAST_IF, pkg/proxy/sockopt_windows.go), so a route
//     of ours never catches them;
//  2. after the first TURN session the daemon creates the adapter "vkturn";
//     once it is there (the status: handshake / up / down) the service puts
//     the address, MTU, routes and the DNS policy on it — until then the
//     traffic goes as it went;
//  3. the physical network's changes go to the daemon's stdin (bind, network,
//     dns); closing stdin stops it (the TURN allocations are given back), and
//     the adapter goes with it — and every route on it.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	tunName   = "vkturn"
	dnsListen = "127.0.0.1"
)

// The phases, as the window shows them.
const (
	PhaseOff      = "off"
	PhaseStarting = "starting" // the daemon runs, the adapter is not configured yet
	PhaseRunning  = "running"  // the adapter carries the routes
	PhaseStopping = "stopping"
)

// physNet is the network the computer is on: the default route that is not
// ours.
type physNet struct {
	Index int
	Name  string
	ID    string       // changes with the network: interface, gateway, addresses
	DNS   []netip.Addr // its resolvers, IPv4 first
}

// platform is what only the operating system does (netcfg_windows.go; a
// fake in the tests and the demo).
type platform interface {
	physical() (physNet, bool)
	tunIndex() (int, error)
	configure(idx int, addr netip.Prefix, mtu int) error
	addRoute(idx int, p netip.Prefix) error
	delRoute(idx int, p netip.Prefix) error
	setDNSPolicy(server netip.Addr) error
	clearDNSPolicy() error
	flushDNS()
	bindControl(idx int) func(network, address string, c syscall.RawConn) error
	prepareCmd(c *exec.Cmd)
	adopt(p *os.Process) error // the daemon dies with the service (a job object)
}

// Dirs: everything of the service lives in one directory, SYSTEM's and the
// administrators' only.
type Dirs struct{ Root string }

func (d Dirs) Settings() string  { return filepath.Join(d.Root, "settings.json") }
func (d Dirs) Config() string    { return filepath.Join(d.Root, "vkturn.conf") }
func (d Dirs) Status() string    { return filepath.Join(d.Root, "run", "status.json") }
func (d Dirs) CredCache() string { return filepath.Join(d.Root, "state", "creds.json") }
func (d Dirs) Lists() string     { return filepath.Join(d.Root, "lists") }

type tunnel struct {
	plat    platform
	dirs    Dirs
	daemon  string // vk-turn-proxyd.exe
	dnsAddr string // the interceptor's listener: 127.0.0.1:53 (the tests: any port)
	log     *logRing
	lists   *lists

	mu        sync.Mutex
	settings  Settings
	phase     string
	err       string
	status    json.RawMessage
	state     string // the daemon's
	cmd       *exec.Cmd
	exited    chan struct{} // closed when cmd has exited
	stdin     io.WriteCloser
	stopCh    chan struct{}
	done      chan struct{}
	phys      physNet
	physOK    bool
	tunIdx    int
	statics   int
	dns       *interceptor
	routes    *routeTable
	listsBusy bool
}

func newTunnel(plat platform, dirs Dirs, daemon string, lg *logRing) *tunnel {
	t := &tunnel{plat: plat, dirs: dirs, daemon: daemon, dnsAddr: net.JoinHostPort(dnsListen, "53"), log: lg, lists: newLists(dirs.Lists()), phase: PhaseOff}
	s, err := load(dirs.Settings())
	if err != nil {
		t.logf("settings: %v — the defaults", err)
	}
	t.settings = s
	t.lists.load(s.Lists)
	return t
}

func (t *tunnel) logf(format string, args ...any) { t.log.add("app: " + fmt.Sprintf(format, args...)) }

// cleanStale undoes what a service that died left: the DNS policy points at
// a listener that is gone, and every lookup of the computer with it.
func (t *tunnel) cleanStale() {
	if err := t.plat.clearDNSPolicy(); err != nil {
		t.logf("DNS policy: %v", err)
	}
	t.plat.flushDNS()
}

func (t *tunnel) Settings() Settings {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.settings
}

// SaveSettings stores s; a running tunnel is restarted on it.
func (t *tunnel) SaveSettings(s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := writePrivate(t.dirs.Settings(), b); err != nil {
		return err
	}
	t.mu.Lock()
	t.settings = s
	running := t.phase == PhaseStarting || t.phase == PhaseRunning
	t.mu.Unlock()
	if running {
		t.Stop()
		return t.Start()
	}
	return nil
}

// Start brings the tunnel up on the saved settings.
func (t *tunnel) Start() error {
	t.mu.Lock()
	if t.phase != PhaseOff {
		t.mu.Unlock()
		return nil
	}
	s := t.settings
	if err := s.Validate(); err != nil {
		t.err = err.Error()
		t.mu.Unlock()
		return err
	}
	t.phase, t.err, t.status, t.state = PhaseStarting, "", nil, ""
	t.stopCh, t.done = make(chan struct{}), make(chan struct{})
	t.mu.Unlock()
	if err := t.begin(s); err != nil {
		close(t.done) // no loop was started
		t.fail("%v", err)
		return err
	}
	return nil
}

func (t *tunnel) begin(s Settings) error {
	phys, ok := t.plat.physical()
	if !ok {
		return errors.New("Нет сети: подключитесь к Wi-Fi или кабелю")
	}
	if s.Mode == ModeDomains && len(s.Lists) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		t.lists.update(ctx, s.Lists, time.Duration(s.ListHours)*time.Hour, false)
		cancel()
		for _, st := range t.lists.states(s.Lists) {
			if st.Error != "" {
				t.logf("list %s: %s", st.URL, st.Error)
			}
		}
	}
	t.routes = newRouteTable(routeOps{
		add: func(p netip.Prefix) error { return t.plat.addRoute(t.currentTun(), p) },
		del: func(p netip.Prefix) error { return t.plat.delRoute(t.currentTun(), p) },
	})
	_ = os.Remove(t.dirs.Status())
	conf := s.DaemonConfig(t.dirs, firstDNS(phys), phys.Index)
	if err := writePrivate(t.dirs.Config(), []byte(conf)); err != nil {
		return fmt.Errorf("config: %v", err)
	}
	cmd := exec.Command(t.daemon, "-config", t.dirs.Config())
	cmd.Dir = t.dirs.Root
	t.plat.prepareCmd(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("Запуск демона: %v", err)
	}
	if err := t.plat.adopt(cmd.Process); err != nil {
		t.logf("job object: %v", err)
	}
	exited := make(chan struct{})
	t.mu.Lock()
	t.cmd, t.exited, t.stdin, t.phys, t.physOK = cmd, exited, stdin, phys, true
	stop, done := t.stopCh, t.done
	t.mu.Unlock()
	t.logf("the network: %s (interface %d), mode %s", phys.Name, phys.Index, s.Mode)
	t.send("network " + phys.ID)
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			t.log.add(sc.Text())
		}
		err := cmd.Wait()
		close(exited)
		t.onExit(cmd, err)
	}()
	go t.loop(s, stop, done)
	return nil
}

func (t *tunnel) currentTun() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tunIdx
}

func firstDNS(p physNet) string {
	for _, a := range p.DNS {
		if a.Is4() {
			return a.String()
		}
	}
	return ""
}

func (t *tunnel) send(line string) {
	t.mu.Lock()
	w := t.stdin
	t.mu.Unlock()
	if w != nil {
		_, _ = io.WriteString(w, line+"\n")
	}
}

// Reconnect: every connection anew (the daemon's "reconnect").
func (t *tunnel) Reconnect() { t.send("reconnect") }

func (t *tunnel) loop(s Settings, stop, done chan struct{}) {
	defer close(done)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	n := 0
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		n++
		t.readStatus()
		t.mu.Lock()
		state, phase := t.state, t.phase
		t.mu.Unlock()
		if phase == PhaseStarting && (state == "handshake" || state == "up" || state == "down") {
			if err := t.configure(s); err != nil {
				t.logf("the adapter: %v — retrying", err)
			}
		}
		if n%2 == 0 {
			t.watchNetwork(s)
		}
		if n%60 == 0 && t.routes != nil {
			if k := t.routes.expire(); k > 0 {
				t.logf("%d route(s) of names expired", k)
			}
		}
		if n%600 == 0 && s.Mode == ModeDomains && s.ListHours > 0 {
			go t.refreshLists(s, false)
		}
	}
}

func (t *tunnel) readStatus() {
	b, err := os.ReadFile(t.dirs.Status())
	if err != nil {
		return
	}
	var st struct {
		State string `json:"state"`
	}
	if json.Unmarshal(b, &st) != nil {
		return
	}
	t.mu.Lock()
	t.status, t.state = b, st.State
	t.mu.Unlock()
}

// configure puts the address, the routes and the DNS policy on the adapter
// the daemon made.
func (t *tunnel) configure(s Settings) error {
	idx, err := t.plat.tunIndex()
	if err != nil {
		return err
	}
	if err := t.plat.configure(idx, s.TunnelPrefix(), s.MTU); err != nil {
		return err
	}
	t.mu.Lock()
	t.tunIdx = idx
	t.mu.Unlock()
	// The config holds the private key, and the daemon has read it.
	_ = os.Remove(t.dirs.Config())

	var static []netip.Prefix
	var match func(string) bool
	tunnelDNS, _ := s.TunnelDNSList()
	switch s.Mode {
	case ModeAll:
		static = append(s.StaticRoutes(),
			// IPv6 into the tunnel too, where WireGuard drops it (its peer
			// takes 0.0.0.0/0 only): the programs fall back to IPv4 instead
			// of going past the tunnel over IPv6.
			netip.MustParsePrefix("::/1"), netip.MustParsePrefix("8000::/1"))
	case ModeSubnets:
		static = s.StaticRoutes()
	case ModeDomains:
		own, _ := ParseOwnDomains(s.OwnDomains)
		var nets []netip.Prefix
		match, nets = t.lists.matcher(s.Lists, own)
		extra, _ := ParsePrefixes(s.Routes)
		static = append(append(extra, nets...), hostPrefixes(tunnelDNS)...)
	}
	added := 0
	for _, p := range static {
		if err := t.plat.addRoute(idx, p); err != nil {
			t.logf("route %s: %v", p, err)
			continue
		}
		added++
	}
	t.logf("the adapter %d: %s, mtu %d, %d route(s)", idx, s.TunnelPrefix(), s.MTU, added)

	if s.Mode != ModeSubnets {
		pol := t.policy(s, match)
		in := newInterceptor(pol)
		if err := in.listen(t.dnsAddr); err != nil {
			t.logf("DNS on %s: %v — the names of the lists will not go through the tunnel", t.dnsAddr, err)
			t.setErr("Порт 53 на 127.0.0.1 занят другой программой: доменный режим не работает")
		} else if err := t.plat.setDNSPolicy(netip.MustParseAddr(dnsListen)); err != nil {
			in.close()
			t.logf("DNS policy: %v", err)
			t.setErr("Не удалось направить DNS Windows в приложение: " + err.Error())
		} else {
			t.mu.Lock()
			t.dns = in
			t.mu.Unlock()
			t.logf("DNS: every lookup through %s", in.addr())
		}
		t.plat.flushDNS()
	}
	t.mu.Lock()
	t.statics = added
	if t.phase == PhaseStarting {
		t.phase = PhaseRunning
	}
	t.mu.Unlock()
	return nil
}

func hostPrefixes(as []netip.Addr) []netip.Prefix {
	var out []netip.Prefix
	for _, a := range as {
		out = append(out, hostPrefix(a))
	}
	return out
}

func (t *tunnel) policy(s Settings, match func(string) bool) *dnsPolicy {
	t.mu.Lock()
	phys := t.phys
	t.mu.Unlock()
	tunnelDNS, _ := s.TunnelDNSList()
	pol := &dnsPolicy{
		all:    s.Mode == ModeAll,
		match:  match,
		tunnel: &upstream{},
		direct: &upstream{control: t.plat.bindControl(phys.Index)},
		routes: t.routes,
	}
	for _, a := range tunnelDNS {
		pol.tunnel.servers = append(pol.tunnel.servers, net.JoinHostPort(a.String(), "53"))
	}
	for _, a := range phys.DNS {
		if a.Is4() && !a.IsLoopback() {
			pol.direct.servers = append(pol.direct.servers, net.JoinHostPort(a.String(), "53"))
		}
	}
	if len(pol.direct.servers) == 0 && s.FallbackDNS != "" {
		pol.direct.servers = []string{net.JoinHostPort(s.FallbackDNS, "53")}
	}
	return pol
}

// watchNetwork tells the daemon of a new physical network — the interface
// to bind to first, then the network, then its resolver.
func (t *tunnel) watchNetwork(s Settings) {
	p, ok := t.plat.physical()
	t.mu.Lock()
	changed := ok != t.physOK || p.ID != t.phys.ID
	t.phys, t.physOK = p, ok
	in := t.dns
	t.mu.Unlock()
	if !changed {
		return
	}
	if !ok {
		t.logf("no network")
		t.send("network none")
		return
	}
	t.logf("the network is now %s (interface %d)", p.Name, p.Index)
	t.send(fmt.Sprintf("bind %d", p.Index))
	t.send("network " + p.ID)
	if d := firstDNS(p); d != "" {
		t.send("dns " + d)
	}
	if in != nil {
		old := in.policy.Load()
		pol := t.policy(s, old.match)
		in.policy.Store(pol)
	}
}

// refreshLists fetches the lists anew; a running tunnel takes them at once
// (new ranges are routed, the names match from the next lookup).
func (t *tunnel) refreshLists(s Settings, force bool) {
	t.mu.Lock()
	if t.listsBusy {
		t.mu.Unlock()
		return
	}
	t.listsBusy = true
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.listsBusy = false
		t.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	changed := t.lists.update(ctx, s.Lists, time.Duration(s.ListHours)*time.Hour, force)
	for _, st := range t.lists.states(s.Lists) {
		if st.Error != "" {
			t.logf("list %s: %s", st.URL, st.Error)
		}
	}
	if !changed {
		return
	}
	t.logf("the lists are updated")
	t.mu.Lock()
	in, idx := t.dns, t.tunIdx
	cur := t.settings
	t.mu.Unlock()
	if in == nil || cur.Mode != ModeDomains {
		return
	}
	own, _ := ParseOwnDomains(cur.OwnDomains)
	match, nets := t.lists.matcher(cur.Lists, own)
	for _, p := range nets {
		_ = t.plat.addRoute(idx, p) // an existing one stays
	}
	pol := *in.policy.Load()
	pol.match = match
	in.policy.Store(&pol)
}

// RefreshLists is the button.
func (t *tunnel) RefreshLists() {
	t.refreshLists(t.Settings(), true)
}

func (t *tunnel) setErr(msg string) {
	t.mu.Lock()
	t.err = msg
	t.mu.Unlock()
}

func (t *tunnel) fail(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	t.logf("%s", msg)
	t.stop(msg)
}

func (t *tunnel) onExit(cmd *exec.Cmd, err error) {
	t.mu.Lock()
	mine := t.cmd == cmd
	stopping := t.phase == PhaseStopping
	t.mu.Unlock()
	if !mine || stopping {
		return
	}
	why := "код 0"
	if err != nil {
		why = err.Error()
	}
	t.fail("Демон остановился (%s): %s", why, t.log.last())
}

// Stop brings the tunnel down: the daemon gracefully, then the DNS policy.
func (t *tunnel) Stop() { t.stop("") }

func (t *tunnel) stop(errMsg string) {
	t.mu.Lock()
	if t.phase == PhaseOff || t.phase == PhaseStopping {
		if errMsg != "" {
			t.err = errMsg
		}
		t.mu.Unlock()
		return
	}
	t.phase = PhaseStopping
	if errMsg != "" {
		t.err = errMsg
	}
	cmd, exited, stdin, stop, done, in := t.cmd, t.exited, t.stdin, t.stopCh, t.done, t.dns
	t.stdin, t.dns = nil, nil
	t.mu.Unlock()

	if stop != nil {
		close(stop)
		<-done
	}
	// The DNS first: the names must not point at a listener that is going.
	if in != nil {
		if err := t.plat.clearDNSPolicy(); err != nil {
			t.logf("DNS policy: %v", err)
		}
		in.close()
		t.plat.flushDNS()
	}
	if stdin != nil {
		_ = stdin.Close() // the end of stdin is the daemon's stop
	}
	if cmd != nil && exited != nil {
		select {
		case <-exited:
		case <-time.After(6 * time.Second):
			t.logf("the daemon did not stop in 6 s — killed")
			_ = cmd.Process.Kill()
			<-exited
		}
	}
	_ = os.Remove(t.dirs.Config())
	if t.routes != nil {
		t.routes.reset()
	}
	t.mu.Lock()
	t.phase, t.cmd, t.tunIdx, t.status, t.state, t.statics = PhaseOff, nil, 0, nil, "", 0
	t.mu.Unlock()
	t.logf("stopped")
}

// view is what the window shows.
type view struct {
	Phase    string          `json:"phase"`
	Error    string          `json:"error,omitempty"`
	Status   json.RawMessage `json:"status,omitempty"`
	Network  string          `json:"network,omitempty"`
	Mode     string          `json:"mode"`
	Statics  int             `json:"static_routes"`
	Dynamic  int             `json:"name_routes"`
	DNS      *dnsView        `json:"dns,omitempty"`
	Lists    []listState     `json:"lists"`
	Ready    bool            `json:"ready"` // the settings are complete
	ReadyErr string          `json:"ready_error,omitempty"`
}

type dnsView struct {
	Queries  int64 `json:"queries"`
	Tunneled int64 `json:"tunneled"`
	Failures int64 `json:"failures"`
}

func (t *tunnel) View() view {
	t.mu.Lock()
	defer t.mu.Unlock()
	v := view{Phase: t.phase, Error: t.err, Status: t.status, Mode: t.settings.Mode, Statics: t.statics}
	if t.physOK && t.phase != PhaseOff {
		v.Network = t.phys.Name
	}
	if t.routes != nil && t.phase != PhaseOff {
		v.Dynamic = t.routes.size()
	}
	if t.dns != nil {
		v.DNS = &dnsView{t.dns.stats.Queries.Load(), t.dns.stats.Tunneled.Load(), t.dns.stats.Failures.Load()}
	}
	v.Lists = t.lists.states(t.settings.Lists)
	if err := t.settings.Validate(); err != nil {
		v.ReadyErr = err.Error()
	} else {
		v.Ready = true
	}
	return v
}

// writePrivate writes a file of the data directory whole (a temporary file
// and a rename); the directory's ACL makes it SYSTEM's and the
// administrators'.
func writePrivate(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
