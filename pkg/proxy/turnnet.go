package proxy

// The TURN clients' transport.Net. With none given, pion's turn.NewClient
// makes a stdnet.Net, and that lists the host's interfaces through netlink —
// which Android 11+ refuses to an app ("netlinkrib: permission denied"), so
// every session of the Android app failed before its first packet (seen on
// the emulator, 2026-10-15). A TURN client on a Conn of its own never looks
// at the interfaces: the stdnet.Net is used as it comes back from a failed
// listing (NewNet returns it either way), and made once — the listing is a
// netlink dump per session otherwise.

import (
	"log"
	"sync"

	"github.com/pion/transport/v4/stdnet"
)

var (
	turnNetOnce sync.Once
	turnNetVal  *stdnet.Net
)

func turnNet() *stdnet.Net {
	turnNetOnce.Do(func() {
		n, err := stdnet.NewNet()
		if err != nil {
			log.Printf("proxy: the interfaces are not listable (%v) — the TURN clients do without", err)
		}
		turnNetVal = n
	})
	return turnNetVal
}
