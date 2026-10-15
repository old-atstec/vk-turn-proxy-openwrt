//go:build !linux && !windows

package proxy

import "errors"

const sockOptsSupported = false

func platformSetSockOpts(uintptr, uint32, string) error {
	return errors.New("socket options are supported on Linux only")
}
