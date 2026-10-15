//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"
)

func setUmask() { syscall.Umask(0o077) }

func privileged() bool { return os.Geteuid() == 0 }

// SIGHUP and SIGUSR1 reconnect, SIGUSR2 writes the profiles (profile.go).
var reconnectSignals = []os.Signal{syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2}

func isProfileSignal(s os.Signal) bool { return s == syscall.SIGUSR2 }

// dirIsOurs: not writable by group or others, and our own.
func dirIsOurs(dir string, fi os.FileInfo) error {
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by others (%v)", dir, fi.Mode().Perm())
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%s belongs to uid %d", dir, st.Uid)
	}
	return nil
}

// configIsPrivate: the config holds the private key — 0600 or narrower.
func configIsPrivate(path string, f *os.File) error {
	if fi, err := f.Stat(); err == nil && fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is readable by others (%v) and holds the private key — the protocol handler writes it 0600", path, fi.Mode().Perm())
	}
	return nil
}
