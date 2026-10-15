package main

// fakePlatform: the operating system's side recorded, for the tests and for
// the window's demo off Windows (main_other.go).

import (
	"errors"
	"net/netip"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

type fakePlatform struct {
	mu      sync.Mutex
	net     physNet
	netOK   bool
	tun     int
	routes  map[netip.Prefix]bool
	policy  netip.Addr
	flushes int
	addr    netip.Prefix
	addErr  error
}

func newFakePlatform() *fakePlatform {
	return &fakePlatform{
		net:    physNet{Index: 7, Name: "Ethernet", ID: "if7/gw192.0.2.1/192.0.2.10", DNS: []netip.Addr{netip.MustParseAddr("192.0.2.1")}},
		netOK:  true,
		routes: map[netip.Prefix]bool{},
	}
}

func (f *fakePlatform) physical() (physNet, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.net, f.netOK
}

func (f *fakePlatform) tunIndex() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tun == 0 {
		return 0, errors.New("no adapter")
	}
	return f.tun, nil
}

func (f *fakePlatform) configure(idx int, addr netip.Prefix, mtu int) error {
	f.mu.Lock()
	f.addr = addr
	f.mu.Unlock()
	return nil
}

func (f *fakePlatform) addRoute(idx int, p netip.Prefix) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		return f.addErr
	}
	f.routes[p] = true
	return nil
}

func (f *fakePlatform) delRoute(idx int, p netip.Prefix) error {
	f.mu.Lock()
	delete(f.routes, p)
	f.mu.Unlock()
	return nil
}

func (f *fakePlatform) hasRoute(p string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.routes[netip.MustParsePrefix(p)]
}

func (f *fakePlatform) setDNSPolicy(a netip.Addr) error {
	f.mu.Lock()
	f.policy = a
	f.mu.Unlock()
	return nil
}

func (f *fakePlatform) clearDNSPolicy() error {
	f.mu.Lock()
	f.policy = netip.Addr{}
	f.mu.Unlock()
	return nil
}

func (f *fakePlatform) flushDNS() {
	f.mu.Lock()
	f.flushes++
	f.mu.Unlock()
}

func (f *fakePlatform) bindControl(int) func(string, string, syscall.RawConn) error { return nil }
func (f *fakePlatform) prepareCmd(*exec.Cmd)                                        {}
func (f *fakePlatform) adopt(*os.Process) error                                     { return nil }
