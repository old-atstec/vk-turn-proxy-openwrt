//go:build linux

package main

import (
	"golang.zx2c4.com/wireguard/tun"
)

// tunFromApp takes the tun the app established (app.go) — its size and its
// addresses are the app's, set on VpnService.Builder.
func tunFromApp(path string) (tun.Device, error) {
	fd, err := receiveFD(path)
	if err != nil {
		return nil, err
	}
	dev, _, err := tun.CreateUnmonitoredTUNFromFD(fd)
	return dev, err
}
