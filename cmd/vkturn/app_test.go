package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cacggghp/vk-turn-proxy/pkg/srs/srstest"
)

// --- the panel's profile ---

// Made by the server panel itself (vkturn-panel; made-up keys and links) —
// the same vector as android/.../ProfileImportTest.kt.
const panelLink = "vkturn://import?c=eyJ2IjoxLCJuYW1lIjoi0KLQtdC70LXRhNC-0L0iLCJwZWVyIjoiMjAzLjAuMTEzLjEwOjU2MDA0Iiwic2VydmVyX2tleSI6IkFRRUJBUUVCQVFFQkFRRUJBUUVCQVFFQkFRRUJBUUVCQVFFQkFRRUJBUUU9IiwicHJpdmF0ZV9rZXkiOiJBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFFPSIsInBzayI6IkFnSUNBZ0lDQWdJQ0FnSUNBZ0lDQWdJQ0FnSUNBZ0lDQWdJQ0FnSUNBZ0k9IiwiYWRkcmVzcyI6IjEwLjY2LjY2LjIvMjQiLCJkbnMiOiIxLjEuMS4xIiwibXR1IjoxMjgwLCJjb25ucyI6MjAsInRyYW5zcG9ydCI6InVkcCIsInZrX2xpbmtzIjpbImh0dHBzOi8vdmsuY29tL2NhbGwvam9pbi9BQUFBZXhhbXBsZSIsImh0dHBzOi8vdmsuY29tL2NhbGwvam9pbi9CQkJCZXhhbXBsZSJdfQ"

