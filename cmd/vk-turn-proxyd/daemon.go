package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"

	"github.com/cacggghp/vk-turn-proxy/pkg/proxy"
	"github.com/cacggghp/vk-turn-proxy/pkg/turnbind"
)

var socketControl = proxy.SocketControl

// handshakeStale: the keepalive keeps the tunnel sending, so WireGuard
// handshakes anew every REKEY_AFTER_TIME (120 s) and, over a live path, has
// its answer within a round trip; retries go every 5 s. A handshake older than
// this is a path that answers nothing — whatever the proxy's session count
// says: over a path that went silent its sessions die one by one, over
// minutes (seen on the router: 20 → 4 in 150 s with every packet dropped).
const handshakeStale = 140 * time.Second

type daemon struct {
	c      config
	st     status
	p      *proxy.Proxy
	dev    *device.Device
	linkUp bool
	cookie string

	deadSince time.Time // the tunnel stopped carrying, while up
	lastErr   string
}

func newDaemon(c config) *daemon {
	d := &daemon{c: c}
	d.st = status{
		Version:          version,
		Commit:           commit,
		Iface:            c.Iface,
		PID:              os.Getpid(),
		Peer:             c.Peer,
		Transport:        transportName(c.UseUDP),
		ConnsConfigured:  c.Conns,
		DownAfterSeconds: int(c.DownAfter / time.Second),
	}
	return d
}

func (d *daemon) setState(s string) {
	if d.st.State != s {
		d.st.State = s
		d.st.StateFrom = time.Now().Unix()
	}
	d.publish()
}

// publish writes the status file with the proxy's numbers as they are now.
func (d *daemon) publish() {
	if d.p != nil {
		d.st.fill(d.p.GetStats(), readWG(d.dev))
	}
	d.st.LinkUp = d.linkUp
	d.st.LastError = d.lastErr
	if d.c.SimulateCaptcha {
		d.st.CaptchaNeeded = true
		if d.st.CaptchaURL == "" {
			d.st.CaptchaURL = "https://id.vk.ru/not_robot_captcha?simulated=1"
		}
	}
	if err := writeStatus(d.c.StatusFile, &d.st); err != nil {
		log.Printf("status: %v", err)
	}
}

func (d *daemon) fail(format string, args ...any) int {
	d.lastErr = fmt.Sprintf(format, args...)
	log.Print(d.lastErr)
	d.publish()
	return 1
}

func (d *daemon) run(ctx context.Context) int {
	syscall.Umask(0o077)
	if err := prepareDir(filepath.Dir(d.c.StatusFile)); err != nil {
		log.Printf("status directory: %v", err)
		return 1
	}
	defer os.Remove(d.c.StatusFile)
	d.setState("starting")
	log.Printf("%s: interface %s, server %s, %d connections over %s", versionString(), d.c.Iface, d.c.Peer, d.c.Conns, transportName(d.c.UseUDP))

	if d.c.CredCache != "" {
		if err := prepareDir(filepath.Dir(d.c.CredCache)); err != nil {
			return d.fail("cred_cache: %v", err)
		}
		if dropped, err := matchCacheToLink(d.c.CredCache, d.c.VKLinks[0]); err != nil {
			return d.fail("cred_cache: %v", err)
		} else if dropped {
			log.Print("the call link changed: the credential cache of the old one is dropped")
		}
		if dropFreshCache(d.c.CredCache, time.Now()) {
			log.Printf("the credential cache is from a run that ended less than %s ago: minting fresh (its identities may still hold seats at the relay)", cacheFreshWindow)
		}
	}
	if err := proxy.SetSocketOptions(d.c.Fwmark, d.c.BindDevice); err != nil {
		return d.fail("fwmark/bind_device: %v", err)
	}
	if d.c.Fwmark != 0 || d.c.BindDevice != "" {
		log.Printf("the proxy's sockets: mark 0x%x, device %q", d.c.Fwmark, d.c.BindDevice)
	}
	installResolver(d.c.DNSServer)
	if d.c.CookieFile != "" {
		if err := d.loadCookie(); err != nil {
			return d.fail("vk_cookie_file: %v", err)
		}
	}

	d.startProxy()
	defer d.shutdown()

	d.setState("connecting")
	if err := d.waitFirstSession(ctx); err != nil {
		if ctx.Err() != nil {
			return 0
		}
		return d.fail("%v", err)
	}
	if err := d.attach(); err != nil {
		return d.fail("%v", err)
	}
	d.setState("handshake")
	if !d.waitHandshake(ctx) {
		return 0
	}
	d.notify("up")

	mctx, cancel := context.WithCancel(ctx)
	defer cancel()
	mon := newNetMonitor(func() (gateway, bool) { return readDefaultRoute(d.c.Iface) }, networkIdentity)
	if mon.up {
		d.st.WAN = mon.cur.String()
		log.Printf("network: via %s", mon.cur)
	}
	go mon.run(mctx, 2*time.Second, d.onNetwork)

	return d.serve(ctx)
}

