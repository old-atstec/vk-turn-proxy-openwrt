//go:build linux

package main

import (
	"errors"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// pinCPUs puts every thread of the process on the CPUs of mask and returns
// how many there are. sched_setaffinity is per thread: the threads that exist
// are set one by one (/proc/self/task), and every later one inherits the mask
// of the thread that clones it — so this runs before the proxy starts.
//
// Why at all: the router's RK3399 is 4×A53 + 2×A72. Spread over all six, the
// daemon spent ~2.2 CPUs per 100 Mbit/s; on the two A72 (0x30) ~1.3 at the
// same throughput (openwrt/TODO.md, 2026-10-15).
func pinCPUs(mask uint64) (int, error) {
	var set unix.CPUSet
	for i := 0; i < 64; i++ {
		if mask&(1<<i) != 0 {
			set.Set(i)
		}
	}
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return 0, err
	}
	for _, t := range tasks {
		tid, err := strconv.Atoi(t.Name())
		if err != nil {
			continue
		}
		// A thread that exited meanwhile is not an error.
		if err := unix.SchedSetaffinity(tid, &set); err != nil && !errors.Is(err, unix.ESRCH) {
			return 0, err
		}
	}
	var got unix.CPUSet
	if err := unix.SchedGetaffinity(0, &got); err != nil {
		return 0, err
	}
	return got.Count(), nil
}
