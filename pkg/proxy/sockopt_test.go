package proxy

// The socket options (sockopt.go) reach every socket the proxy opens toward a
// remote: the four TURN control sockets and the VK and browser-TLS dialers.
// The platform call is replaced by a recorder, so the test runs anywhere and
// needs no CAP_NET_ADMIN; sockopt_linux_test.go checks the real call as root.
//
// Sabotage seen red: sockoptControl dropped from runTURN's UDP dialer, from
// setupSRTPSession's ListenConfig, and dialControl no longer applying them.

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/cbeuw/connutil"
)

type sockOptLog struct {
	mu   sync.Mutex
	seen []sockOpts
}

func (l *sockOptLog) set(_ uintptr, mark uint32, device string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = append(l.seen, sockOpts{mark: mark, device: device})
	return nil
}

func (l *sockOptLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.seen)
}

func installSockOptRecorder(t *testing.T, mark uint32, device string) *sockOptLog {
	t.Helper()
	l := &sockOptLog{}
	setSockOpts = l.set
	socketOpts.Store(&sockOpts{mark: mark, device: device}) // SetSocketOptions refuses off Linux
	t.Cleanup(func() {
		setSockOpts = platformSetSockOpts
		socketOpts.Store(nil)
	})
	return l
}

func TestSocketOptionsReachEveryTURNSocket(t *testing.T) {
	for _, tc := range []struct {
		name string
		udp  bool
		srtp bool
	}{
		{"runTURN over TCP", false, false},
		{"runTURN over UDP", true, false},
		{"setupSRTPSession over TCP", false, true},
		{"setupSRTPSession over UDP", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var addr string
			if tc.udp {
				addr, _ = udpQuotaTURN(t, 10)
			} else {
				addr = loopbackTURN(t)
			}
			rec := installSockOptRecorder(t, 0x200000, "")
			p, cp, creds := hookStandProxy(t, addr, tc.udp)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			if tc.srtp {
				go func() {
					c, err := p.setupSRTPSession(ctx, addr, creds, 0, 0, nil)
					if c != nil {
						_ = c.Close()
					}
					done <- err
				}()
			} else {
				conn1, conn2 := connutil.AsyncPacketPipe()
				defer conn1.Close()
				defer conn2.Close()
				go func() { done <- p.runTURN(ctx, addr, creds, conn2, 0, 0, nil) }()
			}
			waitUntil(t, "the relay to accept the allocation", 5*time.Second, func() bool { return slotAllocated(cp) == 1 })
			cancel()
			<-done
			if rec.count() == 0 {
				t.Fatal("the TURN control socket was opened without the socket options")
			}
			for _, o := range rec.seen {
				if o.mark != 0x200000 {
					t.Fatalf("socket got mark 0x%x, want 0x200000", o.mark)
				}
			}
		})
	}
}

// The VK session clients and the browser-TLS transport: a real dial through
// each dialer reaches the recorder.
func TestSocketOptionsReachTheVKAndBrowserDialers(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	for name, d := range map[string]*net.Dialer{"vkDiagDialer": func() *net.Dialer { d := vkDiagDialer(); return &d }(), "browserDialer": browserDialer()} {
		rec := installSockOptRecorder(t, 0, "eth9")
		c, err := d.Dial("tcp4", ln.Addr().String())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		_ = c.Close()
		if rec.count() != 1 || rec.seen[0].device != "eth9" {
			t.Fatalf("%s: options applied %v, want once with device eth9", name, rec.seen)
		}
	}
}

// No options set: nothing is asked of the platform (the app's path).
func TestNoSocketOptionsIsANoOp(t *testing.T) {
	rec := installSockOptRecorder(t, 0, "")
	socketOpts.Store(nil)
	if err := dialControl("tcp4", "203.0.113.7:443", nil); err != nil {
		t.Fatal(err)
	}
	c, err := (&net.ListenConfig{Control: sockoptControl}).ListenPacket(context.Background(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	if rec.count() != 0 {
		t.Fatalf("options applied %d time(s) with none set", rec.count())
	}
}

func TestSetSocketOptionsOffClears(t *testing.T) {
	socketOpts.Store(&sockOpts{mark: 1})
	if err := SetSocketOptions(0, ""); err != nil {
		t.Fatal(err)
	}
	if socketOpts.Load() != nil {
		t.Fatal("SetSocketOptions(0, \"\") left options set")
	}
}
