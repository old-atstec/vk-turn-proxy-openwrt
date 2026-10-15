package main

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"
)

// receiveFD connects to the unix socket at path and takes the one descriptor
// the other side sends with its first byte (SCM_RIGHTS) — what Android's
// LocalSocket.setFileDescriptorsForSend does.
func receiveFD(path string) (int, error) {
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return -1, err
	}
	defer c.Close()
	// The app establishes the VPN in between: a dialog the user has not
	// answered yet is a refusal there, not a wait here.
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	buf := make([]byte, 1)
	oob := make([]byte, syscall.CmsgSpace(4*4))
	_, oobn, _, _, err := c.ReadMsgUnix(buf, oob)
	if err != nil {
		return -1, fmt.Errorf("%s: %w", path, err)
	}
	msgs, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return -1, err
	}
	fd := -1
	for _, m := range msgs {
		fds, err := syscall.ParseUnixRights(&m)
		if err != nil {
			continue
		}
		for _, f := range fds {
			if fd < 0 {
				fd = f
			} else {
				syscall.Close(f)
			}
		}
	}
	if fd < 0 {
		return -1, errors.New(path + ": the app sent no descriptor (the VPN was not established?)")
	}
	return fd, nil
}
