package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync/atomic"
	"time"
)

// SIGUSR2 writes a profile of the next profileFor next to the status file:
// cpu.pprof, mutex.pprof and block.pprof (0600, the status directory is 0700).
// For finding what holds the throughput under load on the router itself — the
// daemon listens on nothing, so there is no pprof endpoint. Read on a computer:
// go tool pprof -top <binary> cpu.pprof. A second signal while one runs is
// ignored.
const profileFor = 30 * time.Second

var profiling atomic.Bool

func (d *daemon) profile() {
	if !profiling.CompareAndSwap(false, true) {
		log.Print("profile: one is already running")
		return
	}
	go func() {
		defer profiling.Store(false)
		dir := filepath.Dir(d.c.StatusFile)
		if err := writeProfiles(dir, profileFor); err != nil {
			log.Printf("profile: %v", err)
			return
		}
		log.Printf("profile: %s written to %s", profileFor, dir)
	}()
}

func writeProfiles(dir string, span time.Duration) error {
	// The CPU profile streams into a 0600 temporary file renamed into place;
	// the other two are written whole by writeAtomic, as the status is.
	cpu, err := os.CreateTemp(dir, ".cpu.pprof.*")
	if err != nil {
		return err
	}
	defer os.Remove(cpu.Name())
	defer cpu.Close()
	if err := pprof.StartCPUProfile(cpu); err != nil {
		return fmt.Errorf("cpu: %w", err)
	}
	runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(int(time.Millisecond))
	defer runtime.SetMutexProfileFraction(0)
	defer runtime.SetBlockProfileRate(0)
	log.Printf("profile: %s started", span)
	time.Sleep(span)
	pprof.StopCPUProfile()
	if err := cpu.Close(); err != nil {
		return fmt.Errorf("cpu: %w", err)
	}
	if err := os.Rename(cpu.Name(), filepath.Join(dir, "cpu.pprof")); err != nil {
		return err
	}

	var buf bytes.Buffer
	for _, name := range []string{"mutex", "block"} {
		buf.Reset()
		if err := pprof.Lookup(name).WriteTo(&buf, 0); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := writeAtomic(filepath.Join(dir, name+".pprof"), buf.Bytes()); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}
