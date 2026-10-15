package srs

// The fixtures are written here by a small encoder of the format (the
// package comment), not taken from a real list: documentation names and
// addresses only. The decoder was also checked by hand against real lists
// of MetaCubeX/meta-rules-dat (category-ai-!cn, google, geoip telegram):
// see docs/windows-testing.md.

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"net/netip"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// --- a writer of the format ---

type enc struct{ b bytes.Buffer }

func (e *enc) byte(b byte)      { e.b.WriteByte(b) }
func (e *enc) uvarint(v uint64) { e.b.Write(binary.AppendUvarint(nil, v)) }
func (e *enc) raw(b []byte)     { e.b.Write(b) }
func (e *enc) str(s string)     { e.uvarint(uint64(len(s))); e.raw([]byte(s)) }
func (e *enc) strs(t byte, v ...string) {
	e.byte(t)
	e.uvarint(uint64(len(v)))
	for _, s := range v {
		e.str(s)
	}
}
func (e *enc) u64s(v []uint64) {
	e.uvarint(uint64(len(v)))
	for _, x := range v {
		e.raw(binary.BigEndian.AppendUint64(nil, x))
	}
}

// domains writes a domain set: exact names, domain_suffix ('\n', version 2+)
// and strict-subdomain entries ('\r', what version 1 and a leading dot write).
func (e *enc) domains(t byte, exact, suffix, sub []string) {
	var keys []string
	for _, d := range exact {
		keys = append(keys, reverseRunes(d))
	}
	for _, d := range suffix {
		keys = append(keys, reverseRunes("\n"+d))
	}
	for _, d := range sub {
		keys = append(keys, reverseRunes("\r"+d))
	}
	sort.Strings(keys)
	leaves, bitmap, labels := louds(keys)
	e.byte(t)
	e.byte(0)
	e.u64s(leaves)
	e.u64s(bitmap)
	e.uvarint(uint64(len(labels)))
	e.raw(labels)
}

// louds builds the trie of sorted keys breadth-first: per node a 0 bit per
// child label, then a 1.
func louds(keys []string) (leaves, bitmap []uint64, labels []byte) {
	set := func(bm *[]uint64, i int) {
		for i>>6 >= len(*bm) {
			*bm = append(*bm, 0)
		}
		(*bm)[i>>6] |= 1 << (i & 63)
	}
	grow := func(bm *[]uint64, i int) {
		for i>>6 >= len(*bm) {
			*bm = append(*bm, 0)
		}
	}
	type span struct{ s, e, col int }
	q := []span{{0, len(keys), 0}}
	bit := 0
	for i := 0; i < len(q); i++ {
		sp := q[i]
		if sp.col == len(keys[sp.s]) {
			sp.s++
			set(&leaves, i)
		}
		for j := sp.s; j < sp.e; {
			from := j
			for j < sp.e && keys[j][sp.col] == keys[from][sp.col] {
				j++
			}
			q = append(q, span{from, j, sp.col + 1})
			labels = append(labels, keys[from][sp.col])
			grow(&bitmap, bit)
			bit++
		}
		set(&bitmap, bit)
		bit++
	}
	return
}

func (e *enc) ipcidr(t byte, ranges ...[2]string) {
	e.byte(t)
	e.byte(1)
	e.raw(binary.BigEndian.AppendUint64(nil, uint64(len(ranges))))
	for _, r := range ranges {
		for _, s := range r {
			a := netip.MustParseAddr(s).AsSlice()
			e.uvarint(uint64(len(a)))
			e.raw(a)
		}
	}
}

func (e *enc) final(invert bool) {
	e.byte(itemFinal)
	if invert {
		e.byte(1)
	} else {
		e.byte(0)
	}
}

func file(version byte, rules int, body func(*enc)) []byte {
	var e enc
	e.uvarint(uint64(rules))
	body(&e)
	var out bytes.Buffer
	out.WriteString("SRS")
	out.WriteByte(version)
	zw := zlib.NewWriter(&out)
	zw.Write(e.b.Bytes())
	zw.Close()
	return out.Bytes()
}

