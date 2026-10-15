//go:build windows

package main

// The wintun adapter is up once its session starts (tun.CreateTUN); its
// address, routes and DNS are the app's to set (cmd/vkturn), as netifd's on
// the router.
func setLinkUp(string) error { return nil }
