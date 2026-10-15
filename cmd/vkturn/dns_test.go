package main

// The interceptor against two resolvers on loopback — "the tunnel's" and
// "the network's" — each answering with its own address, so the test sees
// which one a name went to.

import (
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// fakeResolver answers every A with addr (TTL 300), every other type empty,
// over UDP and TCP on one port; it counts the queries and, if gate is set,
// holds them until it is closed.
type fakeResolver struct {
	addr    [4]byte
	queries atomic.Int32
	udp     net.PacketConn
	tcp     net.Listener
}

func newFakeResolver(t *testing.T, addr string) *fakeResolver {
	t.Helper()
	r := &fakeResolver{addr: netip.MustParseAddr(addr).As4()}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	r.udp, r.tcp = pc, ln
	t.Cleanup(func() { pc.Close(); ln.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if a := r.answer(buf[:n]); a != nil {
				pc.WriteTo(a, from)
			}
		}
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				q, err := readTCPMsg(c)
				if err != nil {
					return
				}
				if a := r.answer(q); a != nil {
					writeTCPMsg(c, a)
				}
			}()
		}
	}()
	return r
}

func (r *fakeResolver) server() string { return r.udp.LocalAddr().String() }

func (r *fakeResolver) answer(q []byte) []byte {
	r.queries.Add(1)
	var p dnsmessage.Parser
	h, err := p.Start(q)
	if err != nil {
		return nil
	}
	qs, err := p.Question()
	if err != nil {
		return nil
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, RecursionAvailable: true})
	b.StartQuestions()
	b.Question(qs)
	b.StartAnswers()
	if qs.Type == dnsmessage.TypeA {
		b.AResource(dnsmessage.ResourceHeader{Name: qs.Name, Class: dnsmessage.ClassINET, TTL: 300}, dnsmessage.AResource{A: r.addr})
	}
	m, _ := b.Finish()
	return m
}

func query(t *testing.T, server, name string, typ dnsmessage.Type, tcp bool) (dnsmessage.RCode, []netip.Addr, int) {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 0x4242, RecursionDesired: true})
	b.StartQuestions()
	b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: typ, Class: dnsmessage.ClassINET})
	q, _ := b.Finish()
	network := "udp"
	if tcp {
		network = "tcp"
	}
	c, err := net.DialTimeout(network, server, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	var r []byte
	if tcp {
		if err := writeTCPMsg(c, q); err != nil {
			t.Fatal(err)
		}
		r, err = readTCPMsg(c)
	} else {
		c.Write(q)
		buf := make([]byte, 4096)
		n, e := c.Read(buf)
		r, err = buf[:n], e
	}
	if err != nil {
		t.Fatalf("%s %v: %v", name, typ, err)
	}
	var p dnsmessage.Parser
	h, err := p.Start(r)
	if err != nil || h.ID != 0x4242 || !h.Response {
		t.Fatalf("%s: a bad answer %+v %v", name, h, err)
	}
	p.SkipAllQuestions()
	all, _ := p.AllAnswers()
	var as []netip.Addr
	for _, a := range all {
		if ar, ok := a.Body.(*dnsmessage.AResource); ok {
			as = append(as, netip.AddrFrom4(ar.A))
		}
	}
	return h.RCode, as, len(all)
}

type dnsStand struct {
	in             *interceptor
	tunnel, direct *fakeResolver
	fp             *fakePlatform
	routes         *routeTable
	addr           string
}

func newDNSStand(t *testing.T, all bool, match func(string) bool) *dnsStand {
	t.Helper()
	s := &dnsStand{
		tunnel: newFakeResolver(t, "203.0.113.5"),
		direct: newFakeResolver(t, "198.51.100.5"),
		fp:     newFakePlatform(),
	}
	s.routes = newRouteTable(routeOps{
		add: func(p netip.Prefix) error { return s.fp.addRoute(1, p) },
		del: func(p netip.Prefix) error { return s.fp.delRoute(1, p) },
	})
	s.in = newInterceptor(&dnsPolicy{
		all: all, match: match, routes: s.routes,
		tunnel: &upstream{servers: []string{s.tunnel.server()}},
		direct: &upstream{servers: []string{s.direct.server()}},
	})
	if err := s.in.listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.in.close)
	s.addr = s.in.addr()
	return s
}

func suffixMatch(suffix string) func(string) bool {
	return func(n string) bool {
		return n == suffix || len(n) > len(suffix) && n[len(n)-len(suffix)-1:] == "."+suffix
	}
}

