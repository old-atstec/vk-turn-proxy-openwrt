//go:build windows

package main

// The install: elevated, run by the window through UAC on the first start
// (and again when a newer vkturn.exe is started — the update).
//
//   - the three files into %ProgramFiles%\VK TURN: a service that runs as
//     SYSTEM must run from where only administrators write — not from the
//     Downloads folder the archive was unpacked in;
//   - %ProgramData%\VK TURN with a protected ACL (SYSTEM and the
//     administrators): the settings with the private key, the daemon's
//     config, status and credential cache;
//   - the window's token there, readable by the users who installed;
//   - the service (automatic, restarted if it fails), a Start menu
//     shortcut, vkturn:// and .vkturn opening the window.

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

var shipped = []string{"vkturn.exe", "vk-turn-proxyd.exe", "wintun.dll"}

func installDir() (string, error) {
	pf, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(pf, displayName), nil
}

// The data directory: SYSTEM and the administrators, inherited by all within.
const dataSDDL = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

func protectDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return setSDDL(dir, dataSDDL)
}

func setSDDL(path, sddl string) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func currentUserSID() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}

func install(args []string) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("установка — от администратора")
	}
	var users []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--user" && i+1 < len(args) {
			if _, err := windows.StringToSid(args[i+1]); err != nil {
				return fmt.Errorf("--user %q: %v", args[i+1], err)
			}
			users = append(users, args[i+1])
			i++
		}
	}
	dst, err := installDir()
	if err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	// The files. A running service holds them: stopped first.
	src := exeDir()
	if !strings.EqualFold(filepath.Clean(src), filepath.Clean(dst)) {
		stopService(m)
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		for _, f := range shipped {
			if err := copyFile(filepath.Join(src, f), filepath.Join(dst, f)); err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
		}
	}
	exe := filepath.Join(dst, "vkturn.exe")

	// The data and the token.
	data, err := dataDir()
	if err != nil {
		return err
	}
	if err := protectDir(data); err != nil {
		return err
	}
	if err := writeToken(data, users); err != nil {
		return err
	}

	// The service.
	cfg := mgr.Config{
		DisplayName:  displayName,
		Description:  "Туннель VK TURN: WireGuard через TURN-релеи звонков VK. Окно — vkturn.exe.",
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
	}
	s, err := m.OpenService(serviceName)
	if err == nil {
		old, err := s.Config()
		if err == nil {
			old.BinaryPathName = fmt.Sprintf(`"%s" service`, exe)
			old.DisplayName, old.Description, old.StartType = cfg.DisplayName, cfg.Description, cfg.StartType
			err = s.UpdateConfig(old)
		}
		if err != nil {
			s.Close()
			return err
		}
	} else {
		s, err = m.CreateService(serviceName, exe, cfg, "service")
		if err != nil {
			return err
		}
	}
	defer s.Close()
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 3 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, 24*3600)
	if st, err := s.Query(); err != nil || st.State != svc.Running {
		if err := s.Start(); err != nil {
			return fmt.Errorf("запуск сервиса: %w", err)
		}
	}

	if err := registerHandlers(exe); err != nil {
		return fmt.Errorf("vkturn://: %w", err)
	}
	if err := shortcut(exe); err != nil {
		return fmt.Errorf("ярлык: %w", err)
	}
	return nil
}