// prepareDir makes dir (0700) if it is missing and refuses one that a
// non-root user could write or that is a symlink: the files in it hold the
// TURN credentials and the status, and are written by root.
func prepareDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by others (%v)", dir, fi.Mode().Perm())
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%s belongs to uid %d", dir, st.Uid)
	}
	return nil
}

// matchCacheToLink keeps the credential cache only while the call link it was
// minted from is the one configured: the cache itself does not name its link,
// and a link changed in LuCI must not go on running on the old call's
// credentials. The link's fingerprint (never the link) is kept beside the
// cache as <cache>.link. Reports whether a cache was dropped.
func matchCacheToLink(cache, link string) (bool, error) {
	sum := sha256.Sum256([]byte(link))
	want := hex.EncodeToString(sum[:])
	mark := cache + ".link"
	have, err := os.ReadFile(mark)
	if err == nil && strings.TrimSpace(string(have)) == want {
		return false, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	// No fingerprint, or another link's: a cache there is not this link's.
	dropped := false
	if err := os.Remove(cache); err == nil {
		dropped = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(mark), ".link-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name()) // a no-op after the rename
	if _, err := tmp.WriteString(want + "\n"); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	return dropped, os.Rename(tmp.Name(), mark)
}

// cacheFreshWindow: the pool loads a slot used in its last ten minutes as
// PENDING — the relay may still hold its allocations — and a pool whose every
// slot is pending mints nothing and waits those minutes out (seen on the
// router: a restart one second after a stop sat ten minutes at zero
// connections). A restart that soon — ifup, an upgrade, a reboot — mints fresh
// identities instead (the VK Calls path takes about a second); an older cache
// is kept and spares the mints, which matters most when VK asks for a captcha.
const cacheFreshWindow = 11 * time.Minute

// dropFreshCache removes the cache when it was written within the window.
func dropFreshCache(cache string, now time.Time) bool {
	fi, err := os.Lstat(cache)
	if err != nil || !fi.Mode().IsRegular() || now.Sub(fi.ModTime()) >= cacheFreshWindow {
		return false
	}
	return os.Remove(cache) == nil
}

func (d *daemon) loadCookie() error {
	f, err := os.Open(d.c.CookieFile)
	if err != nil {
		return err
	}
	defer f.Close()
	cookies, err := parseNetscapeCookies(f)
	if err != nil {
		return err
	}
	cookie, exp, err := vkCookieHeader(cookies, time.Now())
	if err != nil {
		return err
	}
	d.cookie = cookie
	if !exp.IsZero() {
		log.Printf("VK login from %s, valid until %s", d.c.CookieFile, exp.Format("2006-01-02"))
	}
	return nil
}

// noCaptchaUI: a router has no WebView. Every pool path asks VK with the
// solver off and publishes a captcha instead of blocking; NewProxy's default
// would wait for an answer from a UI that does not exist.
func noCaptchaUI(string) (string, error) {
	return "", errors.New("the router has no captcha UI")
}

func (d *daemon) startProxy() {
	c := d.c
	if c.UplinkPaceKiB > 0 {
		proxy.SetUplinkPace(c.UplinkPaceKiB, 16) // 16 KiB: the app's burst
	} else {
		proxy.SetUplinkPace(0, 0)
	}
	if d.cookie != "" {
		proxy.SetVKCookieAuth(true, d.cookie, c.VKLinks) // before NewProxy: the pool is sized by it
	} else {
		proxy.SetVKCookieAuth(false, "", nil)
	}
	size := poolSize(c.Conns, c.PoolReserve)
	d.p = proxy.NewProxy(proxy.Config{
		PeerAddr:      c.Peer,
		TurnServer:    c.TurnServer,
		TurnPort:      c.TurnPort,
		VKLink:        c.VKLinks[0],
		UseDTLS:       true,
		UseSrtp:       true,
		UseUDP:        c.UseUDP,
		NumConns:      c.Conns,
		CredCachePath: c.CredCache,
		CaptchaSolver: noCaptchaUI,
		GOMAXPROCS:    -1,
		CredPoolSize:  size,
	})
	go func() {
		if err := d.p.Start(); err != nil {
			log.Printf("proxy: %v", err)
		}
	}()
}

// waitFirstSession waits, with no limit, for the first TURN session: a
// bootstrap that fails is retried by the proxy's own watchdog. Only what no
// retry mends ends the wait — a dead call link, a VK login VK refused.
func (d *daemon) waitFirstSession(ctx context.Context) error {
	t0 := time.Now()
	boot := make(chan error, 1)
	go func() { boot <- d.p.WaitBootstrap(100 * 365 * 24 * time.Hour) }()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	captchaSaid := false
	for {
		if msg := proxy.CookieAuthFatalError(); msg != "" {
			return fmt.Errorf("VK refused the login: %s — export the cookies anew", msg)
		}
		s := d.p.GetStats()
		if s.ActiveConns > 0 {
			log.Printf("proxy: first connection up in %s", time.Since(t0).Round(time.Millisecond))
			d.lastErr = ""
			return nil
		}
		if s.CaptchaImageURL != "" && !captchaSaid {
			log.Printf("proxy: VK asks for a captcha its automatic solver could not pass — asked again every 2 min (10 under a rate limit); the status shows the link")
			captchaSaid = true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-boot:
			if err == nil {
				continue
			}
			if strings.HasPrefix(err.Error(), "resolve peer") {
				return fmt.Errorf("the server's address: %v", err) // Start stops there; no watchdog retries
			}
			var dead *proxy.CallUnavailableError
			if errors.As(err, &dead) {
				return fmt.Errorf("the call link is dead (VK %d: %s) — make a new call and set vk_link", dead.Code, dead.Message)
			}
			d.lastErr = err.Error()
			log.Printf("proxy: %v — its watchdog retries; waiting", err)
		case <-tick.C:
			d.publish()
		case <-poll.C:
		}
	}
}

// attach: the tun device, named after the interface, and WireGuard on the
// proxy's bind. The device is up and has no address: netifd gives it one
// when the hook says "up".
func (d *daemon) attach() error {
	tdev, err := tun.CreateTUN(d.c.Iface, d.c.MTU)
	if err != nil {
		return fmt.Errorf("tun %s: %w", d.c.Iface, err)
	}
	if err := setLinkUp(d.c.Iface); err != nil {
		_ = tdev.Close()
		return fmt.Errorf("tun %s up: %w", d.c.Iface, err)
	}
	uapi, err := uapiConfig(d.c)
	if err != nil {
		_ = tdev.Close()
		return err
	}
	d.dev = device.NewDevice(proxy.WrapTUNForStats(tdev), turnbind.NewTURNBind(d.p), wgLogger(d.c.WGVerbose))
	if err := d.dev.IpcSet(uapi); err != nil {
		return fmt.Errorf("wireguard: %v", err)
	}
	if err := d.dev.Up(); err != nil {
		return fmt.Errorf("wireguard: %v", err)
	}
	log.Printf("tunnel: %s mtu %d", d.c.Iface, d.c.MTU)
	return nil
}

func (d *daemon) waitHandshake(ctx context.Context) bool {
	t0 := time.Now()
	warned := false
	last := time.Now()
	for readWG(d.dev).lastHandshake.IsZero() {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
		if time.Since(last) > 5*time.Second {
			d.publish()
			last = time.Now()
		}
		if !warned && time.Since(t0) > 30*time.Second {
			d.lastErr = "no WireGuard handshake after 30 s — the server's public key, its -srtp listener, its peer for this key?"
			log.Print("wireguard: " + d.lastErr + " still waiting")
			warned = true
		}
	}
	d.lastErr = ""
	log.Printf("wireguard: handshake in %s", time.Since(t0).Round(time.Millisecond))
	return true
}

// alive: the tunnel carries traffic — a TURN session up and a WireGuard
// handshake that WireGuard still honours.
func alive(s proxy.Stats, hs time.Time, now time.Time) bool {
	return s.ActiveConns > 0 && !hs.IsZero() && now.Sub(hs) < handshakeStale
}

// linkStep is the up/down decision of one tick: down after downAfter without
// a live tunnel, up again the moment it lives.
func linkStep(up, live bool, deadSince, now time.Time, downAfter time.Duration) (newUp bool, newDeadSince time.Time) {
	switch {
	case live:
		return true, time.Time{}
	case !up:
		return false, deadSince
	case deadSince.IsZero():
		return true, now
	case now.Sub(deadSince) >= downAfter:
		return false, deadSince
	}
	return true, deadSince
}

func (d *daemon) serve(ctx context.Context) int {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGHUP, syscall.SIGUSR1)
	defer signal.Stop(sig)
	var stats <-chan time.Time
	if d.c.StatsEvery > 0 {
		t := time.NewTicker(d.c.StatsEvery)
		defer t.Stop()
		stats = t.C
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			log.Print("stopping")
			d.printStats()
			return 0
		case s := <-sig:
			log.Printf("%v: reconnecting every connection", s)
			d.p.ForceReconnect()
		case <-stats:
			d.printStats()
		case now := <-tick.C:
			if msg := proxy.CookieAuthFatalError(); msg != "" {
				return d.fail("VK refused the login: %s — export the cookies anew", msg)
			}
			live := alive(d.p.GetStats(), readWG(d.dev).lastHandshake, now)
			up, since := linkStep(d.linkUp, live, d.deadSince, now, d.c.DownAfter)
			if since != d.deadSince && !since.IsZero() {
				log.Printf("tunnel: no live session — down in %s unless it comes back", d.c.DownAfter)
			}
			d.deadSince = since
			switch {
			case up && !d.linkUp:
				log.Print("tunnel: carrying again")
				d.notify("up")
			case !up && d.linkUp:
				log.Printf("tunnel: nothing carried for %s", d.c.DownAfter)
				d.notify("down")
			}
			if n++; n%5 == 0 {
				d.publish()
			}
		}
	}
}