func read(t *testing.T, b []byte) *RuleSet {
	t.Helper()
	rs, err := Read(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func expect(t *testing.T, rs *RuleSet, yes, no []string) {
	t.Helper()
	for _, n := range yes {
		if !rs.Match(n) {
			t.Errorf("%s: no match, want one", n)
		}
	}
	for _, n := range no {
		if rs.Match(n) {
			t.Errorf("%s: matched, want not", n)
		}
	}
}

// --- the tests ---

// The three kinds of a domain set, as sing-box matches them: an exact name;
// domain_suffix ("example.com": the name and its subdomains, on a label
// boundary only); a leading-dot suffix (subdomains only).
func TestDomainSetKinds(t *testing.T) {
	rs := read(t, file(2, 1, func(e *enc) {
		e.byte(0)
		e.domains(itemDomain, []string{"exact.example.org"}, []string{"example.com", "test"}, []string{".example.net"})
		e.final(false)
	}))
	expect(t, rs,
		[]string{"exact.example.org", "EXACT.Example.ORG.", "example.com", "a.example.com", "a.b.example.com", "x.test", "a.example.net"},
		[]string{"sub.exact.example.org", "example.org", "notexample.com", "example.com.evil.invalid", "example.net", "anexample.net", "", "."},
	)
	want := Stats{Rules: 1, Domains: 1, Suffixes: 3, Skipped: map[string]int{}}
	if !reflect.DeepEqual(rs.Stats, want) {
		t.Fatalf("stats %+v, want %+v", rs.Stats, want)
	}
}

// Version 1 wrote a domain_suffix as the name plus a strict-subdomain entry;
// the two together match as one suffix.
func TestVersion1LegacySuffix(t *testing.T) {
	rs := read(t, file(1, 1, func(e *enc) {
		e.byte(0)
		e.domains(itemDomain, []string{"example.com"}, nil, []string{".example.com"})
		e.final(false)
	}))
	expect(t, rs, []string{"example.com", "www.example.com"}, []string{"wwwexample.com", "example.co"})
}

// Keyword and regex are OR'd with the names in one rule; a second rule is
// another chance.
func TestKeywordRegexAndRulesAreOred(t *testing.T) {
	rs := read(t, file(3, 2, func(e *enc) {
		e.byte(0)
		e.domains(itemDomain, []string{"one.example.org"}, nil, nil)
		e.strs(itemDomainKeyword, "Keyword")
		e.final(false)
		e.byte(0)
		e.strs(itemDomainRegex, `^api[0-9]+\.example\.net$`)
		e.final(false)
	}))
	expect(t, rs,
		[]string{"one.example.org", "has-keyword-inside.example.com", "api42.example.net"},
		[]string{"two.example.org", "api.example.net", "api42.example.net.example.com"},
	)
	if rs.Stats.Keywords != 1 || rs.Stats.Regexes != 1 {
		t.Fatalf("stats %+v", rs.Stats)
	}
}

// A rule that also asks for a port, a process or a network type cannot be
// satisfied by a DNS query: it never matches, and is counted.
func TestRulesWithItemsADNSQueryCannotSatisfyNeverMatch(t *testing.T) {
	rs := read(t, file(4, 3, func(e *enc) {
		e.byte(0)
		e.domains(itemDomain, nil, []string{"example.com"}, nil)
		e.byte(itemPort)
		e.uvarint(1)
		e.raw([]byte{0x01, 0xbb})
		e.final(false)

		e.byte(0)
		e.domains(itemDomain, nil, []string{"example.org"}, nil)
		e.strs(itemProcessName, "app.exe")
		e.byte(itemNetworkIsExpensive)
		e.final(true) // inverted: still never
		e.byte(0)
		e.domains(itemDomain, nil, []string{"example.net"}, nil)
		e.final(false)
	}))
	expect(t, rs, []string{"example.net"}, []string{"example.com", "a.example.org", "example.invalid"})
	if want := map[string]int{"port": 1, "process_name": 1}; !reflect.DeepEqual(rs.Stats.Skipped, want) {
		t.Fatalf("skipped %v, want %v", rs.Stats.Skipped, want)
	}
}

// Inverted rules and logical ones, as sing-box evaluates them.
func TestInvertAndLogicalRules(t *testing.T) {
	rs := read(t, file(2, 1, func(e *enc) {
		// and( suffix example.com, not keyword "ads" )
		e.byte(1)
		e.byte(0)
		e.uvarint(2)
		e.byte(0)
		e.domains(itemDomain, nil, []string{"example.com"}, nil)
		e.final(false)
		e.byte(0)
		e.strs(itemDomainKeyword, "ads")
		e.final(true)
		e.byte(0) // the logical rule not inverted
	}))
	expect(t, rs, []string{"www.example.com"}, []string{"ads.example.com", "example.org"})

	rs = read(t, file(2, 1, func(e *enc) {
		// not( or( exact a.example.org, exact b.example.org ) )
		e.byte(1)
		e.byte(1)
		e.uvarint(2)
		e.byte(0)
		e.domains(itemDomain, []string{"a.example.org"}, nil, nil)
		e.final(false)
		e.byte(0)
		e.domains(itemDomain, []string{"b.example.org"}, nil, nil)
		e.final(false)
		e.byte(1)
	}))
	expect(t, rs, []string{"c.example.org"}, []string{"a.example.org", "b.example.org"})
}

// ip_cidr: the ranges of a plain rule are routed as they are; inverted or
// logical ones are not, and an address set matches no name.
func TestIPCIDRBecomesPrefixes(t *testing.T) {
	rs := read(t, file(2, 3, func(e *enc) {
		e.byte(0)
		e.ipcidr(itemIPCIDR, [2]string{"192.0.2.0", "192.0.2.255"}, [2]string{"198.51.100.4", "198.51.100.11"}, [2]string{"2001:db8::", "2001:db8::ffff"})
		e.final(false)
		e.byte(0)
		e.ipcidr(itemIPCIDR, [2]string{"203.0.113.0", "203.0.113.255"})
		e.final(true)
		e.byte(0)
		e.ipcidr(itemSourceIPCIDR, [2]string{"203.0.113.0", "203.0.113.127"})
		e.domains(itemDomain, []string{"example.com"}, nil, nil)
		e.final(false)
	}))
	var got []string
	for _, p := range rs.Prefixes() {
		got = append(got, p.String())
	}
	want := []string{"192.0.2.0/24", "198.51.100.4/30", "198.51.100.8/30", "2001:db8::/112"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prefixes %v, want %v", got, want)
	}
	expect(t, rs, nil, []string{"example.com", "example.org"})
	if rs.Stats.Prefixes != 4 || rs.Stats.Skipped["source_ip_cidr"] != 1 {
		t.Fatalf("stats %+v", rs.Stats)
	}
}

// AdGuard items are read past (the rest of the file still parses) and never
// match; the rule's other items still do.
func TestAdGuardIsReadNotMatched(t *testing.T) {
	rs := read(t, file(2, 2, func(e *enc) {
		e.byte(0)
		e.domains(itemAdGuardDomain, []string{"||ads.example.com^"}, nil, nil)
		e.final(false)
		e.byte(0)
		e.domains(itemDomain, []string{"example.org"}, nil, nil)
		e.final(false)
	}))
	expect(t, rs, []string{"example.org"}, []string{"ads.example.com"})
	if rs.Stats.Skipped["adguard_domain"] != 1 {
		t.Fatalf("stats %+v", rs.Stats)
	}
}

func TestUTF8NamesKeepTheirRunes(t *testing.T) {
	rs := read(t, file(2, 1, func(e *enc) {
		e.byte(0)
		e.domains(itemDomain, []string{"пример.испытание"}, []string{"тест"}, nil)
		e.final(false)
	}))
	expect(t, rs, []string{"пример.испытание", "а.тест"}, []string{"пример.тест.example"})
}

func TestBadFilesAreRefused(t *testing.T) {
	good := file(2, 1, func(e *enc) {
		e.byte(0)
		e.domains(itemDomain, []string{"example.com"}, nil, nil)
		e.final(false)
	})
	for name, tc := range map[string]struct {
		b    []byte
		want string
	}{
		"json":      {[]byte(`{"version":2,"rules":[]}`), "no SRS magic"},
		"empty":     {nil, "not a sing-box rule-set"},
		"version 6": {append([]byte("SRS\x06"), good[4:]...), "version 6"},
		"version 0": {append([]byte("SRS\x00"), good[4:]...), "version 0"},
		"not zlib":  {[]byte("SRS\x02garbage"), "body"},
		"truncated": {good[:len(good)-6], "body"},
		"trailing": {file(2, 1, func(e *enc) {
			e.byte(0)
			e.final(false)
			e.byte(0)
		}), "after the last rule"},
		"unknown item": {file(2, 1, func(e *enc) {
			e.byte(0)
			e.byte(0x40)
		}), "unknown item type 64"},
		"huge count": {file(2, 1, func(e *enc) {
			e.byte(0)
			e.byte(itemDomainKeyword)
			e.uvarint(1 << 40)
		}), "a list of"},
		"bad regex": {file(2, 1, func(e *enc) {
			e.byte(0)
			e.strs(itemDomainRegex, "(")
			e.final(false)
		}), "domain_regex"},
		"labels short": {file(2, 1, func(e *enc) {
			e.byte(0)
			e.byte(itemDomain)
			e.byte(0)
			e.u64s([]uint64{1})
			e.u64s([]uint64{0b10}) // the root has one child, and no label for it
			e.uvarint(0)
			e.final(false)
		}), "labels"},
	} {
		_, err := Read(bytes.NewReader(tc.b))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want one with %q", name, err, tc.want)
		}
	}
}

// A body that unpacks past the limit is refused, not read into memory.
func TestBodyLimit(t *testing.T) {
	var out bytes.Buffer
	out.WriteString("SRS\x02")
	zw, _ := zlib.NewWriterLevel(&out, zlib.BestCompression)
	var e enc
	e.uvarint(1)
	e.byte(0)
	e.byte(itemDomainKeyword)
	const each = maxCount - 1
	n := maxBody/each + 2
	e.uvarint(uint64(n))
	zw.Write(e.b.Bytes())
	zeros := make([]byte, each)
	for i := 0; i < n; i++ {
		zw.Write(binary.AppendUvarint(nil, each))
		zw.Write(zeros)
	}
	zw.Close()
	_, err := Read(&out)
	if err == nil || !strings.Contains(err.Error(), "MiB") {
		t.Fatalf("error %v, want the body limit", err)
	}
}

func TestRangePrefixes(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		want     string
	}{
		{"192.0.2.0", "192.0.2.255", "192.0.2.0/24"},
		{"192.0.2.7", "192.0.2.7", "192.0.2.7/32"},
		{"198.51.100.1", "198.51.100.6", "198.51.100.1/32 198.51.100.2/31 198.51.100.4/31 198.51.100.6/32"},
		{"0.0.0.0", "255.255.255.255", "0.0.0.0/0"},
		{"2001:db8::", "2001:db8::1", "2001:db8::/127"},
	} {
		var got []string
		for _, p := range RangePrefixes(netip.MustParseAddr(tc.from), netip.MustParseAddr(tc.to)) {
			got = append(got, p.String())
		}
		if strings.Join(got, " ") != tc.want {
			t.Errorf("%s-%s: %v, want %s", tc.from, tc.to, got, tc.want)
		}
	}
}
