// Package srs reads sing-box's binary rule-sets (.srs) — the domain lists
// the Windows app (cmd/vkturn) sends into the tunnel — and matches domain
// names against them as sing-box does.
//
// The format, as sing-box writes it (common/srs/binary.go of
// SagerNet/sing-box, the domain set of SagerNet/sing common/domain):
//
//	"SRS"  version (one byte, 1…5)  zlib( uvarint(rule count)  rule… )
//
//	rule         = 0x00 item… 0xFF invert(byte)               a default rule
//	             | 0x01 mode(0 and, 1 or) uvarint(n) rule… invert(byte)   a logical one
//	item         = type(byte) body; the body by type:
//	  0 query_type          uvarint(n) n×uint16 BE
//	  1 network, 3 keyword, 4 regex, 8 source_port_range, 10 port_range,
//	  11 process_name, 12 process_path, 13 package_name, 14 wifi_ssid,
//	  15 wifi_bssid, 17 process_path_regex, 23 package_name_regex
//	                        uvarint(n) n×(uvarint(len) bytes)
//	  2 domain + domain_suffix   a domain set (below)
//	  5 source_ip_cidr, 6 ip_cidr   version byte 1, uint64 BE n, n×(from, to),
//	                        an address = uvarint(4|16) bytes
//	  7 source_port, 9 port  uvarint(n) n×uint16 BE
//	  16 adguard_domain     a domain set (AdGuard rules; read, not matched)
//	  18 network_type       uvarint(n) n bytes
//	  19 network_is_expensive, 20 network_is_constrained   no body
//	  21 network_interface_address   uvarint(n) n×(type byte, uvarint(m) m×prefix)
//	  22 default_interface_address   uvarint(m) m×prefix
//	                        a prefix = uvarint(4|16) address bytes, bits byte
//	domain set   = byte 0, uvarint(n) n×uint64 BE leaves,
//	               uvarint(n) n×uint64 BE label bitmap, uvarint(n) labels
//
// The domain set is a LOUDS trie of the reversed names: in breadth-first
// order each node is one 0 bit per child (its label in labels) and a 1; a
// leaf bit marks where a name ends. A name's first character says what it
// is: '\r' + ".example.com" matches the subdomains only, '\n' +
// "example.com" the name and its subdomains (domain_suffix since version 2),
// anything else the name exactly. Matching follows sing-box's default rule:
// the destination items (domain, domain_suffix, keyword, regex, ip_cidr) are
// OR'd, every other kind of item is AND'd with them — and since a DNS query
// tells nothing of ports or processes, a rule with any of those never
// matches here (counted in Stats.Skipped).
package srs

