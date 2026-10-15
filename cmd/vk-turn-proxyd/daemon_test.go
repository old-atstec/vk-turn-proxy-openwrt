package main

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cacggghp/vk-turn-proxy/pkg/proxy"
)

const (
	tPriv = "dwdtCnMYpX08FsFyUbJmRd9ML4frwJkqsXf7pR25LCo=" // RFC 7748 §6.1, Alice's private key
	tPub  = "hSDwCYkwp1R0i33ctD73Wg2/Og0mOBr066SpjqqbTmo=" // … and her public key
	tSrv  = "xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg="
)

func goodConfig() string {
	return strings.Join([]string{
		"# written by vkturn.sh",
		"iface=vkturn",
		"peer=203.0.113.10:56004",
		"private_key=" + tPriv,
		"public_key=" + tSrv,
		"vk_link=https://vk.com/call/join/AAAA",
		"vk_link=https://vk.com/call/join/BBBB",
		"conns=20",
		"turn_transport=udp",
		"fwmark=0x200000",
		"down_after=60",
		"status_file=/var/run/vkturn/vkturn/status.json",
		"notify=/lib/netifd/vkturn-notify.sh",
		"",
	}, "\n")
}

func TestAConfigOfTheProtocolHandlerParses(t *testing.T) {
	c, err := parseConfig(strings.NewReader(goodConfig()))
	if err != nil {
		t.Fatal(err)
	}
	if c.Iface != "vkturn" || c.Conns != 20 || !c.UseUDP || c.Fwmark != 0x200000 || len(c.VKLinks) != 2 || c.DownAfter != time.Minute {
		t.Fatalf("parsed %+v", c)
	}
	if c.MTU != mtuStandard || c.Keepalive != 25 || c.PoolReserve != 1 || !c.SpreadRelays {
		t.Fatalf("defaults lost: mtu %d keepalive %d reserve %d spread_relays %v", c.MTU, c.Keepalive, c.PoolReserve, c.SpreadRelays)
	}
	if c, err := parseConfig(strings.NewReader(goodConfig() + "spread_relays=0\n")); err != nil || c.SpreadRelays {
		t.Fatalf("spread_relays=0: %v, spread %v", err, c.SpreadRelays)
	}
	if c.CPUAffinity != 0 {
		t.Fatalf("cpu_affinity is on by default: 0x%x", c.CPUAffinity)
	}
	if c, err := parseConfig(strings.NewReader(goodConfig() + "cpu_affinity=0x30\n")); err != nil || c.CPUAffinity != 0x30 {
		t.Fatalf("cpu_affinity=0x30: %v, mask 0x%x", err, c.CPUAffinity)
	}
	if _, err := parseConfig(strings.NewReader(goodConfig() + "cpu_affinity=big\n")); err == nil {
		t.Fatal("cpu_affinity=big parsed")
	}
}

func TestABadConfigIsNamedWithoutItsSecret(t *testing.T) {
	for name, tc := range map[string]struct{ cfg, want string }{
		"bad private key":  {strings.Replace(goodConfig(), tPriv, "c2VjcmV0LXNlY3JldA==", 1), "private_key"},
		"no link":          {strings.ReplaceAll(goodConfig(), "vk_link=", "#"), "vk_link"},
		"too many conns":   {strings.Replace(goodConfig(), "conns=20", "conns=500", 1), "conns"},
		"unknown key":      {goodConfig() + "default_route=1\n", "unknown key"},
		"twice":            {goodConfig() + "conns=10\n", "twice"},
		"bad transport":    {strings.Replace(goodConfig(), "turn_transport=udp", "turn_transport=quic", 1), "udp or tcp"},
		"long iface":       {strings.Replace(goodConfig(), "iface=vkturn", "iface=a-very-long-interface", 1), "iface"},
		"psk not a key":    {goodConfig() + "preshared_key=hunter2hunter2\n", "preshared_key"},
		"peer not a pair":  {strings.Replace(goodConfig(), "peer=203.0.113.10:56004", "peer=203.0.113.10", 1), "peer"},
		"not key=value":    {goodConfig() + "garbage\n", "key=value"},
		"no status file":   {strings.Replace(goodConfig(), "status_file=", "#", 1), "status_file"},
		"bad turn server":  {goodConfig() + "turn_server=relay\n", "host:port"},
		"bad fwmark":       {strings.Replace(goodConfig(), "fwmark=0x200000", "fwmark=mark", 1), "fwmark"},
		"bad dns":          {goodConfig() + "dns_server=resolver\n", "dns_server"},
		"down_after small": {strings.Replace(goodConfig(), "down_after=60", "down_after=1", 1), "down_after"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseConfig(strings.NewReader(tc.cfg))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one naming %q", err, tc.want)
			}
			for _, secret := range []string{"c2VjcmV0LXNlY3JldA==", "hunter2hunter2", tPriv} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("the error carries a secret: %v", err)
				}
			}
		})
	}
}