func TestImportsThePanelsLink(t *testing.T) {
	mine := defaultSettings()
	mine.Mode, mine.Routes, mine.TCP, mine.FallbackDNS = ModeSubnets, "198.51.100.0/24", true, "198.51.100.53"
	name, s, err := importProfile("Вот подключение:\n"+panelLink+"\nудачи", mine)
	if err != nil {
		t.Fatal(err)
	}
	if name != "Телефон" || s.Peer != "203.0.113.10:56004" || s.ServerKey != "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=" ||
		s.PrivateKey != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAE=" || s.PSK != "AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI=" ||
		s.Address != "10.66.66.2/24" || s.TunnelDNS != "1.1.1.1" || s.Conns != 20 || s.MTU != 1280 || s.TCP ||
		strings.Join(s.Links, " ") != "https://vk.com/call/join/AAAAexample https://vk.com/call/join/BBBBexample" {
		t.Fatalf("imported %q %+v", name, s)
	}
	// What the profile does not carry stays the user's.
	if s.Mode != ModeSubnets || s.Routes != mine.Routes || s.FallbackDNS != mine.FallbackDNS {
		t.Fatalf("the user's own settings changed: %+v", s)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestImportRefusesWhatIsNotAProfile(t *testing.T) {
	for _, bad := range []string{
		"",
		"https://vk.com/call/join/x",
		"vkturn://import?c=!!!",
		"vkturn://import?c=bm90IGpzb24",
		`{"v": 2, "peer": "a:1"}`,
		`{"v": 1, "peer": "a:1", "server_key": "k", "private_key": "p", "address": "10.0.0.2/24", "vk_links": []}`,
		`{"v": 1, "server_key": "k", "private_key": "p", "address": "10.0.0.2/24", "vk_links": ["x"]}`,
	} {
		if _, _, err := importProfile(bad, defaultSettings()); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// --- settings and the daemon's config ---

func goodSettings() Settings {
	_, s, _ := importProfile(panelLink, defaultSettings())
	return s
}

func TestDaemonConfig(t *testing.T) {
	s := goodSettings()
	c := s.DaemonConfig(Dirs{Root: `C:\ProgramData\VK TURN`}, "192.0.2.1", 12)
	for _, want := range []string{"iface=vkturn\n", "peer=203.0.113.10:56004\n", "vk_link=https://vk.com/call/join/AAAAexample\n",
		"vk_link=https://vk.com/call/join/BBBBexample\n", "pool_reserve=3\n", "dns_server=192.0.2.1\n", "bind_device=12\n",
		"network_events=stdin\n", "turn_transport=udp\n", "preshared_key="} {
		if !strings.Contains(c, want) {
			t.Errorf("config lacks %q:\n%s", want, c)
		}
	}
	if strings.Contains(c, "tun_fd_socket") {
		t.Error("the Windows daemon creates its adapter itself: no tun_fd_socket")
	}
	if c := s.DaemonConfig(Dirs{Root: "x"}, "", 0); !strings.Contains(c, "dns_server=77.88.8.8\n") || strings.Contains(c, "bind_device") {
		t.Errorf("no network DNS: the fallback; no index: no bind\n%s", c)
	}
}

func TestValidate(t *testing.T) {
	for name, mod := range map[string]func(*Settings){
		"no peer":        func(s *Settings) { s.Peer = "" },
		"bad key":        func(s *Settings) { s.ServerKey = "abc" },
		"no links":       func(s *Settings) { s.Links = nil },
		"http link":      func(s *Settings) { s.Links = []string{"http://vk.com/call/join/x"} },
		"v6 address":     func(s *Settings) { s.Address = "2001:db8::2/64" },
		"mtu":            func(s *Settings) { s.MTU = 1500 },
		"mode":           func(s *Settings) { s.Mode = "smart" },
		"subnets empty":  func(s *Settings) { s.Mode, s.Routes = ModeSubnets, "" },
		"domains empty":  func(s *Settings) { s.Mode, s.Lists, s.OwnDomains, s.Routes = ModeDomains, nil, "", "" },
		"http list":      func(s *Settings) { s.Lists = []string{"http://example.com/x.srs"} },
		"bad own domain": func(s *Settings) { s.OwnDomains = "https://example.com/" },
		"v6 route":       func(s *Settings) { s.Mode, s.Routes = ModeSubnets, "2001:db8::/32" },
	} {
		s := goodSettings()
		mod(&s)
		if s.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	s := goodSettings()
	s.Mode, s.Lists, s.OwnDomains = ModeDomains, nil, "*.example.com, example.org."
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if own, _ := ParseOwnDomains(s.OwnDomains); strings.Join(own, " ") != "example.com example.org" {
		t.Fatalf("own %v", own)
	}
}

// --- the lists ---

func TestListsFetchCacheAndMatch(t *testing.T) {
	body := srstest.Domains([]string{"exact.example.org"}, []string{"example.com"})
	served := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		if r.URL.Path == "/broken.srs" {
			w.Write([]byte("<html>not found</html>"))
			return
		}
		w.Write(body)
	}))
	defer srv.Close()
	dir := t.TempDir()
	l := newLists(dir)
	l.client = srv.Client()
	good, broken := srv.URL+"/ai.srs", srv.URL+"/broken.srs"
	urls := []string{good, broken}
	if !l.update(context.Background(), urls, time.Hour, false) {
		t.Fatal("nothing changed on the first fetch")
	}
	st := l.states(urls)
	if st[0].Error != "" || st[0].Names != 2 || st[0].Updated.IsZero() || st[1].Error == "" {
		t.Fatalf("states %+v", st)
	}
	match, _ := l.matcher(urls, []string{"own.example.net"})
	for n, want := range map[string]bool{"a.example.com": true, "exact.example.org": true, "x.exact.example.org": false, "own.example.net": true, "a.own.example.net": true, "example.net": false} {
		if match(n) != want {
			t.Errorf("%s: %v", n, !want)
		}
	}
	// Fresh: not fetched again; forced: fetched.
	n := served
	l.update(context.Background(), []string{good}, time.Hour, false)
	if served != n {
		t.Fatal("a fresh list fetched again")
	}
	l.update(context.Background(), []string{good}, time.Hour, true)
	if served != n+1 {
		t.Fatal("the button did not fetch")
	}
	// A new process reads the cache, and a broken download keeps it.
	l2 := newLists(dir)
	l2.client = srv.Client()
	l2.load([]string{good})
	if m, _ := l2.matcher([]string{good}, nil); !m("www.example.com") {
		t.Fatal("the cached copy was not read")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.srs"))
	if len(files) != 1 {
		t.Fatalf("cache files %v (the broken one must not be kept)", files)
	}
}

// --- the window's door ---

func TestTheUIWantsTheTokenTheHostAndTheHeader(t *testing.T) {
	tn := newTunnel(newFakePlatform(), Dirs{Root: t.TempDir()}, "", &logRing{})
	u := newUI(tn, "secret-token-0123456789abcdef0123456789", "", uiPort)
	h := u.handler()
	do := func(method, path, host string, cookie bool, header bool) int {
		r := httptest.NewRequest(method, path, strings.NewReader("{}"))
		r.Host = host
		if cookie {
			r.AddCookie(&http.Cookie{Name: "vkturn", Value: cookieValue(u.token)})
		}
		if header {
			r.Header.Set("X-VKTurn", "1")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	host := "127.0.0.1:47447"
	for _, tc := range []struct {
		name         string
		method, path string
		host         string
		cookie, hdr  bool
		want         int
	}{
		{"the page", "GET", "/", host, false, false, 200},
		{"state, no cookie", "GET", "/api/state", host, false, false, 401},
		{"state", "GET", "/api/state", host, true, false, 200},
		{"rebound name", "GET", "/api/state", "evil.example:47447", true, false, 403},
		{"other port", "GET", "/api/state", "127.0.0.1:80", true, false, 403},
		{"change, no header", "POST", "/api/disconnect", host, true, false, 403},
		{"change", "POST", "/api/disconnect", host, true, true, 200},
		{"login, bad token", "GET", "/login?t=nope", host, false, false, 401},
		{"login", "GET", "/login?t=" + u.token, host, false, false, 303},
	} {
		if got := do(tc.method, tc.path, tc.host, tc.cookie, tc.hdr); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
	// The private key never leaves.
	s := goodSettings()
	tn.settings = s
	r := httptest.NewRequest("GET", "/api/settings", nil)
	r.Host = host
	r.AddCookie(&http.Cookie{Name: "vkturn", Value: cookieValue(u.token)})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if strings.Contains(w.Body.String(), s.PrivateKey) || !strings.Contains(w.Body.String(), `"has_private_key":true`) {
		t.Fatalf("settings out: %s", w.Body.String())
	}
}

func TestLogRingSince(t *testing.T) {
	var l logRing
	for i := 0; i < logLines+5; i++ {
		l.add("line")
	}
	lines, last := l.since(-1)
	if len(lines) != logLines || last != int64(logLines+4) {
		t.Fatalf("%d lines, last %d", len(lines), last)
	}
	l.add("new")
	lines, last2 := l.since(last)
	if len(lines) != 1 || !strings.HasSuffix(lines[0], "new") || last2 != last+1 {
		t.Fatalf("%v %d", lines, last2)
	}
	if lines, _ := l.since(last2); len(lines) != 0 {
		t.Fatal("nothing new expected")
	}
}

var _ = os.Getenv
