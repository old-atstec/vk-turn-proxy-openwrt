//go:build linux

package proxy

import (
	"context"
	"net"
	"os"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// The real SO_MARK on a real socket: needs CAP_NET_ADMIN, so root only.
func TestSocketMarkIsSetOnLinux(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("SO_MARK needs CAP_NET_ADMIN")
	}
	if err := SetSocketOptions(0x200000, ""); err != nil {
		t.Fatal(err)
	}
	defer SetSocketOptions(0, "")
	pc, err := (&net.ListenConfig{Control: sockoptControl}).ListenPacket(context.Background(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	raw, err := pc.(syscall.Conn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var mark int
	var gerr error
	_ = raw.Control(func(fd uintptr) { mark, gerr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK) })
	if gerr != nil || mark != 0x200000 {
		t.Fatalf("SO_MARK = 0x%x (%v), want 0x200000", mark, gerr)
	}
}
