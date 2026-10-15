//go:build windows

package proxy

// Windows has no socket mark and no SO_BINDTODEVICE. What keeps the proxy's
// own sockets off a tunnel that holds the default route (the Windows app,
// cmd/vkturn, "all traffic") is IP_UNICAST_IF / IPV6_UNICAST_IF: the socket
// sends by the given interface whatever the routing table prefers — the
// option WireGuard for Windows puts on its own UDP socket for the same reason.
// The "device" is the physical interface's index (a decimal number); the app
// moves it when the network changes (the daemon's "bind" command, app.go).

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"

	"golang.org/x/sys/windows"
)

const sockOptsSupported = true

// ws2ipdef.h; not in x/sys.
const (
	ipUnicastIf   = 31
	ipv6UnicastIf = 31
)

func platformSetSockOpts(fd uintptr, mark uint32, device string) error {
	if mark != 0 {
		return errors.New("no socket mark on Windows")
	}
	if device == "" {
		return nil
	}
	idx, err := strconv.ParseUint(device, 10, 32)
	if err != nil || idx == 0 {
		return fmt.Errorf("bind_device %q: an interface index on Windows", device)
	}
	// The IPv4 option takes the index in network byte order, the IPv6 one in
	// host order (the documented quirk). A socket is IPv4, IPv6 or dual-stack
	// (Go's unconnected UDP): the option of the other family fails on the
	// first two, both apply on the third.
	var be [4]byte
	binary.BigEndian.PutUint32(be[:], uint32(idx))
	h := windows.Handle(fd)
	e4 := windows.SetsockoptInt(h, windows.IPPROTO_IP, ipUnicastIf, int(binary.NativeEndian.Uint32(be[:])))
	e6 := windows.SetsockoptInt(h, windows.IPPROTO_IPV6, ipv6UnicastIf, int(idx))
	if e4 != nil && e6 != nil {
		return fmt.Errorf("IP_UNICAST_IF %d: %w", idx, e4)
	}
	return nil
}
