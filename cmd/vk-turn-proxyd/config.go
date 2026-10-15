package main

// The daemon's config: one file of key=value lines, written by the netifd
// protocol handler (openwrt/vk-turn-proxy/files/vkturn.sh) from the
// interface's UCI section into /var/run/vkturn/<iface>/config — 0600 in a
// 0700 directory, since it holds the WireGuard private key, which never goes
// on a command line or into the environment.
//
//	iface=vkturn
//	peer=203.0.113.10:56004
//	private_key=<base64>
//	public_key=<base64>
//	vk_link=https://vk.com/call/join/...
//	conns=20
//
// Blank lines and lines starting with # are skipped; a key given twice is a
// list (vk_link) or an error. Nothing in a value is ever printed for a
// secret key.

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// maxConns: the console's ceiling; above ~60 on one relay host the relay may
// blackhole (upstream's note), so the UCI default is 20.
const maxConns = 120

// The app's tunnel MTU rule (TunnelMTU.swift): 1280 by default, 1000…1400.
const (
	mtuStandard = 1280
	mtuMinimum  = 1000
	mtuMaximum  = 1400
)

type config struct {
	Iface         string
	Peer          string
	PrivateKey    string
	PublicKey     string
	PresharedKey  string
	MTU           int
	Keepalive     int
	VKLinks       []string
	Conns         int
	UseUDP        bool
	TurnServer    string // host, "" = VK's own
	TurnPort      string
	PoolReserve   int
	UplinkPaceKiB int
	SpreadRelays  bool   // every relay of the call, not the first (proxy.Config.SpreadRelays)
	CPUAffinity   uint64 // CPU mask the daemon runs on, 0 = every CPU (affinity_linux.go)
	CookieFile    string
	CredCache     string
	Fwmark        uint32
	BindDevice    string // a device name; on Windows the interface index (pkg/proxy/sockopt_windows.go)
	DNSServer     string // host:port, "" = the system's resolver
	DownAfter     time.Duration
	StatsEvery    time.Duration
	StatusFile    string
	Notify        string // the netifd hook: <Notify> up|down
	TunFDSocket   string // the app mode (app.go): the tun comes from the app over this unix socket
	AppEvents     bool   // network_events=stdin: the app tells the network changes (app.go)
	Debug         bool
	WGVerbose     bool

	// SimulateCaptcha publishes "captcha needed" in the status whatever VK
	// says: the LuCI status page's test, never for production.
	SimulateCaptcha bool
}

// secretKeys are never echoed in an error.
var secretKeys = map[string]bool{"private_key": true, "preshared_key": true}

func defaultConfig() config {
	return config{
		MTU:          mtuStandard,
		Keepalive:    25,
		Conns:        20,
		UseUDP:       true,
		PoolReserve:  1,
		SpreadRelays: true,
		DownAfter:    60 * time.Second,
	}
}

func loadConfig(path string) (config, error) {
	f, err := os.Open(path)
	if err != nil {
		return config{}, err
	}
	defer f.Close()
	if err := configIsPrivate(path, f); err != nil {
		return config{}, err
	}
	return parseConfig(f)
}

func parseConfig(r io.Reader) (config, error) {
	c := defaultConfig()
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return config{}, fmt.Errorf("line %d: not key=value", n)
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if seen[k] && k != "vk_link" {
			return config{}, fmt.Errorf("line %d: %s given twice", n, k)
		}
		seen[k] = true
		if err := c.set(k, v); err != nil {
			if secretKeys[k] {
				return config{}, fmt.Errorf("line %d: %s: %v", n, k, err)
			}
			return config{}, fmt.Errorf("line %d: %s=%q: %v", n, k, v, err)
		}
	}
	if err := sc.Err(); err != nil {
		return config{}, err
	}
	return c, c.validate()
}

