package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTheAppModeKeysParse(t *testing.T) {
	c, err := parseConfig(strings.NewReader(goodConfig() + "tun_fd_socket=/data/x/tun.sock\nnetwork_events=stdin\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.TunFDSocket != "/data/x/tun.sock" || !c.AppEvents {
		t.Fatalf("parsed %+v", c)
	}
	if c, _ := parseConfig(strings.NewReader(goodConfig())); c.TunFDSocket != "" || c.AppEvents {
		t.Fatalf("the router's config is in the app mode: %+v", c)
	}
	if _, err := parseConfig(strings.NewReader(goodConfig() + "network_events=netlink\n")); err == nil {
		t.Fatal("network_events=netlink parsed")
	}
}

// The app's commands drive the same monitor the router's route table does:
// one Wi-Fi → LTE switch is one handover, "none" then a network is gone and
// back (the VK client rotated once), and the first name is no handover.
func TestTheAppsNetworkCommandsAreOneHandoverEach(t *testing.T) {
	nw := newAppNetwork()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	reconnect := make(chan struct{}, 1)
	done := make(chan struct{})
	var bound []string
	bind := func(dev string) error { bound = append(bound, dev); return nil }
	go func() { readAppCommands(r, nw, reconnect, bind, stop); close(done) }()
	say := func(line string) {
		if _, err := w.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond) // the reader's turn
	}

	say("network 100")
	m := newNetMonitor(nw.read, appIdentity)
	var p pathLog
	rotations := 0
	step := func() { tellPath(&p, func() { rotations++ }, m.step()) }
	step() // nothing changed
	say("bind 7")
	say("network 101")
	step()
	say("network none")
	step()
	step()
	say("network 102")
	step()
	say("reconnect")
	say("reconnect") // one pending is enough; the reader never blocks
	say("dns 192.0.2.53")
	say("bogus")
	w.Close()
	<-done

	want := "change,up,change,transition,up"
	if got := strings.Join(p, ","); got != want {
		t.Fatalf("the proxy was told %s, want %s", got, want)
	}
	if rotations != 1 {
		t.Fatalf("the VK client rotated %d times, want 1", rotations)
	}
	if strings.Join(bound, ",") != "7" {
		t.Fatalf("bound to %v, want [7]", bound)
	}
	if len(reconnect) != 1 {
		t.Fatalf("%d reconnects pending, want 1", len(reconnect))
	}
	if got := *resolverServer.Load(); got != "192.0.2.53:53" {
		t.Fatalf("resolver %s", got)
	}
	if ctx.Err() == nil {
		t.Fatal("the end of stdin did not stop the daemon")
	}
}