func TestTheConfigMustNotBeReadableByOthers(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(p, []byte(goodConfig()), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(p); err == nil || !strings.Contains(err.Error(), "readable by others") {
		t.Fatalf("a 0644 config was accepted: %v", err)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(p); err != nil {
		t.Fatal(err)
	}
}

func TestMTUIsClampedAsTheApp(t *testing.T) {
	for in, want := range map[string]int{"0": 1280, "576": 1000, "1500": 1400, "1350": 1350} {
		c, err := parseConfig(strings.NewReader(goodConfig() + "mtu=" + in + "\n"))
		if err != nil || c.MTU != want {
			t.Errorf("mtu=%s → %d (%v), want %d", in, c.MTU, err, want)
		}
	}
}

func TestProcRoute(t *testing.T) {
	const table = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"vkturn\t00000000\t00000000\t0001\t0\t0\t0\t00000000\t0\t0\t0\n" + // the tunnel itself: skipped
		"eth0\t00000000\t017100CB\t0003\t0\t0\t10\t00000000\t0\t0\t0\n" +
		"wwan0\t00000000\t0101A8C0\t0003\t0\t0\t20\t00000000\t0\t0\t0\n" +
		"eth0\t007100CB\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n"
	g, ok := parseProcRoute(strings.NewReader(table), "vkturn")
	if !ok || g.Iface != "eth0" || g.IP != "203.0.113.1" {
		t.Fatalf("default = %+v %v, want 203.0.113.1 dev eth0", g, ok)
	}
	if _, ok := parseProcRoute(strings.NewReader(strings.SplitN(table, "\n", 2)[0]+"\n"), ""); ok {
		t.Fatal("an empty table has a default route")
	}
	g, ok = parseProcRoute(strings.NewReader("Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\n"+
		"pppoe-wan\t00000000\t00000000\t0001\t0\t0\t0\t00000000\n"), "")
	if !ok || g.Iface != "pppoe-wan" || g.IP != "" {
		t.Fatalf("point-to-point default = %+v %v", g, ok)
	}
}

type pathLog []string

func (p *pathLog) OnPathChange()     { *p = append(*p, "change") }
func (p *pathLog) OnPathTransition() { *p = append(*p, "transition") }
func (p *pathLog) OnPathUp()         { *p = append(*p, "up") }

// A WAN reconnect is ONE path change — gone, then back — and a new WAN
// address on the same gateway is a new network.
func TestAWANReconnectIsOneHandover(t *testing.T) {
	wan := gateway{IP: "203.0.113.1", Iface: "eth0"}
	addr := "203.0.113.10/24"
	up := true
	m := newNetMonitor(func() (gateway, bool) { return wan, up }, func(g gateway, u bool) string {
		if !u {
			return "down"
		}
		return g.String() + " " + addr
	})
	var p pathLog
	rotations := 0
	step := func() { tellPath(&p, func() { rotations++ }, m.step()) }
	step() // nothing changed
	up = false
	step()
	step()
	up = true
	step()
	addr = "203.0.113.77/24"
	step()
	want := "change,transition,up,change,up"
	if got := strings.Join(p, ","); got != want {
		t.Fatalf("the proxy was told %s, want %s", got, want)
	}
	if rotations != 1 {
		t.Fatalf("the VK client rotated %d times, want 1 (back after gone)", rotations)
	}
}

func TestLinkGoesDownOnlyAfterDownAfterAndUpAtOnce(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	up, since := true, time.Time{}
	step := func(live bool, at time.Duration) {
		up, since = linkStep(up, live, since, t0.Add(at), time.Minute)
	}
	step(false, 0)
	if !up {
		t.Fatal("down at the first dead tick")
	}
	step(false, 59*time.Second)
	if !up {
		t.Fatal("down before down_after")
	}
	step(true, 59*time.Second+500*time.Millisecond) // came back in time: the clock restarts
	step(false, 70*time.Second)
	step(false, 129*time.Second)
	if !up {
		t.Fatal("the dead time before the recovery was counted")
	}
	step(false, 130*time.Second)
	if up {
		t.Fatal("still up after down_after without a live session")
	}
	step(false, 300*time.Second)
	if up {
		t.Fatal("up again with nothing live")
	}
	step(true, 301*time.Second)
	if !up || !since.IsZero() {
		t.Fatal("not up at once when the tunnel lives again")
	}
}

func TestAliveNeedsASessionAndAFreshHandshake(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, tc := range []struct {
		conns int32
		hs    time.Time
		want  bool
	}{
		{1, now.Add(-time.Minute), true},
		{0, now.Add(-time.Minute), false},
		{5, time.Time{}, false},
		{5, now.Add(-handshakeStale), false},
	} {
		if got := alive(proxy.Stats{ActiveConns: tc.conns}, tc.hs, now); got != tc.want {
			t.Errorf("alive(conns %d, hs %v) = %v", tc.conns, tc.hs, got)
		}
	}
}

func TestKeys(t *testing.T) {
	pub, err := pubKey(tPriv)
	if err != nil || pub != tPub {
		t.Fatalf("pubkey = %q %v, want %q", pub, err, tPub)
	}
	k, err := genKey()
	if err != nil || checkKey(k) != nil {
		t.Fatalf("genkey = %q %v", k, err)
	}
	if k2, _ := genKey(); k2 == k {
		t.Fatal("genkey repeats itself")
	}
	if _, err := pubKey("not a key"); err == nil {
		t.Fatal("pubkey of garbage")
	}
}

func TestUAPIHasThePeerAndNoBase64(t *testing.T) {
	c, err := parseConfig(strings.NewReader(goodConfig()))
	if err != nil {
		t.Fatal(err)
	}
	u, err := uapiConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"replace_peers=true", "endpoint=203.0.113.10:56004", "persistent_keepalive_interval=25", "allowed_ip=0.0.0.0/0"} {
		if !strings.Contains(u, want) {
			t.Errorf("uapi lacks %q", want)
		}
	}
	if strings.Contains(u, tPriv) {
		t.Fatal("the uapi carries the base64 key (it wants hex)")
	}
}

func TestStatusIsWrittenWholeAndPrivate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "status.json")
	s := &status{Iface: "vkturn", State: "up", CaptchaNeeded: true, CaptchaURL: "https://id.vk.ru/x"}
	if err := writeStatus(p, s); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("status file mode %v", fi.Mode().Perm())
	}
	var back map[string]any
	b, _ := os.ReadFile(p)
	if err := json.Unmarshal(b, &back); err != nil || back["state"] != "up" || back["captcha_needed"] != true {
		t.Fatalf("status = %s (%v)", b, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}
}

