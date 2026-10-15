// vk-turn-proxyd — the router daemon of vk-turn-proxy: WireGuard over
// DTLS-SRTP over VK's TURN relays to the server (anton48/vk-turn-proxy,
// -srtp), as a network interface of OpenWrt's netifd.
//
//	vk-turn-proxyd -config /var/run/vkturn/vkturn/config   # started by netifd (proto vkturn)
//	vk-turn-proxyd -config <file> -check                     # validate a config and exit
//	vk-turn-proxyd genkey | vk-turn-proxyd pubkey            # WireGuard keys, as wg genkey / wg pubkey
//	vk-turn-proxyd version
//
// The division of labour (openwrt/README.md):
//
//   - the daemon creates the tun device named after the interface, runs the
//     proxy and WireGuard on it, and tells netifd when the tunnel carries
//     traffic and when it stopped, through the protocol's hook
//     (/lib/netifd/vkturn-notify.sh up|down);
//   - netifd puts the addresses and the routes on the device, and takes them
//     off again — the daemon never runs `ip`, never touches the default
//     route, the DNS or any route of the router, and journals nothing: there
//     is nothing of its own to take back but the device, which goes with it;
//   - the proxy's own sockets leave by the main table (the WAN), optionally
//     marked (fwmark) or bound to a device (bind_device) so that no policy
//     routing on the router (NetShift) ever catches them.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// version and commit are set by the build: -ldflags "-X main.version=… -X main.commit=…".
var (
	version = "dev"
	commit  = ""
)

func main() {
	os.Exit(realMain(os.Args[1:], os.Stdin, os.Stdout))
}

func realMain(args []string, stdin *os.File, stdout *os.File) int {
	signal.Ignore(syscall.SIGPIPE) // netifd's log pipe may go away; the shutdown must not
	setupLog(os.Stderr, false)
	if len(args) > 0 {
		switch args[0] {
		case "genkey":
			k, err := genKey()
			if err != nil {
				log.Print(err)
				return 1
			}
			fmt.Fprintln(stdout, k)
			return 0
		case "pubkey":
			k, err := pubKeyFrom(stdin)
			if err != nil {
				log.Print(err)
				return 1
			}
			fmt.Fprintln(stdout, k)
			return 0
		case "version", "-version", "--version":
			fmt.Fprintln(stdout, versionString())
			return 0
		}
	}
	fs := flag.NewFlagSet("vk-turn-proxyd", flag.ContinueOnError)
	configPath := fs.String("config", "", "the config file the protocol handler wrote (key=value lines, 0600)")
	check := fs.Bool("check", false, "validate the config and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 || *configPath == "" {
		log.Print("usage: vk-turn-proxyd -config <file> [-check] | genkey | pubkey | version")
		return 2
	}
	scrubEnv()
	c, err := loadConfig(*configPath)
	if err != nil {
		log.Printf("config %s: %v", *configPath, err)
		return 2
	}
	setupLog(os.Stderr, c.Debug)
	if len(c.VKLinks) > 1 && c.CookieFile == "" {
		log.Printf("config: %d call links given; the anonymous mode mints from the first one only (openwrt/TODO.md)", len(c.VKLinks))
	}
	if *check {
		fmt.Fprintln(stdout, "config OK")
		return 0
	}
	if os.Geteuid() != 0 {
		log.Print("run it as root: it creates a tun device")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return newDaemon(c).run(ctx)
}

func versionString() string {
	if commit == "" {
		return "vk-turn-proxyd " + version
	}
	return "vk-turn-proxyd " + version + " (" + commit + ")"
}

// scrubEnv drops the proxy's debugging switches (VK_*): one of them writes a
// file to a path it names (VK_DUMP_POW_HTML), others change which VK
// endpoints and credentials are used. procd and netifd pass no environment
// of the user's, but a manual start might; on a router none of them belong.
func scrubEnv() {
	var dropped []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "VK_") {
			os.Unsetenv(k)
			dropped = append(dropped, k)
		}
	}
	if len(dropped) > 0 {
		log.Printf("ignoring the debugging environment: %s", strings.Join(dropped, " "))
	}
}

// installResolver points the process's own lookups (VK's API and captcha
// hosts) at server, through sockets with the proxy's socket options; with
// none the system's resolver (dnsmasq on the router) is used.
func installResolver(server string) {
	if server == "" {
		return
	}
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second, Control: socketControl}
			return d.DialContext(ctx, network, server)
		},
	}
}
