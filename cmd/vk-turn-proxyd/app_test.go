package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
	go func() { readAppCommands(r, nw, reconnect, stop); close(done) }()
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

// receiveFD takes the descriptor the app sends with its first byte, as
// Android's LocalSocket does it.
func TestTheTunDescriptorComesOverTheUnixSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "fd") // short: sun_path is 104 bytes on macOS
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "tun.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	f, err := os.CreateTemp(dir, "tun")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("tun"); err != nil {
		t.Fatal(err)
	}
	go func() {
		c, err := ln.AcceptUnix()
		if err != nil {
			return
		}
		defer c.Close()
		_, _, _ = c.WriteMsgUnix([]byte{1}, syscall.UnixRights(int(f.Fd())), nil)
	}()
	fd, err := receiveFD(path)
	if err != nil {
		t.Fatal(err)
	}
	got := os.NewFile(uintptr(fd), "received")
	defer got.Close()
	buf := make([]byte, 3)
	if _, err := got.ReadAt(buf, 0); err != nil || string(buf) != "tun" {
		t.Fatalf("read %q, %v: not the file that was sent", buf, err)
	}

	go func() {
		c, err := ln.AcceptUnix()
		if err == nil {
			_, _ = c.Write([]byte{1}) // a byte and no descriptor: the VPN was refused
			c.Close()
		}
	}()
	if _, err := receiveFD(path); err == nil || !strings.Contains(err.Error(), "no descriptor") {
		t.Fatalf("no descriptor: %v", err)
	}
}
