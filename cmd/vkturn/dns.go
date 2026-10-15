package main

// The DNS interceptor. While the tunnel is up in the modes "all" and
// "domains", Windows sends every lookup here: a Name Resolution Policy rule
// for "." (every name) names this listener as the only server (dnspolicy in
// netcfg_windows.go). NRPT rather than the adapter's DNS: Windows asks the
// resolvers of every adapter at once ("smart multi-homed name resolution")
// and takes the first answer — the provider's would race ours, and a name
// that should go through the tunnel would come back with no route made.
//
// Per query:
//   - use-application-dns.net — NXDOMAIN: Firefox's canary; its DNS over
//     HTTPS, which would pass us by, stays off by default;
//   - a name for the tunnel (every name in "all", a name of the lists in
//     "domains"): AAAA and HTTPS/SVCB are answered empty (the tunnel is IPv4
//     only — an IPv6 address would go past it), everything else is asked of
//     the tunnel's resolvers through the tunnel, and in "domains" every A of
//     the answer gets its /32 into the tunnel BEFORE the answer is let go
//     (routes.go) — the program connects the moment it has it;
//   - any other name: the physical network's resolvers, by a socket bound
//     to the physical interface.
//
// The raw query goes upstream and the raw answer comes back (the ID, EDNS,
// the TC bit and all); over TCP when it came over TCP.

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/cacggghp/vk-turn-proxy/pkg/srs"
)

const (
	dnsTimeout   = 3 * time.Second // all the resolvers of an upstream together
	dnsMaxActive = 256
	canary       = "use-application-dns.net"
)

// upstream asks some resolvers: each in turn until one answers.
type upstream struct {
	servers []string                                               // ip:port
	control func(network, address string, c syscall.RawConn) error // the bound interface, or nil
}

func (u *upstream) exchange(ctx context.Context, q []byte, tcp bool) ([]byte, error) {
	if len(u.servers) == 0 {
		return nil, errors.New("no resolver")
	}
	var last error
	for _, s := range u.servers {
		c, cancel := context.WithTimeout(ctx, max(dnsTimeout/time.Duration(len(u.servers)), time.Second))
		r, err := exchangeOne(c, s, q, tcp, u.control)
		cancel()
		if err == nil {
			return r, nil
		}
		last = err
	}
	return nil, last
}

func exchangeOne(ctx context.Context, server string, q []byte, tcp bool, control func(string, string, syscall.RawConn) error) ([]byte, error) {
	d := net.Dialer{Control: control}
	network := "udp"
	if tcp {
		network = "tcp"
	}
	conn, err := d.DialContext(ctx, network, server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if tcp {
		if err := writeTCPMsg(conn, q); err != nil {
			return nil, err
		}
		return readTCPMsg(conn)
	}
	if _, err := conn.Write(q); err != nil {
		return nil, err
	}
	buf := make([]byte, 65535)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		// An answer to another query (a late one on a reused port) is not ours.
		if n >= 2 && len(q) >= 2 && buf[0] == q[0] && buf[1] == q[1] {
			return append([]byte(nil), buf[:n]...), nil
		}
	}
}

func writeTCPMsg(w io.Writer, m []byte) error {
	b := make([]byte, 2+len(m))
	binary.BigEndian.PutUint16(b, uint16(len(m)))
	copy(b[2:], m)
	_, err := w.Write(b)
	return err
}

func readTCPMsg(r io.Reader) ([]byte, error) {
	var l [2]byte
	if _, err := io.ReadFull(r, l[:]); err != nil {
		return nil, err
	}
	m := make([]byte, binary.BigEndian.Uint16(l[:]))
	_, err := io.ReadFull(r, m)
	return m, err
}

// dnsPolicy is what the interceptor does, swapped whole when the lists or
// the network change.
type dnsPolicy struct {
	all    bool              // every name through the tunnel
	match  func(string) bool // "domains": the names for the tunnel
	tunnel *upstream
	direct *upstream
	routes *routeTable // "domains": the /32s of the answers
}

type dnsStats struct {
	Queries   atomic.Int64
	Tunneled  atomic.Int64
	Failures  atomic.Int64
	RouteErrs atomic.Int64
}

type interceptor struct {
	policy atomic.Pointer[dnsPolicy]
	stats  dnsStats
	sem    chan struct{}

	mu  sync.Mutex
	udp net.PacketConn
	tcp net.Listener
	wg  sync.WaitGroup
}

func newInterceptor(p *dnsPolicy) *interceptor {
	in := &interceptor{sem: make(chan struct{}, dnsMaxActive)}
	in.policy.Store(p)
	return in
}

