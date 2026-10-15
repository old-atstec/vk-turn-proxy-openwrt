package main

// The app mode: the daemon as the engine of the Android app (android/).
//
// On a phone there is no netifd and no root. The app's VpnService owns the
// tun — Android creates it and decides which apps go into it (the split
// tunnel is VpnService.Builder's per-app list, android/README.md) — and runs
// the daemon as its child process, under the app's own uid. That uid is left
// out of the VPN, so the proxy's sockets take the phone's network as they are:
// no protect() per socket, no fwmark.
//
// What changes against the router, by two config keys:
//
//   - tun_fd_socket=<path>: the tun comes from the app, over a unix socket it
//     listens on, as a descriptor (SCM_RIGHTS) — the app establishes the VPN
//     only when the daemon asks for it, after the first TURN session, as
//     netifd puts the addresses on only after the hook's "up";
//   - network_events=stdin: the phone's network is the app's to tell (Android
//     keeps /proc/net/route from apps), on stdin, one command per line:
//
//     network <id>    the default network is now <id> (the app's handle of it)
//     network none    no network
//     dns <ip[:port]> the resolver for the proxy's own lookups (VK, captcha)
//     bind <device>   the proxy's sockets from now on leave by <device>
//                     (bind_device; the Windows app sends the physical
//                     interface's index before each "network", since its
//                     routes may hold the default — cmd/vkturn)
//     reconnect       every connection anew, as SIGHUP
//
//     and the end of stdin is the stop: the app closes it, the daemon gives
//     the TURN allocations back and exits, as on SIGTERM.

import (
	"bufio"
	"context"
	"io"
	"log"
	"strings"
	"sync"
)

// appNetwork is the network state as the app last told it, read by the
// netMonitor in place of /proc/net/route. Until the app says otherwise the
// network is up and unnamed: the first "network <id>" names it without a
// handover (the monitor is made after the handshake, long after that line).
type appNetwork struct {
	mu sync.Mutex
	id string
	up bool
}

func newAppNetwork() *appNetwork { return &appNetwork{up: true} }

func (a *appNetwork) set(id string, up bool) {
	a.mu.Lock()
	a.id, a.up = id, up
	a.mu.Unlock()
}

func (a *appNetwork) read() (gateway, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return gateway{Iface: a.id}, a.up
}

// appIdentity: the app's handle is the network's identity — Android gives a
// new Network to every new connection, the same Wi-Fi rejoined included.
func appIdentity(g gateway, up bool) string {
	if !up {
		return "down"
	}
	return "app " + g.Iface
}

// readAppCommands reads the app's commands until the end of r, then calls
// stop. reconnect is sent on, never blocking the reader: one pending is
// enough. bind puts the socket options on (proxy.SetSocketOptions).
func readAppCommands(r io.Reader, nw *appNetwork, reconnect chan<- struct{}, bind func(string) error, stop context.CancelFunc) {
	defer stop()
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		cmd, arg, _ := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		arg = strings.TrimSpace(arg)
		switch cmd {
		case "":
		case "network":
			if arg == "" || arg == "none" {
				nw.set("", false)
			} else {
				nw.set(arg, true)
			}
		case "dns":
			var c config
			if err := c.set("dns_server", arg); err != nil || c.DNSServer == "" {
				log.Printf("app: dns %q: an IP address or ip:port", arg)
				continue
			}
			installResolver(c.DNSServer)
		case "bind":
			if err := bind(arg); err != nil {
				log.Printf("app: bind %q: %v", arg, err)
			}
		case "reconnect":
			select {
			case reconnect <- struct{}{}:
			default:
			}
		default:
			log.Printf("app: unknown command %q", cmd)
		}
	}
	if err := sc.Err(); err != nil {
		log.Printf("app: stdin: %v", err)
	}
	log.Print("app: stdin closed — stopping")
}
