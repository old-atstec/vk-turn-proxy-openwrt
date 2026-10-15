package proxy

// Connections spread over every relay of the call (Config.SpreadRelays).
//
// VK answers a mint with turn_server.urls — two relays on different /24s for
// the calls seen so far, the same pair on every mint of one link. The app
// dials Addresses[0] only, so a tunnel of one link sits on one relay host. On
// the router (2026-10-15, openwrt/TODO.md) that host was the ceiling: 20
// connections 34 Mbit/s, 40 — 60, 60 — 58, with the router's and the server's
// CPU idle and the WAN at 250+.
//
// Connection i dials Addresses[i % n] of whatever credential the pool hands
// it. The quota is per (identity, relay), and a slot carries at most
// connsPerSlot connections in all, so splitting them over relays can only
// lower each bucket's count. The pool is unchanged: slots, leases, saturation
// and the cache know the credential, not the relay. Cookie mode is left out —
// there a slot IS one relay (cookieCredForSlot), chosen so no two slots share
// one, and Addresses is the whole call's list.

// spreadRelay is the address connection connIdx dials on creds: addr (the
// slot's, Addresses[0]) when there is nothing to spread over or the caller is
// not a connection (connIdx < 0, the probe).
func spreadRelay(addr string, creds *TURNCreds, connIdx int) string {
	if creds == nil || connIdx < 0 || len(creds.Addresses) < 2 {
		return addr
	}
	return creds.Addresses[connIdx%len(creds.Addresses)]
}

// distinctRelaysAll counts the relay addresses the filled slots offer — every
// one of each credential's Addresses, the set spreadRelay dials.
func (cp *credPool) distinctRelaysAll() int {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	seen := make(map[string]struct{})
	for i := range cp.pool {
		c := cp.pool[i].creds
		if c == nil || cp.pool[i].addr == "" {
			continue
		}
		seen[cp.pool[i].addr] = struct{}{}
		for _, a := range c.Addresses {
			seen[a] = struct{}{}
		}
	}
	return len(seen)
}

// cachedAddresses is a cache entry's Addresses as loaded: the saved list when
// it starts with the entry's own address, else that address alone (a file
// from before the field, or one edited by hand).
func cachedAddresses(e credCacheEntry) []string {
	if len(e.Addresses) > 0 && e.Addresses[0] == e.Address {
		return append([]string(nil), e.Addresses...)
	}
	if e.Address == "" {
		return nil
	}
	return []string{e.Address}
}