// listen binds UDP and TCP on addr (127.0.0.1:53) and serves until close.
func (in *interceptor) listen(addr string) error {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", pc.LocalAddr().String())
	if err != nil {
		pc.Close()
		return err
	}
	in.mu.Lock()
	in.udp, in.tcp = pc, ln
	in.mu.Unlock()
	in.wg.Add(2)
	go in.serveUDP(pc)
	go in.serveTCP(ln)
	return nil
}

func (in *interceptor) addr() string {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.udp == nil {
		return ""
	}
	return in.udp.LocalAddr().String()
}

func (in *interceptor) close() {
	in.mu.Lock()
	if in.udp != nil {
		in.udp.Close()
	}
	if in.tcp != nil {
		in.tcp.Close()
	}
	in.mu.Unlock()
	in.wg.Wait()
}

func (in *interceptor) serveUDP(pc net.PacketConn) {
	defer in.wg.Done()
	for {
		buf := make([]byte, 4096)
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue // Windows reports a port unreachable of an earlier reply here
		}
		in.sem <- struct{}{}
		go func() {
			defer func() { <-in.sem }()
			if r := in.answer(buf[:n], false); r != nil {
				_, _ = pc.WriteTo(r, from)
			}
		}()
	}
}

func (in *interceptor) serveTCP(ln net.Listener) {
	defer in.wg.Done()
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		go func() {
			defer c.Close()
			for {
				_ = c.SetDeadline(time.Now().Add(10 * time.Second))
				q, err := readTCPMsg(c)
				if err != nil {
					return
				}
				in.sem <- struct{}{}
				r := in.answer(q, true)
				<-in.sem
				if r == nil || writeTCPMsg(c, r) != nil {
					return
				}
			}
		}()
	}
}

// answer is one query's reply, nil to drop it.
func (in *interceptor) answer(q []byte, tcp bool) []byte {
	in.stats.Queries.Add(1)
	pol := in.policy.Load()
	var p dnsmessage.Parser
	h, err := p.Start(q)
	if err != nil || h.Response {
		return nil
	}
	qs, err := p.Question()
	if err != nil {
		return reply(h, nil, dnsmessage.RCodeFormatError)
	}
	name := srs.Normalize(qs.Name.String())
	if name == canary {
		return reply(h, &qs, dnsmessage.RCodeNameError)
	}
	toTunnel := pol.all || (pol.match != nil && pol.match(name))
	up := pol.direct
	if toTunnel {
		in.stats.Tunneled.Add(1)
		switch qs.Type {
		case dnsmessage.TypeAAAA, dnsmessage.TypeHTTPS, dnsmessage.TypeSVCB:
			return reply(h, &qs, dnsmessage.RCodeSuccess)
		}
		up = pol.tunnel
	}
	ctx, cancel := context.WithTimeout(context.Background(), dnsTimeout+time.Second)
	defer cancel()
	r, err := up.exchange(ctx, q, tcp)
	if err != nil {
		in.stats.Failures.Add(1)
		return reply(h, &qs, dnsmessage.RCodeServerFailure)
	}
	if toTunnel && !pol.all && pol.routes != nil {
		for _, a := range answerAddrs(r) {
			if err := pol.routes.ensure(a.addr, a.ttl); err != nil {
				in.stats.RouteErrs.Add(1)
				log.Printf("dns: route %s (%s): %v", a.addr, name, err)
			}
		}
	}
	return r
}

type answerAddr struct {
	addr netip.Addr
	ttl  time.Duration
}

// answerAddrs: the A records of an answer, whatever names they are under
// (the CNAME chain's end).
func answerAddrs(r []byte) []answerAddr {
	var p dnsmessage.Parser
	if _, err := p.Start(r); err != nil {
		return nil
	}
	if err := p.SkipAllQuestions(); err != nil {
		return nil
	}
	var out []answerAddr
	for {
		h, err := p.AnswerHeader()
		if err != nil {
			return out
		}
		if h.Type != dnsmessage.TypeA || h.Class != dnsmessage.ClassINET {
			if p.SkipAnswer() != nil {
				return out
			}
			continue
		}
		a, err := p.AResource()
		if err != nil {
			return out
		}
		out = append(out, answerAddr{netip.AddrFrom4(a.A), time.Duration(h.TTL) * time.Second})
	}
}

// reply is an answer of our own: the question echoed, no records.
func reply(q dnsmessage.Header, qs *dnsmessage.Question, rcode dnsmessage.RCode) []byte {
	h := dnsmessage.Header{
		ID:                 q.ID,
		Response:           true,
		OpCode:             q.OpCode,
		RecursionDesired:   q.RecursionDesired,
		RecursionAvailable: true,
		RCode:              rcode,
	}
	b := dnsmessage.NewBuilder(nil, h)
	if qs != nil {
		_ = b.StartQuestions()
		_ = b.Question(*qs)
	}
	m, err := b.Finish()
	if err != nil {
		return nil
	}
	return m
}