// The domain mode: a listed name goes to the tunnel's resolver and its
// address is routed before the answer comes; any other name to the
// network's, unrouted; AAAA and HTTPS of a listed name are empty.
func TestTheDomainModeSplitsByName(t *testing.T) {
	s := newDNSStand(t, false, suffixMatch("example.com"))
	for _, tcp := range []bool{false, true} {
		rc, as, _ := query(t, s.addr, "chat.Example.COM.", dnsmessage.TypeA, tcp)
		if rc != dnsmessage.RCodeSuccess || len(as) != 1 || as[0].String() != "203.0.113.5" {
			t.Fatalf("listed (tcp %v): %v %v — not the tunnel's resolver", tcp, rc, as)
		}
		if !s.fp.hasRoute("203.0.113.5/32") {
			t.Fatalf("listed (tcp %v): answered with no route", tcp)
		}
		rc, as, _ = query(t, s.addr, "www.example.org.", dnsmessage.TypeA, tcp)
		if rc != dnsmessage.RCodeSuccess || len(as) != 1 || as[0].String() != "198.51.100.5" {
			t.Fatalf("other (tcp %v): %v %v — not the network's resolver", tcp, rc, as)
		}
	}
	if s.fp.hasRoute("198.51.100.5/32") || len(s.fp.routes) != 1 {
		t.Fatalf("routes %v: only the listed name's", s.fp.routes)
	}
	tq := s.tunnel.queries.Load()
	for _, typ := range []dnsmessage.Type{dnsmessage.TypeAAAA, dnsmessage.TypeHTTPS, dnsmessage.TypeSVCB} {
		rc, _, n := query(t, s.addr, "example.com.", typ, false)
		if rc != dnsmessage.RCodeSuccess || n != 0 {
			t.Fatalf("%v of a listed name: %v with %d answers, want an empty success", typ, rc, n)
		}
	}
	if s.tunnel.queries.Load() != tq {
		t.Fatal("an empty answer was asked upstream")
	}
	if _, _, n := query(t, s.addr, "example.org.", dnsmessage.TypeAAAA, false); n != 0 || s.direct.queries.Load() == 0 {
		t.Fatal("the AAAA of another name did not go to the network's resolver")
	}
	if got := s.in.stats.Tunneled.Load(); got != 5 {
		t.Fatalf("tunneled %d, want 5", got)
	}
}

func TestTheAllModeSendsEveryNameThroughTheTunnel(t *testing.T) {
	s := newDNSStand(t, true, nil)
	_, as, _ := query(t, s.addr, "www.example.org.", dnsmessage.TypeA, false)
	if len(as) != 1 || as[0].String() != "203.0.113.5" || s.direct.queries.Load() != 0 {
		t.Fatalf("all: %v, the network's resolver asked %d times", as, s.direct.queries.Load())
	}
	if len(s.fp.routes) != 0 {
		t.Fatal("all: per-name routes (the default is in the tunnel already)")
	}
	if _, _, n := query(t, s.addr, "www.example.org.", dnsmessage.TypeAAAA, false); n != 0 {
		t.Fatal("all: an AAAA answered")
	}
}

// Firefox's canary: NXDOMAIN, so that its DNS over HTTPS stays off.
func TestTheDoHCanaryIsNXDOMAIN(t *testing.T) {
	s := newDNSStand(t, false, suffixMatch("example.com"))
	rc, _, _ := query(t, s.addr, "use-application-dns.net.", dnsmessage.TypeA, false)
	if rc != dnsmessage.RCodeNameError || s.direct.queries.Load()+s.tunnel.queries.Load() != 0 {
		t.Fatalf("canary: %v", rc)
	}
}

func TestAnUpstreamThatDoesNotAnswerIsSERVFAIL(t *testing.T) {
	s := newDNSStand(t, false, suffixMatch("example.com"))
	dead, _ := net.ListenPacket("udp", "127.0.0.1:0") // reads nothing
	defer dead.Close()
	pol := *s.in.policy.Load()
	pol.tunnel = &upstream{servers: []string{dead.LocalAddr().String()}}
	s.in.policy.Store(&pol)
	t0 := time.Now()
	rc, _, _ := query(t, s.addr, "example.com.", dnsmessage.TypeA, false)
	if rc != dnsmessage.RCodeServerFailure || time.Since(t0) > dnsTimeout+3*time.Second {
		t.Fatalf("%v after %s", rc, time.Since(t0))
	}
	if len(s.fp.routes) != 0 {
		t.Fatal("a route with no answer")
	}
}

// Many lookups at once: each listed one routed, the table counts each
// address once.
func TestConcurrentLookups(t *testing.T) {
	s := newDNSStand(t, false, suffixMatch("example.com"))
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			query(t, s.addr, "a.example.com.", dnsmessage.TypeA, false)
		}()
	}
	wg.Wait()
	if s.routes.size() != 1 || s.in.stats.Failures.Load() != 0 {
		t.Fatalf("routes %d, failures %d", s.routes.size(), s.in.stats.Failures.Load())
	}
}

func TestAnswerAddrsFollowsCNAMEs(t *testing.T) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 1, Response: true})
	b.StartQuestions()
	b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName("www.example.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET})
	b.StartAnswers()
	b.CNAMEResource(dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName("www.example.com."), Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName("edge.example.net.")})
	b.AResource(dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName("edge.example.net."), Class: dnsmessage.ClassINET, TTL: 20}, dnsmessage.AResource{A: [4]byte{192, 0, 2, 1}})
	b.AResource(dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName("edge.example.net."), Class: dnsmessage.ClassINET, TTL: 20}, dnsmessage.AResource{A: [4]byte{192, 0, 2, 2}})
	m, _ := b.Finish()
	got := answerAddrs(m)
	if len(got) != 2 || got[1].addr.String() != "192.0.2.2" || got[0].ttl != 20*time.Second {
		t.Fatalf("%v", got)
	}
}
