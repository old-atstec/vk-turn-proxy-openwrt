//go:build unix

package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

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
