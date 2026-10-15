package main

// The log. netifd reads the daemon's stdout and stderr and puts every line
// into syslog itself ("netifd: vkturn (pid): ..."), with the time — so the
// lines carry no timestamp and no colour. At the info level the proxy's
// periodic diagnostics (written for iOS field debugging: memory, path and
// heartbeat lines every few seconds) are dropped: the router's log ring is
// small and shared. log_level=debug keeps everything.

import (
	"bytes"
	"io"
	"log"
	"sync"

	"golang.zx2c4.com/wireguard/device"
)

// noisy are the substrings of the periodic diagnostic lines.
var noisy = [][]byte{
	[]byte("proxy: memstats"),
	[]byte("proxy: pathstats"),
	[]byte("proxy: HEARTBEAT"),
	[]byte("proxy: heartbeat"),
	[]byte("[HEALTH]"),
	[]byte("sockstats"),
}

type filterWriter struct {
	mu    sync.Mutex
	out   io.Writer
	debug bool
}

func (f *filterWriter) Write(p []byte) (int, error) {
	if !f.debug {
		for _, n := range noisy {
			if bytes.Contains(p, n) {
				return len(p), nil
			}
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.out.Write(p)
}

func setupLog(out io.Writer, debug bool) {
	log.SetFlags(0)
	log.SetOutput(&filterWriter{out: out, debug: debug})
}

// wgLogger is wireguard-go's logger through the log package.
func wgLogger(verbose bool) *device.Logger {
	l := &device.Logger{
		Verbosef: device.DiscardLogf,
		Errorf:   func(format string, args ...any) { log.Printf("(wireguard) ERROR: "+format, args...) },
	}
	if verbose {
		l.Verbosef = func(format string, args ...any) { log.Printf("(wireguard) "+format, args...) }
	}
	return l
}
