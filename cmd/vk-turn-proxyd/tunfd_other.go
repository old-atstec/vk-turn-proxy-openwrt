//go:build !linux

package main

import (
	"errors"

	"golang.zx2c4.com/wireguard/tun"
)

func tunFromApp(string) (tun.Device, error) {
	return nil, errors.New("tun_fd_socket: Linux (Android) only")
}
