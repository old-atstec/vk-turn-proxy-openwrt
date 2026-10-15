package main

// The routes the domain mode puts into the tunnel as names resolve: one /32
// per address of a matched name, held well past the DNS TTL. A route taken
// away under a live connection breaks it — its packets would leave by the
// physical interface with the tunnel's source address — so the hold is long
// (6 h by default, twice the TTL if that is longer) and renewed by every
// answer that names the address again; browsers ask again every minute or so
// while a site is open. The table is bounded: past maxRoutes the route that
// would expire first goes (never the one just named).

import (
	"net/netip"
	"sort"
	"sync"
	"time"
)

const (
	routeHold = 6 * time.Hour
	maxRoutes = 8192
)

type routeOps struct {
	add func(netip.Prefix) error
	del func(netip.Prefix) error
}

type routeTable struct {
	mu   sync.Mutex
	ops  routeOps
	hold time.Duration
	max  int
	now  func() time.Time
	exp  map[netip.Addr]time.Time
}

func newRouteTable(ops routeOps) *routeTable {
	return &routeTable{ops: ops, hold: routeHold, max: maxRoutes, now: time.Now, exp: map[netip.Addr]time.Time{}}
}

func hostPrefix(a netip.Addr) netip.Prefix { return netip.PrefixFrom(a, a.BitLen()) }

// ensure routes a into the tunnel for at least the hold (or twice ttl).
// It returns once the route is in place: the answer that named the address
// goes to the program only after.
func (t *routeTable) ensure(a netip.Addr, ttl time.Duration) error {
	a = a.Unmap()
	until := t.now().Add(max(t.hold, 2*ttl))
	t.mu.Lock()
	defer t.mu.Unlock()
	if old, ok := t.exp[a]; ok {
		if until.After(old) {
			t.exp[a] = until
		}
		return nil
	}
	if err := t.ops.add(hostPrefix(a)); err != nil {
		return err
	}
	t.exp[a] = until
	if len(t.exp) > t.max {
		t.evictLocked(len(t.exp)-t.max, a)
	}
	return nil
}

// evictLocked takes away the n routes that would expire first — never keep,
// the one an answer is about to name.
func (t *routeTable) evictLocked(n int, keep netip.Addr) {
	type e struct {
		a netip.Addr
		t time.Time
	}
	all := make([]e, 0, len(t.exp))
	for a, x := range t.exp {
		if a != keep {
			all = append(all, e{a, x})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
	for _, x := range all[:min(n, len(all))] {
		_ = t.ops.del(hostPrefix(x.a))
		delete(t.exp, x.a)
	}
}

// expire takes away the routes past their hold and returns how many.
func (t *routeTable) expire() int {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for a, x := range t.exp {
		if !now.Before(x) {
			_ = t.ops.del(hostPrefix(a))
			delete(t.exp, a)
			n++
		}
	}
	return n
}

// reset forgets every route without touching the system: the adapter they
// were on is gone, and they with it.
func (t *routeTable) reset() {
	t.mu.Lock()
	t.exp = map[netip.Addr]time.Time{}
	t.mu.Unlock()
}

func (t *routeTable) size() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.exp)
}
