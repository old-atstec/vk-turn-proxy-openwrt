//go:build windows

// vkturn — VK TURN for Windows: the tunnel of the router and the Android app
// (WireGuard → DTLS-SRTP → VK's TURN relays → anton48/vk-turn-proxy -srtp)
// as a desktop app — run it, press «Включить». windows/README.md.
//
//	vkturn.exe                     the window (installs itself on the first run, UAC once)
//	vkturn.exe vkturn://import?…   the window with the panel's connection to import
//	vkturn.exe <file>.vkturn       the same from a file
//	vkturn.exe tray                the tray icon alone (at logon)
//	vkturn.exe service             the service (the Service Control Manager runs it)
//	vkturn.exe install|uninstall   elevated; the window runs them through UAC
package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// version and commit are set by the build (windows/build.sh).
var (
	version = "dev"
	commit  = ""
)

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	var err error
	switch {
	case cmd == "service":
		err = runService()
	case cmd == "install":
		err = install(args[1:])
	case cmd == "uninstall":
		err = uninstall(args[1:])
	case cmd == "tray":
		err = runTray(false)
	case cmd == "version" || cmd == "--version":
		msgBox(fmt.Sprintf("VK TURN %s %s", version, commit), windows.MB_ICONINFORMATION)
		return
	case cmd == "" || strings.HasPrefix(cmd, "vkturn://") || strings.HasSuffix(strings.ToLower(cmd), ".vkturn"):
		err = launch(cmd)
	default:
		err = fmt.Errorf("неизвестная команда %q", cmd)
	}
	if err != nil {
		if cmd != "service" {
			msgBox(err.Error(), windows.MB_ICONERROR)
		}
		os.Exit(1)
	}
}

func msgBox(text string, flags uint32) {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString("VK TURN")
	_, _ = windows.MessageBox(0, t, c, flags|windows.MB_OK|windows.MB_SETFOREGROUND)
}