func TestPrepareDirRefusesWhatOthersCouldWrite(t *testing.T) {
	base := t.TempDir()
	ok := filepath.Join(base, "ok", "nested")
	if err := prepareDir(ok); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(ok); fi.Mode().Perm() != 0o700 {
		t.Fatalf("made %v, want 0700", fi.Mode().Perm())
	}
	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := prepareDir(open); err == nil {
		t.Fatal("a world-writable directory was accepted")
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(ok, link); err != nil {
		t.Fatal(err)
	}
	if err := prepareDir(link); err == nil {
		t.Fatal("a symlink was accepted as the directory")
	}
}

func TestTheInfoLogDropsThePeriodicDiagnostics(t *testing.T) {
	var buf bytes.Buffer
	w := &filterWriter{out: &buf}
	w.Write([]byte("proxy: memstats 5s rss=10MB\n"))
	w.Write([]byte("proxy: pathstats tick os-default=203.0.113.4\n"))
	w.Write([]byte("pion/turnc: Refresh timer 2 expired\n"))
	w.Write([]byte("  conn 3:  TX 1.2 KB/s (2.0 MB cum)  RX 9.1 KB/s (40.1 MB cum)\n"))
	w.Write([]byte("proxy: reorder(downlink): 120 pkts, 1.2% out-of-order\n"))
	w.Write([]byte("proxy: first connection up in 1s\n"))
	if got := buf.String(); got != "proxy: first connection up in 1s\n" {
		t.Fatalf("info log = %q", got)
	}
	buf.Reset()
	w.debug = true
	w.Write([]byte("proxy: memstats 5s\n"))
	if buf.Len() == 0 {
		t.Fatal("debug dropped a line")
	}
}

func TestTheDebuggingEnvironmentIsDropped(t *testing.T) {
	t.Setenv("VK_DUMP_POW_HTML", filepath.Join(t.TempDir(), "x"))
	t.Setenv("VK_SKIP_VKCALLS", "1")
	scrubEnv()
	for _, k := range []string{"VK_DUMP_POW_HTML", "VK_SKIP_VKCALLS"} {
		if _, ok := os.LookupEnv(k); ok {
			t.Errorf("%s survived", k)
		}
	}
}

// The commands that need no root — the key tools and -check — write nothing
// in the working directory (ТЗ 6.1: nothing is journaled, nothing in CWD).
func TestNothingIsWrittenInTheWorkingDirectory(t *testing.T) {
	cwd := t.TempDir()
	cfgDir := t.TempDir()
	cfg := filepath.Join(cfgDir, "config")
	if err := os.WriteFile(cfg, []byte(goodConfig()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	in, _ := os.Open(os.DevNull)
	defer in.Close()
	out, _ := os.Create(filepath.Join(cfgDir, "out"))
	defer out.Close()
	for _, args := range [][]string{{"version"}, {"genkey"}, {"-config", cfg, "-check"}} {
		if rc := realMain(args, in, out); rc != 0 {
			t.Fatalf("%v: exit %d", args, rc)
		}
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Fatalf("written in the working directory: %v", entries)
	}
}

// The daemon changes nothing of the router's but its own device: it runs no
// program but the netifd hook — no ip, route, resolvectl, uci — and writes no
// file but the status, the profiles of SIGUSR2 beside it (and, through the
// proxy, the credential cache). A scan
// of the package's source, since the real calls need root and a router.
func TestTheDaemonRunsNoNetworkToolAndWritesNoSystemFile(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	execs, writes := 0, 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch pkg.Name + "." + sel.Sel.Name {
			case "exec.Command", "exec.CommandContext", "syscall.Exec", "os.StartProcess":
				execs++
				if len(call.Args) < 2 || !strings.Contains(fset.Position(call.Pos()).Filename, "daemon.go") {
					t.Errorf("%s: a program is run outside the netifd hook", fset.Position(call.Pos()))
				}
				if s, ok := call.Args[1].(*ast.SelectorExpr); !ok || s.Sel.Name != "Notify" {
					t.Errorf("%s: runs something other than the configured hook", fset.Position(call.Pos()))
				}
			case "os.WriteFile", "os.Create", "os.OpenFile":
				writes++
				t.Errorf("%s: writes a file (%s) — only the status is written, through writeStatus", fset.Position(call.Pos()), sel.Sel.Name)
			case "os.CreateTemp":
				writes++
			}
			return true
		})
	}
	if execs != 1 || writes != 3 {
		t.Fatalf("the scan found %d exec and %d write sites, want 1 and 3 (the status, the link's fingerprint, the CPU profile) — it no longer reads what it was written for", execs, writes)
	}
}

