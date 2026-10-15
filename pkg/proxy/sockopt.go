package proxy

// Socket options for every socket the proxy opens toward a remote: a firewall
// mark (SO_MARK) and/or a bound device (SO_BINDTODEVICE).
//
// On a router the proxy's own traffic must never be caught by the policy
// routing it serves: NetShift (sing-box) marks the router's OWN output to its
// fake-IP range and its subnets as well (chain mangle_output), and a user may
// one day route the default through the tunnel. A mark that the router's rules
// let pass (NetShift returns 0x200000, sing-box's own egress mark) or a bound
// WAN device keeps the TURN sockets, the VK API clients and the captcha's
// hosts on the physical path whatever the routing tables say.
//
// The options ride the same Control hook as the dial hook (dialhook.go):
// dialControl applies them after asking the hook, sockoptControl applies them
// alone where the hook was already asked by hand. dialhook_test.go's scan
// keeps every dialer and listener on one of the two.

import (
	"errors"
	"sync/atomic"
	"syscall"
)

type sockOpts struct {
	mark   uint32
	device string
}

var socketOpts atomic.Pointer[sockOpts]

// setSockOpts is the platform's way to put o on one socket (sockopt_linux.go).
// A variable so that the tests can see every socket it is asked about.
var setSockOpts = platformSetSockOpts

// SetSocketOptions makes every socket the proxy opens from now on carry the
// mark (0 = none) and be bound to the device ("" = none). Both empty turns
// the options off. An error: the platform has no such options.
func SetSocketOptions(mark uint32, device string) error {
	if mark == 0 && device == "" {
		socketOpts.Store(nil)
		return nil
	}
	if !sockOptsSupported {
		return errors.New("socket mark and bound device are supported on Linux only")
	}
	socketOpts.Store(&sockOpts{mark: mark, device: device})
	return nil
}

// applySockOpts puts the configured options on the socket behind c, if any.
// c is nil when a test calls a Control by hand.
func applySockOpts(c syscall.RawConn) error {
	o := socketOpts.Load()
	if o == nil || c == nil {
		return nil
	}
	var serr error
	if err := c.Control(func(fd uintptr) { serr = setSockOpts(fd, o.mark, o.device) }); err != nil {
		return err
	}
	return serr
}

// sockoptControl is a net.Dialer / net.ListenConfig Control that applies the
// socket options only — for the sites that asked the dial hook themselves.
func sockoptControl(_, _ string, c syscall.RawConn) error {
	return applySockOpts(c)
}

// SocketControl is sockoptControl for the code around the proxy (the
// daemon's own resolver): its sockets leave the same way the proxy's do.
func SocketControl(network, address string, c syscall.RawConn) error {
	return sockoptControl(network, address, c)
}
