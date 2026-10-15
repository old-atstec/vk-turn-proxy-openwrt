//go:build linux

package proxy

import (
	"fmt"

	"golang.org/x/sys/unix"
)

const sockOptsSupported = true

func platformSetSockOpts(fd uintptr, mark uint32, device string) error {
	if mark != 0 {
		if err := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, int(mark)); err != nil {
			return fmt.Errorf("SO_MARK 0x%x: %w", mark, err)
		}
	}
	if device != "" {
		if err := unix.BindToDevice(int(fd), device); err != nil {
			return fmt.Errorf("SO_BINDTODEVICE %s: %w", device, err)
		}
	}
	return nil
}