func TestPoolSize(t *testing.T) {
	for _, tc := range [][3]int{{20, 1, 4}, {30, 0, 3}, {1, 1, 2}, {120, 3, 48}} {
		if got := poolSize(tc[0], tc[1]); got != tc[2] {
			t.Errorf("poolSize(%d, %d) = %d, want %d", tc[0], tc[1], got, tc[2])
		}
	}
}

// A call link changed in LuCI drops the credentials of the old one; the same
// link keeps them; the fingerprint never holds the link itself.
func TestTheCredentialCacheFollowsTheCallLink(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "creds.json")
	const a, b = "https://vk.ru/call/join/AAAA", "https://vk.ru/call/join/BBBB"
	write := func() {
		if err := os.WriteFile(cache, []byte(`{"creds":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	exists := func() bool { _, err := os.Stat(cache); return err == nil }

	write() // a cache from before the fingerprint: its link is unknown
	if dropped, err := matchCacheToLink(cache, a); err != nil || !dropped || exists() {
		t.Fatalf("an unfingerprinted cache: dropped=%v err=%v exists=%v, want it dropped", dropped, err, exists())
	}
	write()
	if dropped, err := matchCacheToLink(cache, a); err != nil || dropped || !exists() {
		t.Fatalf("the same link: dropped=%v err=%v exists=%v, want the cache kept", dropped, err, exists())
	}
	if dropped, err := matchCacheToLink(cache, b); err != nil || !dropped || exists() {
		t.Fatalf("a new link: dropped=%v err=%v exists=%v, want the cache dropped", dropped, err, exists())
	}
	mark, _ := os.ReadFile(cache + ".link")
	if strings.Contains(string(mark), "BBBB") || len(strings.TrimSpace(string(mark))) != 64 {
		t.Fatalf("fingerprint = %q", mark)
	}
	if dropped, err := matchCacheToLink(filepath.Join(dir, "none.json"), a); err != nil || dropped {
		t.Fatalf("no cache at all: dropped=%v err=%v", dropped, err)
	}
}

func TestTheVKRUCallLinkIsAccepted(t *testing.T) {
	c, err := parseConfig(strings.NewReader(strings.Replace(goodConfig(), "vk_link=https://vk.com/call/join/AAAA", "vk_link=https://vk.ru/call/join/Ab-cD_ef0123456789_ExampleLink-xyzXYZ01", 1)))
	if err != nil || c.VKLinks[0] != "https://vk.ru/call/join/Ab-cD_ef0123456789_ExampleLink-xyzXYZ01" {
		t.Fatalf("vk.ru link: %v %v", c.VKLinks, err)
	}
}

// A cache written moments ago is dropped (its slots would all load pending
// and the pool would wait ten minutes); an old one is kept.
func TestAFreshCredentialCacheIsDropped(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "creds.json")
	if err := os.WriteFile(cache, []byte(`{"creds":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * time.Minute)
	if err := os.Chtimes(cache, old, old); err != nil {
		t.Fatal(err)
	}
	if dropFreshCache(cache, time.Now()) {
		t.Fatal("a 30-minute-old cache was dropped")
	}
	now := time.Now()
	if err := os.Chtimes(cache, now, now); err != nil {
		t.Fatal(err)
	}
	if !dropFreshCache(cache, now.Add(time.Minute)) {
		t.Fatal("a cache written a minute ago was kept")
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatal("the fresh cache is still there")
	}
	if dropFreshCache(cache, now) {
		t.Fatal("a missing cache reported as dropped")
	}
}

func TestAStaleHandshakeWithSessionsUpReconnectsAtMostEveryKickEvery(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	up := proxy.Stats{ActiveConns: 20}
	stale := now.Add(-handshakeStale - time.Second)
	if needKick(up, now.Add(-30*time.Second), time.Time{}, now) {
		t.Fatal("kicked with a fresh handshake")
	}
	if !needKick(up, stale, time.Time{}, now) {
		t.Fatal("no kick with sessions up and a stale handshake")
	}
	if needKick(proxy.Stats{}, stale, time.Time{}, now) {
		t.Fatal("kicked with no session (the proxy is reconnecting already)")
	}
	if needKick(up, time.Time{}, time.Time{}, now) {
		t.Fatal("kicked before the first handshake")
	}
	if needKick(up, stale, now.Add(-time.Minute), now) {
		t.Fatal("kicked again a minute after the last kick")
	}
	if !needKick(up, stale, now.Add(-kickEvery), now) {
		t.Fatal("no kick kickEvery after the last one")
	}
}
