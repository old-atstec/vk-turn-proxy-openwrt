package main

// The WAN monitor. On a router the tunnel's own traffic follows the main
// table's default route (the WAN); a PPPoE or DHCP reconnect, a new address
// or another uplink is a new network, and the proxy rebuilds its sessions as
// it does on a phone's handover. The default route is read from
// /proc/net/route every interval — no `ip` binary — skipping the tunnel's own
// interface (a user may route the default through it).
//
// What the proxy is told is the console's rule (upstream's paths.go): ONE
// handover is ONE path change — "gone" then "back" seconds apart is not a
// cascade. A sleep is not detected: routers do not sleep, and a late tick
// under load must not force a health check.

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type gateway struct {
	IP    string // "" on a point-to-point link
	Iface string
}

func (g gateway) String() string {
	if g.IP == "" {
		return g.Iface
	}
	return g.IP + " dev " + g.Iface
}

// parseProcRoute reads /proc/net/route and picks the IPv4 default route the
// kernel uses — the lowest metric, up — skipping the interface skip.
//
//	Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
//	eth0	00000000	017100CB	0003	0	0	0	00000000	0	0	0
func parseProcRoute(r io.Reader, skip string) (gateway, bool) {
	const rtfUp, rtfGateway = 0x1, 0x2
	best, bestMetric, found := gateway{}, int64(1<<62), false
	sc := bufio.NewScanner(r)
	for first := true; sc.Scan(); first = false {
		f := strings.Fields(sc.Text())
		if first || len(f) < 8 {
			continue
		}
		if f[1] != "00000000" || f[7] != "00000000" || f[0] == skip {
			continue
		}
		flags, err := strconv.ParseUint(f[3], 16, 32)
		if err != nil || flags&rtfUp == 0 {
			continue
		}
		metric, _ := strconv.ParseInt(f[6], 10, 64)
		g := gateway{Iface: f[0]}
		if flags&rtfGateway != 0 {
			// The kernel prints the __be32 as a host-order integer: the
			// bytes come back in network order through the native order.
			if v, err := strconv.ParseUint(f[2], 16, 32); err == nil {
				ip := make(net.IP, 4)
				binary.NativeEndian.PutUint32(ip, uint32(v))
				g.IP = ip.String()
			}
		}
		if !found || metric < bestMetric {
			best, bestMetric, found = g, metric, true
		}
	}
	return best, found
}

func readDefaultRoute(skip string) (gateway, bool) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return gateway{}, false
	}
	defer f.Close()
	return parseProcRoute(f, skip)
}

// networkIdentity: the next hop, the interface and its own IPv4 addresses —
// a new WAN address on the same gateway is a new network (the relays saw the
// old one).
func networkIdentity(g gateway, up bool) string {
	if !up {
		return "down"
	}
	var addrs []string
	if ifi, err := net.InterfaceByName(g.Iface); err == nil {
		if as, err := ifi.Addrs(); err == nil {
			for _, a := range as {
				if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
					addrs = append(addrs, n.String())
				}
			}
		}
	}
	return g.String() + " " + strings.Join(addrs, ",")
}

type netEvents struct {
	changed   bool
	up        bool
	prev, cur gateway
	prevUp    bool
}

type netMonitor struct {
	read     func() (gateway, bool)
	identity func(gateway, bool) string

	cur gateway
	up  bool
	id  string
}

func newNetMonitor(read func() (gateway, bool), identity func(gateway, bool) string) *netMonitor {
	g, up := read()
	return &netMonitor{read: read, identity: identity, cur: g, up: up, id: identity(g, up)}
}

func (m *netMonitor) step() netEvents {
	var ev netEvents
	g, up := m.read()
	if id := m.identity(g, up); id != m.id {
		ev.changed = true
		ev.prev, ev.prevUp = m.cur, m.up
		m.cur, m.up, m.id = g, up, id
	}
	ev.cur, ev.up = m.cur, m.up
	return ev
}

func (m *netMonitor) run(ctx context.Context, every time.Duration, handle func(netEvents)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			handle(m.step())
		}
	}
}

// pathSink is *proxy.Proxy's side of it.
type pathSink interface {
	OnPathChange()     // a handover: mark the identities in use
	OnPathTransition() // no usable path yet: hold the acquires
	OnPathUp()         // a network is there: rebuild the sessions on it
}

// tellPath is the console's paths.go: one handover, one path change.
func tellPath(p pathSink, rotateVK func(), ev netEvents) {
	switch {
	case ev.changed && !ev.up: // the WAN is gone — the handover begins
		p.OnPathChange()
	case !ev.changed && !ev.up: // still gone
		p.OnPathTransition()
	case ev.changed && ev.prevUp: // one network replaced by another in one reading
		p.OnPathChange()
		p.OnPathUp()
	case ev.changed: // back after "gone"
		rotateVK()
		p.OnPathUp()
	}
}
