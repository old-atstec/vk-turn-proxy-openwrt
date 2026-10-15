package main

// What the user set, kept by the service in settings.json in its data
// directory (SYSTEM and the administrators only — it holds the private key).
// The daemon gets it as its config file (key=value, the format of the
// router's protocol handler, cmd/vk-turn-proxyd/config.go), the network as
// routes and the DNS policy (tunnel.go). The fields are the Android app's
// (android/.../Settings.kt), plus the domain mode.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// The modes: what goes into the tunnel.
const (
	ModeAll     = "all"     // everything (0.0.0.0/0, the local networks optionally past it)
	ModeSubnets = "subnets" // the listed subnets only
	ModeDomains = "domains" // the names of the lists, their addresses as they resolve
)

// poolReserve: a laptop changes networks as a phone does (Wi-Fi ↔ cable,
// sleep): the Android app's 3 — two changes with 1 were ten dead minutes.
const poolReserve = 3

type Settings struct {
	Peer        string   `json:"peer"`
	ServerKey   string   `json:"server_key"`
	PrivateKey  string   `json:"private_key"`
	PSK         string   `json:"psk"`
	Links       []string `json:"links"`
	Conns       int      `json:"conns"`
	TCP         bool     `json:"tcp"`
	FallbackDNS string   `json:"fallback_dns"` // the proxy's own lookups when the network names no resolver
	Address     string   `json:"address"`      // 10.66.66.2/24, as on the router
	TunnelDNS   string   `json:"tunnel_dns"`
	MTU         int      `json:"mtu"`

	Mode      string `json:"mode"`
	Routes    string `json:"routes"`     // subnets: what goes in; domains: subnets in besides the names
	BypassLAN bool   `json:"bypass_lan"` // the local networks past the tunnel (all, subnets)

	Lists      []string `json:"lists"`       // .srs URLs (domains)
	OwnDomains string   `json:"own_domains"` // names of the user's own, a suffix each
	ListHours  int      `json:"list_hours"`  // how often the lists are fetched anew, 0 = by the button only

	AutoConnect bool `json:"auto_connect"` // connect when the service starts (Windows starts)
}

func defaultSettings() Settings {
	return Settings{
		Conns:       20,
		FallbackDNS: "77.88.8.8",
		TunnelDNS:   "1.1.1.1",
		MTU:         1280,
		Mode:        ModeDomains,
		Routes:      "",
		BypassLAN:   true,
		Lists:       []string{"https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/sing/geo/geosite/category-ai-!cn.srs"},
		ListHours:   24,
	}
}

// Validate returns the first thing wrong, in the user's words, or nil.
func (s *Settings) Validate() error {
	if h, p, err := net.SplitHostPort(s.Peer); err != nil || h == "" || !portOK(p) {
		return errors.New("Адрес сервера: нужен host:port")
	}
	if !isKey(s.ServerKey) {
		return errors.New("Публичный ключ сервера: не ключ WireGuard (base64, 32 байта)")
	}
	if !isKey(s.PrivateKey) {
		return errors.New("Приватный ключ: не ключ WireGuard — нажмите «Новый ключ»")
	}
	if s.PSK != "" && !isKey(s.PSK) {
		return errors.New("Preshared key: не ключ WireGuard")
	}
	if len(s.Links) == 0 {
		return errors.New("Нужна хотя бы одна ссылка на звонок VK")
	}
	for _, l := range s.Links {
		if u, err := url.Parse(l); err != nil || u.Scheme != "https" || u.Host == "" || strings.ContainsAny(l, " \t\r\n") {
			return fmt.Errorf("Ссылка на звонок %q: нужна https-ссылка", l)
		}
	}
	if s.Conns < 1 || s.Conns > 120 {
		return errors.New("Соединений: от 1 до 120")
	}
	if p, err := netip.ParsePrefix(s.Address); err != nil || !p.Addr().Is4() || p.Bits() < 8 || p.Bits() > 32 {
		return errors.New("Адрес в туннеле: нужен IPv4 с префиксом, как на роутере, например 10.66.66.2/24")
	}
	if _, err := s.TunnelDNSList(); err != nil {
		return err
	}
	if s.FallbackDNS != "" {
		if a, err := netip.ParseAddr(s.FallbackDNS); err != nil || !a.Is4() {
			return errors.New("DNS для запросов к VK: IPv4-адрес")
		}
	}
	if s.MTU < 1000 || s.MTU > 1400 {
		return errors.New("MTU: от 1000 до 1400")
	}
	routes, err := ParsePrefixes(s.Routes)
	if err != nil {
		return fmt.Errorf("Маршруты: %v", err)
	}
	switch s.Mode {
	case ModeAll:
	case ModeSubnets:
		if len(routes) == 0 {
			return errors.New("Режим «по подсетям»: нужен хотя бы один IPv4 CIDR")
		}
	case ModeDomains:
		if len(s.Lists) == 0 && strings.TrimSpace(s.OwnDomains) == "" && len(routes) == 0 {
			return errors.New("Режим «по доменам»: нужен хотя бы один список, свой домен или подсеть")
		}
		for _, l := range s.Lists {
			if u, err := url.Parse(l); err != nil || u.Scheme != "https" || u.Host == "" {
				return fmt.Errorf("Список %q: нужна https-ссылка на .srs", l)
			}
		}
		if _, err := ParseOwnDomains(s.OwnDomains); err != nil {
			return err
		}
	default:
		return fmt.Errorf("Режим %q: all, subnets или domains", s.Mode)
	}
	if s.ListHours < 0 || s.ListHours > 24*30 {
		return errors.New("Обновление списков: от 0 до 720 часов")
	}
	return nil
}

