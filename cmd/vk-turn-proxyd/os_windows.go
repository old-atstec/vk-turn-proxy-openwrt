//go:build windows

package main

// The daemon under the Windows app (cmd/vkturn): run by its service as
// SYSTEM, in %ProgramData%\VK TURN. Windows has no mode bits — os.FileMode
// shows 0666 or 0444 — so what the Unix build reads from the mode is read
// from the ACL here: the config, the status and the credential cache must be
// SYSTEM's and the administrators' alone. C:\ProgramData lets ordinary users
// create subdirectories and files, and a directory made there by default
// inherits that; the app makes its own with a protected ACL (windows/README.md).

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/tun"
)

func init() {
	// One adapter of our own, the same each time: a random GUID would be a new
	// "Network 2, 3, …" for Windows at every start.
	tun.WintunTunnelType = "VK TURN"
	tun.WintunStaticRequestedGUID = &windows.GUID{
		Data1: 0x89faf3f5, Data2: 0x8e21, Data3: 0x412b,
		Data4: [8]byte{0xb1, 0x9b, 0x36, 0x2c, 0x83, 0xaf, 0x2e, 0xef},
	}
}

func setUmask() {}

// privileged: an elevated token (SYSTEM's is): wintun needs it.
func privileged() bool { return windows.GetCurrentProcessToken().IsElevated() }

// No SIGHUP/SIGUSR on Windows: the app's "reconnect" (app.go) stands for them.
var reconnectSignals []os.Signal

func isProfileSignal(os.Signal) bool { return false }

func dirIsOurs(dir string, fi os.FileInfo) error {
	if !fi.IsDir() || fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return fmt.Errorf("%s is not a plain directory", dir)
	}
	return aclOnlyAdmins(dir, writeRights)
}

func configIsPrivate(path string, _ *os.File) error {
	if err := aclOnlyAdmins(path, readRights|writeRights); err != nil {
		return fmt.Errorf("%v — it holds the private key", err)
	}
	return nil
}

const (
	readRights  = windows.FILE_READ_DATA | windows.GENERIC_READ | windows.GENERIC_ALL
	writeRights = windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.WRITE_DAC |
		windows.WRITE_OWNER | windows.DELETE | windows.GENERIC_WRITE | windows.GENERIC_ALL
)

// aclOnlyAdmins refuses a path whose DACL grants any of rights to anyone but
// SYSTEM, the administrators and TrustedInstaller. Inherit-only entries
// (rights for what is created inside) and deny entries are skipped; a missing
// DACL is everyone's.
func aclOnlyAdmins(path string, rights uint32) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("%s has no ACL: anyone may use it", path)
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("%s: ACE %d: %w", path, i, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if uint32(ace.Mask)&rights == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !trustedSID(sid) {
			return fmt.Errorf("%s: the ACL gives %s access (0x%x) — only SYSTEM and the administrators may have it", path, sid, ace.Mask)
		}
	}
	return nil
}

func trustedSID(sid *windows.SID) bool {
	for _, w := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid} {
		if sid.IsWellKnown(w) {
			return true
		}
	}
	// NT SERVICE\TrustedInstaller: it owns parts of the system drive.
	return sid.String() == "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
}