func (c *config) set(k, v string) error {
	var err error
	switch k {
	case "iface":
		c.Iface = v
	case "peer":
		c.Peer = v
	case "private_key":
		c.PrivateKey = v
	case "public_key":
		c.PublicKey = v
	case "preshared_key":
		c.PresharedKey = v
	case "mtu":
		c.MTU, err = intIn(v, 0, 9000)
		if c.MTU == 0 {
			c.MTU = mtuStandard
		}
		c.MTU = min(max(c.MTU, mtuMinimum), mtuMaximum)
	case "keepalive":
		c.Keepalive, err = intIn(v, 0, 3600)
	case "vk_link":
		if v != "" {
			c.VKLinks = append(c.VKLinks, v)
		}
	case "conns":
		c.Conns, err = intIn(v, 1, maxConns)
	case "turn_transport":
		switch v {
		case "udp", "":
			c.UseUDP = true
		case "tcp":
			c.UseUDP = false
		default:
			err = errors.New("udp or tcp")
		}
	case "turn_server":
		if v == "" {
			return nil
		}
		h, p, e := net.SplitHostPort(v)
		if e != nil || h == "" {
			return errors.New("host:port")
		}
		if _, e := strconv.Atoi(p); e != nil {
			return errors.New("host:port")
		}
		c.TurnServer, c.TurnPort = h, p
	case "pool_reserve":
		c.PoolReserve, err = intIn(v, 0, 10)
	case "uplink_pace":
		c.UplinkPaceKiB, err = intIn(v, 0, 1<<20)
	case "vk_cookie_file":
		c.CookieFile = v
	case "cred_cache":
		c.CredCache = v
	case "fwmark":
		if v == "" || v == "0" {
			c.Fwmark = 0
			return nil
		}
		var m uint64
		m, err = strconv.ParseUint(v, 0, 32)
		c.Fwmark = uint32(m)
	case "cpu_affinity":
		c.CPUAffinity, err = cpuMask(v)
	case "bind_device":
		c.BindDevice = v
	case "dns_server":
		if v == "" {
			return nil
		}
		if net.ParseIP(v) != nil {
			v = net.JoinHostPort(v, "53")
		}
		if _, _, e := net.SplitHostPort(v); e != nil {
			return errors.New("an IP address or ip:port")
		}
		c.DNSServer = v
	case "down_after":
		var s int
		s, err = intIn(v, 5, 86400)
		c.DownAfter = time.Duration(s) * time.Second
	case "stats_every":
		var s int
		s, err = intIn(v, 0, 86400)
		c.StatsEvery = time.Duration(s) * time.Second
	case "status_file":
		c.StatusFile = v
	case "notify":
		c.Notify = v
	case "tun_fd_socket":
		c.TunFDSocket = v
	case "network_events":
		switch v {
		case "route", "":
			c.AppEvents = false
		case "stdin":
			c.AppEvents = true
		default:
			err = errors.New("route or stdin")
		}
	case "log_level":
		switch v {
		case "info", "":
			c.Debug = false
		case "debug":
			c.Debug = true
		default:
			err = errors.New("info or debug")
		}
	case "spread_relays":
		c.SpreadRelays, err = boolOf(v)
	case "wg_verbose":
		c.WGVerbose, err = boolOf(v)
	case "simulate_captcha":
		c.SimulateCaptcha, err = boolOf(v)
	default:
		return errors.New("unknown key")
	}
	return err
}

func (c *config) validate() error {
	if c.Iface == "" {
		return errors.New("iface: missing")
	}
	if len(c.Iface) > 15 || strings.ContainsAny(c.Iface, "/ \t") {
		return fmt.Errorf("iface %q: not a Linux interface name", c.Iface)
	}
	for _, k := range []struct{ name, v string }{{"private_key", c.PrivateKey}, {"public_key", c.PublicKey}} {
		if err := checkKey(k.v); err != nil {
			return fmt.Errorf("%s %v", k.name, err)
		}
	}
	if c.PresharedKey != "" {
		if err := checkKey(c.PresharedKey); err != nil {
			return fmt.Errorf("preshared_key %v", err)
		}
	}
	if h, p, err := net.SplitHostPort(c.Peer); err != nil || h == "" || p == "" {
		return errors.New("peer: missing or not host:port (the server's SRTP listener)")
	}
	if len(c.VKLinks) == 0 {
		return errors.New("vk_link: missing — the TURN credentials are minted from a VK call link")
	}
	if c.StatusFile == "" {
		return errors.New("status_file: missing")
	}
	return nil
}

func checkKey(b64 string) error {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return errors.New("is not base64")
	}
	if len(raw) != 32 {
		return fmt.Errorf("is %d bytes, a WireGuard key is 32", len(raw))
	}
	return nil
}

func intIn(v string, lo, hi int) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, errors.New("not a number")
	}
	if n < lo || n > hi {
		return 0, fmt.Errorf("out of %d…%d", lo, hi)
	}
	return n, nil
}

// cpuMask is cpu_affinity: a CPU bit mask as taskset takes it (0x30 = CPUs 4
// and 5); empty or 0 is off.
func cpuMask(v string) (uint64, error) {
	if v == "" || v == "0" {
		return 0, nil
	}
	m, err := strconv.ParseUint(v, 0, 64)
	if err != nil {
		return 0, errors.New("a CPU mask such as 0x30")
	}
	return m, nil
}

func boolOf(v string) (bool, error) {
	switch v {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off", "":
		return false, nil
	}
	return false, errors.New("a boolean")
}

// poolSize is the console's credential pool: (1 + R) × ceil(N/10) identities —
// ten allocations per identity and relay, R reserve sets that ride a network
// change without a mint.
func poolSize(conns, reserve int) int {
	return (1 + reserve) * ((conns + 9) / 10)
}
