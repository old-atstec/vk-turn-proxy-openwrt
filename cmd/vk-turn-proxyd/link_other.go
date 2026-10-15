//go:build !linux

package main

import "errors"

func setLinkUp(string) error {
	return errors.New("the daemon runs on Linux (OpenWrt) only")
}