// TunnelDNSList: the resolvers inside the tunnel, IPv4.
func (s *Settings) TunnelDNSList() ([]netip.Addr, error) {
	var out []netip.Addr
	for _, f := range fields(s.TunnelDNS) {
		a, err := netip.ParseAddr(f)
		if err != nil || !a.Is4() {
			return nil, errors.New("DNS в туннеле: IPv4-адреса через запятую")
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil, errors.New("DNS в туннеле: нужен хотя бы один адрес")
	}
	return out, nil
}

// TunnelPrefix is the address with its prefix (10.66.66.2/24).
func (s *Settings) TunnelPrefix() netip.Prefix {
	p, _ := netip.ParsePrefix(s.Address)
	return p
}

// DaemonConfig is the daemon's config file (cmd/vk-turn-proxyd/config.go).
// dns is the physical network's resolver for the proxy's own lookups, bind
// the physical interface's index its sockets leave by.
func (s *Settings) DaemonConfig(dirs Dirs, dns string, bind int) string {
	var b strings.Builder
	kv := func(k string, v any) { fmt.Fprintf(&b, "%s=%v\n", k, v) }
	b.WriteString("# written by the VK TURN service; the daemon reads it once\n")
	kv("iface", tunName)
	kv("peer", s.Peer)
	kv("private_key", s.PrivateKey)
	kv("public_key", s.ServerKey)
	if s.PSK != "" {
		kv("preshared_key", s.PSK)
	}
	kv("mtu", s.MTU)
	for _, l := range s.Links {
		kv("vk_link", l)
	}
	kv("conns", s.Conns)
	if s.TCP {
		kv("turn_transport", "tcp")
	} else {
		kv("turn_transport", "udp")
	}
	kv("pool_reserve", poolReserve)
	if dns == "" {
		dns = s.FallbackDNS
	}
	if dns != "" {
		kv("dns_server", dns)
	}
	if bind > 0 {
		kv("bind_device", strconv.Itoa(bind))
	}
	kv("status_file", dirs.Status())
	kv("cred_cache", dirs.CredCache())
	kv("network_events", "stdin")
	return b.String()
}

// The routes of the modes "all" and "subnets": the user's, minus the local
// networks if asked — but never the tunnel's own subnet (Android's lesson:
// 10.66.66.0/24 lies in 10/8, and the server's tunnel address went past).
func (s *Settings) StaticRoutes() []netip.Prefix {
	var r []netip.Prefix
	switch s.Mode {
	case ModeAll:
		r = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}
	default:
		r, _ = ParsePrefixes(s.Routes)
	}
	if s.Mode != ModeDomains && s.BypassLAN {
		local := LocalNetworks
		if own := s.TunnelPrefix().Masked(); own.IsValid() && own.Bits() < 32 {
			local = Subtract(local, []netip.Prefix{own})
		}
		r = Subtract(r, local)
	}
	return SplitDefault(r)
}

func load(path string) (Settings, error) {
	s := defaultSettings()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return defaultSettings(), fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

func fields(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t' || r == ';' })
}

// ParsePrefixes reads IPv4 subnets (or single addresses), comma or line
// separated.
func ParsePrefixes(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range fields(s) {
		p, err := netip.ParsePrefix(f)
		if err != nil {
			a, err2 := netip.ParseAddr(f)
			if err2 != nil {
				return nil, fmt.Errorf("%q: не IPv4 CIDR", f)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if !p.Addr().Is4() {
			return nil, fmt.Errorf("%q: туннель только IPv4", f)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// ParseOwnDomains reads the user's own names: one per line or comma, each
// matching itself and its subdomains (domain_suffix); a leading "*." or "."
// is dropped.
func ParseOwnDomains(s string) ([]string, error) {
	var out []string
	for _, f := range fields(s) {
		d := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(f, "*"), "."), "."))
		if d == "" || len(d) > 253 || strings.ContainsAny(d, "/:@") {
			return nil, fmt.Errorf("Свои домены: %q — не имя", f)
		}
		out = append(out, d)
	}
	return out, nil
}

func portOK(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n > 0 && n < 65536
}

func isKey(s string) bool {
	b, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(b) == 32 && len(s) == 44
}

// randomToken: 32 random bytes, URL-safe.
func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
