package main

// The service's tunnel end to end over the fake platform, with the test
// binary itself as the daemon (TestMain): it writes the status the real one
// writes, records what it is told on stdin and stops when stdin ends.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	switch os.Getenv("VKTURN_FAKE_DAEMON") {
	case "":
		os.Exit(m.Run())
	case "crash":
		fmt.Println("vk-turn-proxyd: something went wrong")
		os.Exit(3)
	default:
		fakeDaemon()
	}
}

func fakeDaemon() {
	conf := os.Args[len(os.Args)-1]
	b, err := os.ReadFile(conf)
	if err != nil {
		fmt.Println("no config:", err)
		os.Exit(2)
	}
	status := ""
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "status_file="); ok {
			status = v
		}
	}
	os.MkdirAll(filepath.Dir(status), 0o700)
	write := func(state string) {
		j, _ := json.Marshal(map[string]any{"state": state, "conns_active": 3, "conns_configured": 20})
		os.WriteFile(status, j, 0o600)
	}
	write("up")
	fmt.Println("proxy: first connection up")
	rec, _ := os.Create(filepath.Join(filepath.Dir(status), "stdin.log"))
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		fmt.Fprintln(rec, sc.Text())
		rec.Sync()
	}
	rec.Close()
	write("stopping")
	fmt.Println("stopped")
	os.Exit(0)
}

func waitFor(t *testing.T, what string, limit time.Duration, f func() bool) {
	t.Helper()
	for t0 := time.Now(); time.Since(t0) < limit; time.Sleep(20 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func newTestTunnel(t *testing.T, mode string) (*tunnel, *fakePlatform) {
	t.Helper()
	fp := newFakePlatform()
	self, _ := os.Executable()
	tn := newTunnel(fp, Dirs{Root: t.TempDir()}, self, &logRing{})
	tn.dnsAddr = "127.0.0.1:0"
	s := goodSettings()
	s.Mode, s.Lists, s.OwnDomains, s.Routes = mode, nil, "example.com", "198.51.100.0/24"
	if err := tn.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	return tn, fp
}

func stdinLog(tn *tunnel) string {
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(tn.dirs.Status()), "stdin.log"))
	return string(b)
}

func TestTheTunnelInTheDomainMode(t *testing.T) {
	t.Setenv("VKTURN_FAKE_DAEMON", "1")
	tn, fp := newTestTunnel(t, ModeDomains)
	if err := tn.Start(); err != nil {
		t.Fatal(err)
	}
	// The daemon says "up", but there is no adapter yet: nothing is routed.
	waitFor(t, "the daemon's status", 5*time.Second, func() bool { return tn.View().Status != nil })
	time.Sleep(1500 * time.Millisecond)
	if v := tn.View(); v.Phase != PhaseStarting || len(fp.routes) != 0 {
		t.Fatalf("before the adapter: %s, routes %v", v.Phase, fp.routes)
	}
	fp.mu.Lock()
	fp.tun = 9
	fp.mu.Unlock()
	waitFor(t, "the adapter configured", 5*time.Second, func() bool { return tn.View().Phase == PhaseRunning })
	if fp.addr.String() != "10.66.66.2/24" {
		t.Fatalf("address %v", fp.addr)
	}
	for _, r := range []string{"198.51.100.0/24", "1.1.1.1/32"} { // the extra subnet, the tunnel's resolver
		if !fp.hasRoute(r) {
			t.Errorf("no route %s: %v", r, fp.routes)
		}
	}
	if fp.hasRoute("0.0.0.0/1") {
		t.Error("the domain mode routes the default")
	}
	if fp.policy != netip.MustParseAddr("127.0.0.1") || fp.flushes < 1 {
		t.Fatalf("DNS policy %v, flushes %d", fp.policy, fp.flushes)
	}
	if _, err := os.Stat(tn.dirs.Config()); !os.IsNotExist(err) {
		t.Fatal("the config with the private key is still there once the daemon read it")
	}
	tn.mu.Lock()
	match := tn.dns.policy.Load().match
	tn.mu.Unlock()
	if !match("chat.example.com") || match("example.org") {
		t.Fatal("the own domain does not match")
	}

	// Another network: the daemon is told to bind to it, then of it.
	fp.mu.Lock()
	fp.net = physNet{Index: 8, Name: "Wi-Fi", ID: "if8/gw198.51.100.1/198.51.100.20", DNS: []netip.Addr{netip.MustParseAddr("198.51.100.1")}}
	fp.mu.Unlock()
	want := "network if7/gw192.0.2.1/192.0.2.10\nbind 8\nnetwork if8/gw198.51.100.1/198.51.100.20\ndns 198.51.100.1\n"
	waitFor(t, "the network change on stdin", 5*time.Second, func() bool { return stdinLog(tn) == want })
	tn.mu.Lock()
	direct := tn.dns.policy.Load().direct.servers
	tn.mu.Unlock()
	if strings.Join(direct, ",") != "198.51.100.1:53" {
		t.Fatalf("the interceptor still asks %v", direct)
	}

	tn.Stop()
	v := tn.View()
	if v.Phase != PhaseOff || v.Error != "" {
		t.Fatalf("after stop: %+v", v)
	}
	if fp.policy.IsValid() {
		t.Fatal("the DNS policy outlived the tunnel")
	}
	if !strings.Contains(tn.log.last()+strings.Join(func() []string { l, _ := tn.log.since(-1); return l }(), "\n"), "stopped") {
		t.Fatal("the daemon did not stop by the end of stdin")
	}
}

