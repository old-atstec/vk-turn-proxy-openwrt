package main

import (
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSubtractAndSplit(t *testing.T) {
	str := func(ps []netip.Prefix) string {
		var s []string
		for _, p := range ps {
			s = append(s, p.String())
		}
		return strings.Join(s, " ")
	}
	pp := func(s ...string) []netip.Prefix {
		var out []netip.Prefix
		for _, x := range s {
			out = append(out, netip.MustParsePrefix(x))
		}
		return out
	}
	for _, tc := range []struct {
		a, b []netip.Prefix
		want string
	}{
		{pp("192.0.2.0/24"), pp("192.0.2.0/25"), "192.0.2.128/25"},
		{pp("192.0.2.0/24"), pp("198.51.100.0/24"), "192.0.2.0/24"},
		{pp("192.0.2.0/24"), pp("192.0.2.0/24"), ""},
		{pp("192.0.2.0/24", "192.0.2.16/28"), nil, "192.0.2.0/24"},  // nested: merged
		{pp("192.0.2.0/25", "192.0.2.128/25"), nil, "192.0.2.0/24"}, // adjacent: merged
		{pp("0.0.0.0/0"), pp("128.0.0.0/1"), "0.0.0.0/1"},
		{pp("192.0.2.0/24"), pp("192.0.2.64/26"), "192.0.2.0/26 192.0.2.128/25"},
	} {
		if got := str(Subtract(tc.a, tc.b)); got != tc.want {
			t.Errorf("%v − %v = %s, want %s", tc.a, tc.b, got, tc.want)
		}
	}
	if got := str(SplitDefault(pp("0.0.0.0/0", "192.0.2.0/24"))); got != "0.0.0.0/1 128.0.0.0/1 192.0.2.0/24" {
		t.Errorf("split: %s", got)
	}
}

// "All traffic" with the local networks past it: 10/8 goes past — but not
// the tunnel's own /24 inside it (Android's lesson), and nothing is a /0.
func TestStaticRoutesKeepTheTunnelsOwnSubnet(t *testing.T) {
	s := defaultSettings()
	s.Mode, s.Address, s.BypassLAN = ModeAll, "10.66.66.2/24", true
	rs := s.StaticRoutes()
	in := func(a string) bool {
		for _, p := range rs {
			if p.Contains(netip.MustParseAddr(a)) {
				return true
			}
		}
		return false
	}
	for a, want := range map[string]bool{"10.66.66.1": true, "10.1.2.3": false, "192.168.1.1": false, "198.51.100.7": true, "8.8.8.8": true, "224.0.0.251": false} {
		if in(a) != want {
			t.Errorf("%s in the tunnel: %v, want %v", a, !want, want)
		}
	}
	for _, p := range rs {
		if p.Bits() == 0 {
			t.Fatalf("a /0 route: %v", rs)
		}
	}
	s.BypassLAN = false
	if got := s.StaticRoutes(); !reflect.DeepEqual(got, SplitDefault([]netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")})) {
		t.Fatalf("all, no bypass: %v", got)
	}
	s.Mode, s.Routes = ModeSubnets, "198.51.100.0/24, 10.0.0.0/8"
	s.BypassLAN = true
	got := s.StaticRoutes()
	if len(got) != 2 || got[0].String() != "10.66.66.0/24" || got[1].String() != "198.51.100.0/24" {
		t.Fatalf("subnets: %v", got)
	}
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func newTestTable() (*routeTable, *fakePlatform, *fakeClock) {
	fp := newFakePlatform()
	clk := &fakeClock{now: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	rt := newRouteTable(routeOps{
		add: func(p netip.Prefix) error { return fp.addRoute(1, p) },
		del: func(p netip.Prefix) error { return fp.delRoute(1, p) },
	})
	rt.now = clk.Now
	return rt, fp, clk
}

// A name's address: routed once, held for the hold or twice its TTL, renewed
// by each answer that names it again, taken away after.
func TestRouteTableHoldsAndExpires(t *testing.T) {
	rt, fp, clk := newTestTable()
	a := netip.MustParseAddr("192.0.2.10")
	b := netip.MustParseAddr("198.51.100.20")
	if err := rt.ensure(a, 60*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := rt.ensure(b, 5*time.Hour); err != nil { // 2×TTL > the hold
		t.Fatal(err)
	}
	if !fp.hasRoute("192.0.2.10/32") || !fp.hasRoute("198.51.100.20/32") {
		t.Fatalf("routes %v", fp.routes)
	}
	clk.now = clk.now.Add(5 * time.Hour)
	_ = rt.ensure(a, 60*time.Second) // named again: renewed to now + 6 h
	clk.now = clk.now.Add(2 * time.Hour)
	if n := rt.expire(); n != 0 {
		t.Fatalf("%d expired at 7 h, want none", n)
	}
	clk.now = clk.now.Add(4 * time.Hour) // 11 h: a's 11 h reached, b's 10 h passed
	if n := rt.expire(); n != 2 {
		t.Fatalf("%d expired at 11 h, want 2", n)
	}
	if len(fp.routes) != 0 || rt.size() != 0 {
		t.Fatalf("left: %v", fp.routes)
	}
}

func TestRouteTableIsBounded(t *testing.T) {
	rt, fp, clk := newTestTable()
	rt.max = 3
	for i, ttl := range []time.Duration{9 * time.Hour, 4 * time.Hour, 8 * time.Hour} {
		_ = rt.ensure(netip.AddrFrom4([4]byte{192, 0, 2, byte(i + 1)}), ttl)
	}
	clk.now = clk.now.Add(time.Minute)
	_ = rt.ensure(netip.MustParseAddr("192.0.2.9"), 0)
	if rt.size() != 3 {
		t.Fatalf("size %d", rt.size())
	}
	// Held: .1 18 h, .2 8 h, .3 16 h, .9 6 h — .9 would expire first, but an
	// answer is about to name it: .2 goes.
	if !fp.hasRoute("192.0.2.9/32") || fp.hasRoute("192.0.2.2/32") {
		t.Fatalf("evicted the wrong one: %v", fp.routes)
	}
}

func TestRouteTableReportsAFailedAdd(t *testing.T) {
	rt, fp, _ := newTestTable()
	fp.addErr = errors.New("no adapter")
	if err := rt.ensure(netip.MustParseAddr("192.0.2.1"), time.Minute); err == nil {
		t.Fatal("no error")
	}
	if rt.size() != 0 {
		t.Fatal("a route that failed is counted")
	}
	fp.addErr = nil
	if err := rt.ensure(netip.MustParseAddr("192.0.2.1"), time.Minute); err != nil || !fp.hasRoute("192.0.2.1/32") {
		t.Fatalf("retry: %v", err)
	}
	rt.reset()
	if rt.size() != 0 {
		t.Fatal("reset kept routes")
	}
}
