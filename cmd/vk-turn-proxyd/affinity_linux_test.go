//go:build linux

package main

import (
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

// Every thread of the process takes the mask — those that exist and those
// cloned after. Pinned to the one CPU the test already runs on, so nothing
// else on the machine is disturbed. Sabotage seen red: only the calling thread
// set (SchedSetaffinity(0)), a fresh locked thread keeps the old mask.
func TestPinCPUsPinsEveryThread(t *testing.T) {
	var orig unix.CPUSet
	if err := unix.SchedGetaffinity(0, &orig); err != nil {
		t.Fatal(err)
	}
	cpu := -1
	for i := 0; i < 64; i++ {
		if orig.IsSet(i) {
			cpu = i
			break
		}
	}
	defer func() { _, _ = pinCPUs(maskOf(&orig)) }()

	n, err := pinCPUs(1 << cpu)
	if err != nil || n != 1 {
		t.Fatalf("pinCPUs: %d CPUs, %v", n, err)
	}
	// Threads that existed before: some other goroutine's, locked to a thread
	// it was already running on, sees the mask too.
	got := make(chan int)
	for i := 0; i < 4; i++ {
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			var s unix.CPUSet
			if err := unix.SchedGetaffinity(0, &s); err != nil {
				got <- -1
				return
			}
			got <- s.Count()
		}()
	}
	for i := 0; i < 4; i++ {
		if c := <-got; c != 1 {
			t.Fatalf("a thread runs on %d CPUs, want 1", c)
		}
	}
}

func maskOf(s *unix.CPUSet) uint64 {
	var m uint64
	for i := 0; i < 64; i++ {
		if s.IsSet(i) {
			m |= 1 << i
		}
	}
	return m
}