func TestTheTunnelInTheAllMode(t *testing.T) {
	t.Setenv("VKTURN_FAKE_DAEMON", "1")
	tn, fp := newTestTunnel(t, ModeAll)
	fp.tun = 9
	if err := tn.Start(); err != nil {
		t.Fatal(err)
	}
	defer tn.Stop()
	waitFor(t, "running", 5*time.Second, func() bool { return tn.View().Phase == PhaseRunning })
	for _, r := range []string{"::/1", "8000::/1"} {
		if !fp.hasRoute(r) {
			t.Errorf("no %s", r)
		}
	}
	covered := func(a string) bool {
		fp.mu.Lock()
		defer fp.mu.Unlock()
		for p := range fp.routes {
			if p.Contains(netip.MustParseAddr(a)) {
				return true
			}
		}
		return false
	}
	// The local networks past it (bypass_lan by default), the rest in.
	for a, want := range map[string]bool{"8.8.8.8": true, "203.0.113.9": true, "10.66.66.1": true, "192.168.1.1": false, "10.1.1.1": false} {
		if covered(a) != want {
			t.Errorf("%s routed: %v", a, !want)
		}
	}
	tn.mu.Lock()
	all := tn.dns.policy.Load().all
	tn.mu.Unlock()
	if !all {
		t.Fatal("the interceptor is not in the all mode")
	}
}

func TestTheSubnetModeLeavesTheDNSAlone(t *testing.T) {
	t.Setenv("VKTURN_FAKE_DAEMON", "1")
	tn, fp := newTestTunnel(t, ModeSubnets)
	fp.tun = 9
	if err := tn.Start(); err != nil {
		t.Fatal(err)
	}
	defer tn.Stop()
	waitFor(t, "running", 5*time.Second, func() bool { return tn.View().Phase == PhaseRunning })
	if !fp.hasRoute("198.51.100.0/24") || len(fp.routes) != 1 || fp.policy.IsValid() {
		t.Fatalf("routes %v, policy %v", fp.routes, fp.policy)
	}
}

func TestADaemonThatDiesIsReported(t *testing.T) {
	t.Setenv("VKTURN_FAKE_DAEMON", "crash")
	tn, _ := newTestTunnel(t, ModeDomains)
	if err := tn.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the failure", 5*time.Second, func() bool { return tn.View().Phase == PhaseOff })
	if e := tn.View().Error; !strings.Contains(e, "Демон остановился") || !strings.Contains(e, "something went wrong") {
		t.Fatalf("error %q", e)
	}
}

func TestNoNetworkNoStart(t *testing.T) {
	t.Setenv("VKTURN_FAKE_DAEMON", "1")
	tn, fp := newTestTunnel(t, ModeDomains)
	fp.netOK = false
	if err := tn.Start(); err == nil || tn.View().Phase != PhaseOff {
		t.Fatalf("started with no network: %v", err)
	}
}
