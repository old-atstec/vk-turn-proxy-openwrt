//go:build !linux

package main

import "errors"

func pinCPUs(uint64) (int, error) {
	return 0, errors.New("the daemon runs on Linux (OpenWrt) only")
}