func stopService(m *mgr.Mgr) {
	s, err := m.OpenService(serviceName)
	if err != nil {
		return
	}
	defer s.Close()
	if _, err := s.Control(svc.Stop); err != nil {
		return
	}
	for i := 0; i < 60; i++ {
		if st, err := s.Query(); err != nil || st.State == svc.Stopped {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := to + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, to)
}

// writeToken makes the token once and lets users (and those of earlier
// installs) read it.
func writeToken(dir string, users []string) error {
	known, _ := os.ReadFile(usersPath(dir))
	all := map[string]bool{}
	for _, u := range strings.Fields(string(known)) {
		all[u] = true
	}
	for _, u := range users {
		all[u] = true
	}
	var list []string
	for u := range all {
		list = append(list, u)
	}
	if err := os.WriteFile(usersPath(dir), []byte(strings.Join(list, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	p := tokenPath(dir)
	if b, err := os.ReadFile(p); err != nil || len(bytes.TrimSpace(b)) < 32 {
		if err := os.WriteFile(p, []byte(randomToken()+"\n"), 0o600); err != nil {
			return err
		}
	}
	sddl := "D:P(A;;FA;;;SY)(A;;FA;;;BA)"
	for _, u := range list {
		sddl += "(A;;FR;;;" + u + ")"
	}
	return setSDDL(p, sddl)
}

// registerHandlers: vkturn:// links and .vkturn files open the window.
func registerHandlers(exe string) error {
	open := fmt.Sprintf(`"%s" "%%1"`, exe)
	set := func(path string, vals map[string]string) error {
		k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, `SOFTWARE\Classes\`+path, registry.SET_VALUE)
		if err != nil {
			return err
		}
		defer k.Close()
		for n, v := range vals {
			if err := k.SetStringValue(n, v); err != nil {
				return err
			}
		}
		return nil
	}
	for _, e := range []error{
		set(`vkturn`, map[string]string{"": "URL:VK TURN", "URL Protocol": ""}),
		set(`vkturn\shell\open\command`, map[string]string{"": open}),
		set(`.vkturn`, map[string]string{"": "VKTurn.Profile"}),
		set(`VKTurn.Profile`, map[string]string{"": "Подключение VK TURN"}),
		set(`VKTurn.Profile\shell\open\command`, map[string]string{"": open}),
	} {
		if e != nil {
			return e
		}
	}
	return nil
}

func startMenu() (string, error) {
	p, err := windows.KnownFolderPath(windows.FOLDERID_CommonPrograms, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(p, displayName+".lnk"), nil
}

// shortcut: the Start menu entry, made by the shell's own COM object through
// PowerShell (no COM from Go without cgo); the paths go in by the
// environment, never into the script's text.
func shortcut(exe string) error {
	lnk, err := startMenu()
	if err != nil {
		return err
	}
	ps := `$s = (New-Object -ComObject WScript.Shell).CreateShortcut($env:VKTURN_LNK); $s.TargetPath = $env:VKTURN_EXE; $s.WorkingDirectory = Split-Path $env:VKTURN_EXE; $s.Description = 'VK TURN'; $s.Save()`
	cmd := exec.Command(powershell(), "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", ps)
	cmd.Env = append(os.Environ(), "VKTURN_LNK="+lnk, "VKTURN_EXE="+exe)
	(&winPlatform{}).prepareCmd(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func powershell() string {
	sys, err := windows.GetSystemDirectory()
	if err != nil {
		return "powershell.exe"
	}
	return filepath.Join(sys, `WindowsPowerShell\v1.0\powershell.exe`)
}

func uninstall(args []string) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return elevate("uninstall")
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	stopService(m)
	if s, err := m.OpenService(serviceName); err == nil {
		_ = s.Delete()
		s.Close()
	}
	(&winPlatform{}).clearDNSPolicy()
	if lnk, err := startMenu(); err == nil {
		os.Remove(lnk)
	}
	for _, k := range []string{`vkturn\shell\open\command`, `vkturn\shell\open`, `vkturn\shell`, `vkturn`,
		`VKTurn.Profile\shell\open\command`, `VKTurn.Profile\shell\open`, `VKTurn.Profile\shell`, `VKTurn.Profile`, `.vkturn`} {
		_ = registry.DeleteKey(registry.LOCAL_MACHINE, `SOFTWARE\Classes\`+k)
	}
	if data, err := dataDir(); err == nil {
		if askYesNo("Удалить и настройки VK TURN (ключи, ссылки, списки)?") {
			os.RemoveAll(data)
		}
	}
	// The program's own folder: it is running from there — removed a moment
	// after it exits.
	if dst, err := installDir(); err == nil && strings.EqualFold(filepath.Clean(exeDir()), filepath.Clean(dst)) {
		sys, _ := windows.GetSystemDirectory()
		cmd := exec.Command(filepath.Join(sys, "cmd.exe"), "/c", "ping -n 3 127.0.0.1 >nul & rmdir /s /q \"%VKTURN_DIR%\"")
		cmd.Env = append(os.Environ(), "VKTURN_DIR="+dst)
		(&winPlatform{}).prepareCmd(cmd)
		_ = cmd.Start()
	}
	msgBox("VK TURN удалён.", windows.MB_ICONINFORMATION)
	return nil
}

func askYesNo(q string) bool {
	t, _ := windows.UTF16PtrFromString(q)
	c, _ := windows.UTF16PtrFromString("VK TURN")
	r, _ := windows.MessageBox(0, t, c, windows.MB_YESNO|windows.MB_ICONQUESTION|windows.MB_SETFOREGROUND)
	return r == 6 // IDYES
}

// sameFile: the installed copy is this one (no update to install).
func sameFile(a, b string) bool {
	ha, err1 := fileHash(a)
	hb, err2 := fileHash(b)
	return err1 == nil && err2 == nil && ha == hb
}

func fileHash(p string) ([32]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [32]byte{}, err
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}
