package main

import (
	"net/netip"
	"sort"

	"github.com/cacggghp/vk-turn-proxy/pkg/srs"
)

// LocalNetworks are what "the local networks past the tunnel" keeps out —
// the Android app's list (Cidr.LOCAL).
var LocalNetworks = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("224.0.0.0/4"),
}

// Subtract returns the addresses of a not in b, as the fewest prefixes.
// Both are IPv4.
func Subtract(a, b []netip.Prefix) []netip.Prefix {
	type span struct{ lo, hi uint64 }
	toSpan := func(p netip.Prefix) span {
		p = p.Masked()
		v := uint64(u32(p.Addr()))
		return span{v, v + 1<<(32-p.Bits()) - 1}
	}
	var cur []span
	for _, p := range a {
		cur = append(cur, toSpan(p))
	}
	for _, q := range b {
		cut := toSpan(q)
		var next []span
		for _, s := range cur {
			if cut.hi < s.lo || cut.lo > s.hi {
				next = append(next, s)
				continue
			}
			if s.lo < cut.lo {
				next = append(next, span{s.lo, cut.lo - 1})
			}
			if s.hi > cut.hi {
				next = append(next, span{cut.hi + 1, s.hi})
			}
		}
		cur = next
	}
	sort.Slice(cur, func(i, j int) bool { return cur[i].lo < cur[j].lo })
	// Merge what overlaps or touches (a may list 10/8 and 10.1/16).
	var merged []span
	for _, s := range cur {
		if n := len(merged); n > 0 && s.lo <= merged[n-1].hi+1 {
			merged[n-1].hi = max(merged[n-1].hi, s.hi)
			continue
		}
		merged = append(merged, s)
	}
	var out []netip.Prefix
	for _, s := range merged {
		out = append(out, srs.RangePrefixes(addr4(uint32(s.lo)), addr4(uint32(s.hi)))...)
	}
	return out
}

// SplitDefault turns 0.0.0.0/0 into its two halves: more specific than the
// physical default route, they win whatever its metric, and leave it be.
func SplitDefault(ps []netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range ps {
		if p.Bits() == 0 && p.Addr().Is4() {
			out = append(out, netip.MustParsePrefix("0.0.0.0/1"), netip.MustParsePrefix("128.0.0.0/1"))
			continue
		}
		out = append(out, p)
	}
	return out
}

func u32(a netip.Addr) uint32 {
	b := a.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func addr4(v uint32) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}