import (
	"bufio"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxVersion is the newest format read (sing-box's RuleSetVersionCurrent).
const MaxVersion = 5

// Limits against a hostile or broken file: the unpacked body, a list's
// length, a logical rule's depth.
const (
	maxBody  = 64 << 20
	maxCount = 1 << 22
	maxDepth = 100
)

const (
	itemQueryType = iota
	itemNetwork
	itemDomain
	itemDomainKeyword
	itemDomainRegex
	itemSourceIPCIDR
	itemIPCIDR
	itemSourcePort
	itemSourcePortRange
	itemPort
	itemPortRange
	itemProcessName
	itemProcessPath
	itemPackageName
	itemWIFISSID
	itemWIFIBSSID
	itemAdGuardDomain
	itemProcessPathRegex
	itemNetworkType
	itemNetworkIsExpensive
	itemNetworkIsConstrained
	itemNetworkInterfaceAddress
	itemDefaultInterfaceAddress
	itemPackageNameRegex
	itemFinal = 0xFF
)

var itemNames = map[byte]string{
	itemQueryType: "query_type", itemNetwork: "network", itemSourceIPCIDR: "source_ip_cidr",
	itemSourcePort: "source_port", itemSourcePortRange: "source_port_range", itemPort: "port",
	itemPortRange: "port_range", itemProcessName: "process_name", itemProcessPath: "process_path",
	itemPackageName: "package_name", itemWIFISSID: "wifi_ssid", itemWIFIBSSID: "wifi_bssid",
	itemAdGuardDomain: "adguard_domain", itemProcessPathRegex: "process_path_regex",
	itemNetworkType: "network_type", itemNetworkIsExpensive: "network_is_expensive",
	itemNetworkIsConstrained: "network_is_constrained", itemNetworkInterfaceAddress: "network_interface_address",
	itemDefaultInterfaceAddress: "default_interface_address", itemPackageNameRegex: "package_name_regex",
}

// RuleSet is one file's rules.
type RuleSet struct {
	Version uint8
	rules   []*rule
	Stats   Stats
}

// Stats counts what the file holds, for the screen.
type Stats struct {
	Rules    int
	Domains  int // exact names
	Suffixes int // domain_suffix entries, either kind
	Keywords int
	Regexes  int
	Prefixes int            // ip_cidr ranges routed as they are
	Skipped  map[string]int // rules that never match a name here, by the item that rules them out
}

type rule struct {
	// a default rule
	names    *nameSet
	keywords []string
	regexes  []*regexp.Regexp
	ipcidr   []netip.Prefix
	dest     bool   // has a destination item (an empty default rule matches everything)
	skip     string // an item a DNS query cannot satisfy: the rule never matches
	// a logical rule
	logical bool
	and     bool
	sub     []*rule

	invert bool
}

// Read parses a rule-set file.
func Read(r io.Reader) (*RuleSet, error) {
	br := bufio.NewReader(r)
	var head [4]byte
	if _, err := io.ReadFull(br, head[:]); err != nil {
		return nil, fmt.Errorf("not a sing-box rule-set: %w", err)
	}
	if string(head[:3]) != "SRS" {
		return nil, errors.New("not a sing-box binary rule-set (no SRS magic) — a source .json list?")
	}
	rs := &RuleSet{Version: head[3], Stats: Stats{Skipped: map[string]int{}}}
	if rs.Version < 1 || rs.Version > MaxVersion {
		return nil, fmt.Errorf("rule-set version %d: this app reads 1…%d", rs.Version, MaxVersion)
	}
	zr, err := zlib.NewReader(br)
	if err != nil {
		return nil, fmt.Errorf("rule-set body: %w", err)
	}
	defer zr.Close()
	lr := &io.LimitedReader{R: zr, N: maxBody + 1}
	d := &decoder{r: bufio.NewReader(lr)}
	n := d.count()
	for i := 0; i < n && d.err == nil; i++ {
		ru := d.rule(0, &rs.Stats)
		if d.err != nil {
			break
		}
		rs.rules = append(rs.rules, ru)
	}
	if d.err == nil {
		// The end of the stream checks zlib's checksum: a cut file fails here
		// even when its rules came out whole.
		if _, err := d.r.ReadByte(); err == nil {
			d.err = errors.New("data after the last rule")
		} else if err != io.EOF {
			d.err = err
		}
	}
	if d.err != nil {
		if lr.N <= 0 {
			return nil, fmt.Errorf("rule-set body is over %d MiB", maxBody>>20)
		}
		if len(rs.rules) == n {
			return nil, fmt.Errorf("rule-set body: %w", d.err)
		}
		return nil, fmt.Errorf("rule %d: %w", len(rs.rules), d.err)
	}
	rs.Stats.Rules = len(rs.rules)
	for _, ru := range rs.rules {
		if ru.static() {
			rs.Stats.Prefixes += len(ru.ipcidr)
		}
	}
	return rs, nil
}

// Match reports whether the name (any case, a trailing dot or not) is in
// the set: some rule matches it.
func (rs *RuleSet) Match(name string) bool {
	name = Normalize(name)
	if name == "" {
		return false
	}
	for _, ru := range rs.rules {
		if ru.match(name) {
			return true
		}
	}
	return false
}

// Prefixes are the ip_cidr ranges of the plain rules (top level, not
// inverted, nothing else AND'd): addresses that go into the tunnel as they
// are, with no name to match (geoip lists).
func (rs *RuleSet) Prefixes() []netip.Prefix {
	var out []netip.Prefix
	for _, ru := range rs.rules {
		if ru.static() {
			out = append(out, ru.ipcidr...)
		}
	}
	return out
}

func (ru *rule) static() bool {
	return !ru.logical && !ru.invert && ru.skip == "" && len(ru.ipcidr) > 0
}

// Normalize lowercases a DNS name and drops its trailing dot.
func Normalize(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

func (ru *rule) match(name string) bool {
	if ru.logical {
		m := ru.and
		for _, s := range ru.sub {
			if s.match(name) != ru.and {
				m = !ru.and
				break
			}
		}
		return m != ru.invert
	}
	if ru.skip != "" {
		return false
	}
	if !ru.dest {
		return !ru.invert
	}
	if ru.invert && len(ru.ipcidr) > 0 {
		// "not these addresses" cannot be told from a name.
		return false
	}
	m := ru.names.match(name)
	for _, k := range ru.keywords {
		if m {
			break
		}
		m = strings.Contains(name, k)
	}
	for _, re := range ru.regexes {
		if m {
			break
		}
		m = re.MatchString(name)
	}
	return m != ru.invert
}

// nameSet is a domain set unpacked: lookups by the name's own suffixes, at
// most one per label.
type nameSet struct {
	exact  map[string]struct{}
	suffix map[string]struct{} // '\n': the name or its subdomains
	sub    map[string]struct{} // '\r' + ".x": strict subdomains, kept with the dot
	other  []string            // '\r' without a dot: any longer name ending so (sing-box never writes it)
}

func (s *nameSet) match(name string) bool {
	if s == nil {
		return false
	}
	if _, ok := s.exact[name]; ok {
		return true
	}
	if _, ok := s.suffix[name]; ok {
		return true
	}
	for i := 1; i < len(name); i++ {
		if name[i] != '.' {
			continue
		}
		if _, ok := s.sub[name[i:]]; ok {
			return true
		}
		if _, ok := s.suffix[name[i+1:]]; ok {
			return true
		}
	}
	for _, o := range s.other {
		if len(name) > len(o) && strings.HasSuffix(name, o) {
			return true
		}
	}
	return false
}

func (s *nameSet) add(entry string, st *Stats) {
	if entry == "" {
		return
	}
	switch entry[0] {
	case '\r':
		v := strings.ToLower(entry[1:])
		if strings.HasPrefix(v, ".") {
			s.sub[v] = struct{}{}
		} else if v != "" {
			s.other = append(s.other, v)
		}
		st.Suffixes++
	case '\n':
		s.suffix[strings.ToLower(entry[1:])] = struct{}{}
		st.Suffixes++
	default:
		s.exact[strings.ToLower(entry)] = struct{}{}
		st.Domains++
	}
}

type decoder struct {
	r   *bufio.Reader
	err error
}

func (d *decoder) fail(err error) {
	if d.err == nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		d.err = err
	}
}

func (d *decoder) byte() byte {
	if d.err != nil {
		return 0
	}
	b, err := d.r.ReadByte()
	if err != nil {
		d.fail(err)
	}
	return b
}

func (d *decoder) uvarint() uint64 {
	if d.err != nil {
		return 0
	}
	v, err := binary.ReadUvarint(d.r)
	if err != nil {
		d.fail(err)
	}
	return v
}

// count is a list's length, bounded: the slices grow as the data comes, so
// a huge count in a short file costs nothing but the error.
func (d *decoder) count() int {
	n := d.uvarint()
	if n > maxCount {
		d.fail(fmt.Errorf("a list of %d entries", n))
		return 0
	}
	return int(n)
}

func (d *decoder) bytes(n int) []byte {
	if d.err != nil {
		return nil
	}
	b := make([]byte, 0, min(n, 4096))
	for len(b) < n {
		chunk := min(n-len(b), 4096)
		start := len(b)
		b = append(b, make([]byte, chunk)...)
		if _, err := io.ReadFull(d.r, b[start:]); err != nil {
			d.fail(err)
			return nil
		}
	}
	return b
}

func (d *decoder) strings() []string {
	n := d.count()
	var out []string
	for i := 0; i < n && d.err == nil; i++ {
		out = append(out, string(d.bytes(d.count())))
	}
	return out
}

func (d *decoder) uint64s() []uint64 {
	n := d.count()
	var out []uint64
	for i := 0; i < n && d.err == nil; i++ {
		out = append(out, binary.BigEndian.Uint64(d.bytes(8)))
	}
	return out
}

func (d *decoder) skipUint16s() { d.bytes(2 * d.count()) }

func (d *decoder) addr() netip.Addr {
	n := d.uvarint()
	if d.err == nil && n != 4 && n != 16 {
		d.fail(fmt.Errorf("an address of %d bytes", n))
	}
	b := d.bytes(int(n))
	if d.err != nil {
		return netip.Addr{}
	}
	a, _ := netip.AddrFromSlice(b)
	return a.Unmap()
}

func (d *decoder) prefix() netip.Prefix {
	a := d.addr()
	bits := d.byte()
	if d.err != nil {
		return netip.Prefix{}
	}
	p := netip.PrefixFrom(a, int(bits))
	if !p.IsValid() {
		d.fail(fmt.Errorf("prefix %s/%d", a, bits))
	}
	return p
}

// ipSet reads an IP set (ranges) as the prefixes that cover it exactly.
func (d *decoder) ipSet() []netip.Prefix {
	if v := d.byte(); d.err == nil && v != 1 {
		d.fail(fmt.Errorf("IP set version %d", v))
	}
	raw := d.bytes(8)
	if d.err != nil {
		return nil
	}
	n := binary.BigEndian.Uint64(raw)
	if n > maxCount {
		d.fail(fmt.Errorf("an IP set of %d ranges", n))
		return nil
	}
	var out []netip.Prefix
	for i := uint64(0); i < n && d.err == nil; i++ {
		from, to := d.addr(), d.addr()
		if d.err != nil {
			break
		}
		if from.Is4() != to.Is4() || to.Less(from) {
			d.fail(fmt.Errorf("IP range %s-%s", from, to))
			break
		}
		out = append(out, RangePrefixes(from, to)...)
	}
	return out
}

func (d *decoder) nameSet(st *Stats) *nameSet {
	_ = d.byte() // the set's own version, 0
	leaves := d.uint64s()
	bitmap := d.uint64s()
	labels := d.bytes(d.count())
	if d.err != nil {
		return nil
	}
	keys, err := trieKeys(leaves, bitmap, labels)
	if err != nil {
		d.fail(err)
		return nil
	}
	s := &nameSet{exact: map[string]struct{}{}, suffix: map[string]struct{}{}, sub: map[string]struct{}{}}
	for _, k := range keys {
		s.add(reverseRunes(k), st)
	}
	return s
}

func (d *decoder) rule(depth int, st *Stats) *rule {
	if depth > maxDepth {
		d.fail(errors.New("logical rules nested too deep"))
		return nil
	}
	switch t := d.byte(); {
	case d.err != nil:
		return nil
	case t == 0:
		return d.defaultRule(st)
	case t == 1:
		ru := &rule{logical: true}
		switch mode := d.byte(); mode {
		case 0:
			ru.and = true
		case 1:
		default:
			d.fail(fmt.Errorf("logical mode %d", mode))
			return nil
		}
		n := d.count()
		for i := 0; i < n && d.err == nil; i++ {
			ru.sub = append(ru.sub, d.rule(depth+1, st))
		}
		ru.invert = d.byte() != 0
		return ru
	default:
		d.fail(fmt.Errorf("rule type %d", t))
		return nil
	}
}

func (d *decoder) defaultRule(st *Stats) *rule {
	ru := &rule{}
	unsupported := func(t byte) {
		if ru.skip == "" {
			ru.skip = itemNames[t]
		}
	}
	for d.err == nil {
		t := d.byte()
		if d.err != nil {
			return nil
		}
		switch t {
		case itemFinal:
			ru.invert = d.byte() != 0
			if ru.skip != "" {
				st.Skipped[ru.skip]++
			}
			return ru
		case itemDomain:
			ru.names = d.nameSet(st)
			ru.dest = true
		case itemDomainKeyword:
			for _, k := range d.strings() {
				ru.keywords = append(ru.keywords, strings.ToLower(k))
			}
			st.Keywords += len(ru.keywords)
			ru.dest = true
		case itemDomainRegex:
			for _, s := range d.strings() {
				re, err := regexp.Compile(s)
				if err != nil {
					d.fail(fmt.Errorf("domain_regex %q: %w", s, err))
					return nil
				}
				ru.regexes = append(ru.regexes, re)
			}
			st.Regexes += len(ru.regexes)
			ru.dest = true
		case itemIPCIDR:
			ru.ipcidr = append(ru.ipcidr, d.ipSet()...)
			ru.dest = true
		case itemAdGuardDomain:
			// AdGuard filter syntax (wildcards, anchors): read so that the
			// rest of the file can be, never matched — the rule may still
			// match by its other destination items.
			_ = d.nameSet(&Stats{})
			st.Skipped["adguard_domain"]++
			ru.dest = true
		case itemSourceIPCIDR:
			_ = d.ipSet()
			unsupported(t)
		case itemQueryType, itemSourcePort, itemPort:
			d.skipUint16s()
			unsupported(t)
		case itemNetwork, itemSourcePortRange, itemPortRange, itemProcessName, itemProcessPath,
			itemPackageName, itemWIFISSID, itemWIFIBSSID, itemProcessPathRegex, itemPackageNameRegex:
			_ = d.strings()
			unsupported(t)
		case itemNetworkType:
			d.bytes(d.count())
			unsupported(t)
		case itemNetworkIsExpensive, itemNetworkIsConstrained:
			unsupported(t)
		case itemNetworkInterfaceAddress:
			n := d.count()
			for i := 0; i < n && d.err == nil; i++ {
				_ = d.byte()
				m := d.count()
				for j := 0; j < m && d.err == nil; j++ {
					_ = d.prefix()
				}
			}
			unsupported(t)
		case itemDefaultInterfaceAddress:
			m := d.count()
			for j := 0; j < m && d.err == nil; j++ {
				_ = d.prefix()
			}
			unsupported(t)
		default:
			d.fail(fmt.Errorf("unknown item type %d (a newer sing-box?)", t))
		}
	}
	return nil
}

// trieKeys unpacks a LOUDS trie into its keys. Nodes are numbered in
// breadth-first order, the root 0: walking the bitmap, a 0 is the next
// child of the current node (its label the next one), a 1 ends the node.
func trieKeys(leaves, bitmap []uint64, labels []byte) ([]string, error) {
	parent := []int32{-1}
	label := []byte{0}
	node, li := 0, 0
	for bit := 0; bit < len(bitmap)*64 && node < len(parent); bit++ {
		if bitmap[bit>>6]>>(bit&63)&1 == 0 {
			if li >= len(labels) {
				return nil, errors.New("domain set: more children than labels")
			}
			if len(parent) > maxCount*8 {
				return nil, errors.New("domain set too large")
			}
			parent = append(parent, int32(node))
			label = append(label, labels[li])
			li++
		} else {
			node++
		}
	}
	if node != len(parent) || li != len(labels) {
		return nil, errors.New("domain set: malformed trie")
	}
	var keys []string
	var buf []byte
	for n := range parent {
		if n>>6 >= len(leaves) || leaves[n>>6]>>(n&63)&1 == 0 {
			continue
		}
		buf = buf[:0]
		for c := n; c > 0; c = int(parent[c]) {
			buf = append(buf, label[c])
		}
		// buf is the key from its end: turn it round.
		for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
			buf[i], buf[j] = buf[j], buf[i]
		}
		keys = append(keys, string(buf))
	}
	return keys, nil
}

// reverseRunes is sing-box's reverseDomain: the names are stored reversed
// rune by rune, so that a UTF-8 name stays UTF-8.
func reverseRunes(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n == 1 {
			b[len(s)-i-1] = s[i]
		} else {
			utf8.EncodeRune(b[len(s)-i-n:], r)
		}
		i += n
	}
	return string(b)
}

// RangePrefixes covers [from, to] with the fewest prefixes.
func RangePrefixes(from, to netip.Addr) []netip.Prefix {
	var out []netip.Prefix
	for {
		bits := from.BitLen()
		// The largest block that starts at from and ends at or before to.
		for bits > 0 {
			p, _ := from.Prefix(bits - 1)
			if p.Addr() != from || lastAddr(p).Compare(to) > 0 {
				break
			}
			bits--
		}
		p := netip.PrefixFrom(from, bits)
		out = append(out, p)
		last := lastAddr(p)
		if last.Compare(to) >= 0 {
			return out
		}
		from = last.Next()
	}
}

func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().AsSlice()
	for i := p.Bits(); i < len(b)*8; i++ {
		b[i/8] |= 0x80 >> (i % 8)
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}