// notify tells netifd through the protocol's hook; the state follows.
func (d *daemon) notify(what string) {
	d.linkUp = what == "up"
	if d.linkUp {
		d.setState("up")
	} else {
		d.setState("down")
	}
	if d.c.Notify == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.c.Notify, what)
	cmd.Env = append(os.Environ(), "INTERFACE="+d.c.Iface, "IFNAME="+d.c.Iface)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("notify %s: %v: %s", what, err, strings.TrimSpace(string(out)))
	}
}

func (d *daemon) onNetwork(ev netEvents) {
	switch {
	case ev.changed && !ev.up:
		log.Printf("network: gone (was via %s)", ev.prev)
		d.st.WAN = ""
	case ev.changed:
		log.Printf("network: via %s", ev.cur)
		d.st.WAN = ev.cur.String()
	}
	tellPath(d.p, proxy.RotateVKSessionClient, ev)
}

func (d *daemon) printStats() {
	s := d.p.GetStats()
	hs := "never"
	if t := readWG(d.dev).lastHandshake; !t.IsZero() {
		hs = time.Since(t).Round(time.Second).String() + " ago"
	}
	extra := ""
	if s.CaptchaImageURL != "" {
		extra += " · captcha pending"
	}
	if s.CredPoolQuotaRefusals > 0 {
		extra += fmt.Sprintf(" · 486 ×%d", s.CredPoolQuotaRefusals)
	}
	log.Printf("stats: conns %d/%d · sessions %d since start · tx %.1f MB rx %.1f MB · watchdog restarts %d · pool %d/%d (relays %d) · turn rtt %.0f ms · wg handshake %s%s",
		s.ActiveConns, d.c.Conns, s.TotalConns, float64(s.TxBytes)/1e6, float64(s.RxBytes)/1e6, s.Reconnects,
		s.CredPoolWithCreds, s.CredPoolSize, s.CredPoolDistinctRelays, s.TurnRTTms, hs, extra)
}

// shutdown: the proxy first (it gives the TURN allocations back), then the
// device — and the tun with it.
func (d *daemon) shutdown() {
	d.linkUp = false
	d.setState("stopping")
	if d.p != nil {
		d.p.StopWithTimeout(2 * time.Second)
	}
	if d.dev != nil {
		d.dev.Close()
	}
	log.Print("stopped")
}

type wgState struct {
	rxBytes, txBytes int64
	lastHandshake    time.Time
}

func readWG(dev *device.Device) wgState {
	var st wgState
	if dev == nil {
		return st
	}
	out, err := dev.IpcGet()
	if err != nil {
		return st
	}
	var sec, nsec int64
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "rx_bytes":
			st.rxBytes, _ = strconv.ParseInt(v, 10, 64)
		case "tx_bytes":
			st.txBytes, _ = strconv.ParseInt(v, 10, 64)
		case "last_handshake_time_sec":
			sec, _ = strconv.ParseInt(v, 10, 64)
		case "last_handshake_time_nsec":
			nsec, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if sec > 0 {
		st.lastHandshake = time.Unix(sec, nsec)
	}
	return st
}

func transportName(udp bool) string {
	if udp {
		return "udp"
	}
	return "tcp"
}
