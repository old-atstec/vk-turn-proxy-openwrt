//go:build windows

package main

// The window, from the user's side (never elevated): make sure the service
// is installed and running — asking UAC once if not — then show the page in
// an app window of Edge (no address bar, its own profile in
// %LOCALAPPDATA%\VK TURN\window: no history or extensions of the user's
// browser), and stay in the tray.

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func launch(arg string) error {
	imp := ""
	switch {
	case strings.HasPrefix(arg, "vkturn://"):
		imp = arg
	case arg != "":
		b, err := os.ReadFile(arg)
		if err != nil {
			return err
		}
		if len(b) > 64<<10 {
			return errors.New("это не файл подключения .vkturn")
		}
		imp = string(b)
	}
	if err := ensureService(); err != nil {
		return err
	}
	// Started from the archive: the installed copy takes over (the tray runs
	// from there, and the archive may go).
	if dst, err := installDir(); err == nil {
		if self, _ := os.Executable(); !strings.EqualFold(filepath.Dir(self), dst) {
			cmd := exec.Command(filepath.Join(dst, "vkturn.exe"))
			if arg != "" {
				cmd.Args = append(cmd.Args, arg)
			}
			if err := cmd.Start(); err == nil {
				return nil
			}
		}
	}
	tok, err := readToken()
	if err != nil {
		return err
	}
	if err := openWindow(tok, imp); err != nil {
		return err
	}
	return runTray(true)
}

// ensureService: installed from Program Files, the same build as this exe,
// and running — or one UAC prompt to make it so.
func ensureService() error {
	dst, err := installDir()
	if err != nil {
		return err
	}
	installed := filepath.Join(dst, "vkturn.exe")
	self, _ := os.Executable()
	needInstall := !serviceExists()
	if !needInstall && !strings.EqualFold(filepath.Dir(self), dst) && !sameFile(self, installed) {
		needInstall = true // a newer (or other) build started from its archive: the update
	}
	if needInstall {
		sid, err := currentUserSID()
		if err != nil {
			return err
		}
		if err := elevate("install --user " + sid); err != nil {
			return err
		}
		// The service may be the old one still: wait for this build in its place.
		for t0 := time.Now(); time.Since(t0) < 90*time.Second; time.Sleep(500 * time.Millisecond) {
			if sameFile(self, installed) && serviceRunning() {
				return nil
			}
		}
		return errors.New("VK TURN не установился (отменили запрос администратора?)")
	}
	if serviceRunning() {
		return nil
	}
	if err := startService(); err != nil {
		// Users may not start services: once more through UAC.
		sid, _ := currentUserSID()
		if err := elevate("install --user " + sid); err != nil {
			return err
		}
	}
	return waitRunning(30 * time.Second)
}

func openService(access uint32) (windows.Handle, func(), error) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0, nil, err
	}
	name, _ := windows.UTF16PtrFromString(serviceName)
	h, err := windows.OpenService(scm, name, access)
	if err != nil {
		windows.CloseServiceHandle(scm)
		return 0, nil, err
	}
	return h, func() { windows.CloseServiceHandle(h); windows.CloseServiceHandle(scm) }, nil
}

func serviceExists() bool {
	_, done, err := openService(windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false
	}
	done()
	return true
}

func serviceRunning() bool {
	h, done, err := openService(windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false
	}
	defer done()
	var st windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(h, &st); err != nil {
		return false
	}
	return st.CurrentState == windows.SERVICE_RUNNING
}

func startService() error {
	h, done, err := openService(windows.SERVICE_START)
	if err != nil {
		return err
	}
	defer done()
	return windows.StartService(h, 0, nil)
}

func waitRunning(limit time.Duration) error {
	for t0 := time.Now(); time.Since(t0) < limit; time.Sleep(500 * time.Millisecond) {
		if serviceRunning() {
			return nil
		}
	}
	return errors.New("сервис VK TURN не запустился — журнал: Просмотр событий → Журналы Windows → Система")
}

// elevate runs this exe with args through UAC and waits for it, as far as
// ShellExecute lets: until the service shows up in waitRunning.
func elevate(args string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(self)
	a, _ := windows.UTF16PtrFromString(args)
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(self))
	if err := windows.ShellExecute(0, verb, file, a, dir, windows.SW_HIDE); err != nil {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return errors.New("Без прав администратора VK TURN не установится: туннелю нужен сетевой адаптер и маршруты")
		}
		return err
	}
	return nil
}

func readToken() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	for i := 0; i < 2; i++ {
		b, err := os.ReadFile(tokenPath(dir))
		if err == nil {
			return strings.TrimSpace(string(b)), nil
		}
		if i == 0 && errors.Is(err, os.ErrPermission) {
			// Another user of this computer: the installer adds them.
			sid, _ := currentUserSID()
			if err := elevate("install --user " + sid); err != nil {
				return "", err
			}
			time.Sleep(3 * time.Second)
			continue
		}
		return "", err
	}
	return "", errors.New("нет доступа к VK TURN для этого пользователя")
}

func uiURL(tok, imp string) string {
	u := fmt.Sprintf("http://127.0.0.1:%d/login?t=%s", uiPort, url.QueryEscape(tok))
	if imp != "" {
		u += "&import=" + url.QueryEscape(imp)
	}
	return u
}

func openWindow(tok, imp string) error {
	// The service may have just started: its port comes a moment later.
	u := uiURL(tok, imp)
	if edge := edgePath(); edge != "" {
		local, _ := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
		profile := filepath.Join(local, displayName, "window")
		cmd := exec.Command(edge, "--app="+u, "--user-data-dir="+profile, "--window-size=880,760",
			"--no-first-run", "--no-default-browser-check", "--disable-sync")
		if err := cmd.Start(); err == nil {
			go cmd.Wait()
			return nil
		}
	}
	verb, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(u)
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

// edgePath: msedge.exe from App Paths, else its usual places.
func edgePath() string {
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		k, err := registry.OpenKey(root, `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\msedge.exe`, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		p, _, err := k.GetStringValue("")
		k.Close()
		if err == nil {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles"} {
		p := filepath.Join(os.Getenv(env), `Microsoft\Edge\Application\msedge.exe`)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// autostartTray puts the tray into the user's logon (HKCU Run) or takes it
// out, with "connect when Windows starts".
func autostartTray(on bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	if !on {
		_ = k.DeleteValue(displayName)
		return
	}
	dst, err := installDir()
	if err != nil {
		return
	}
	want := fmt.Sprintf(`"%s" tray`, filepath.Join(dst, "vkturn.exe"))
	if cur, _, err := k.GetStringValue(displayName); err == nil && cur == want {
		return
	}
	_ = k.SetStringValue(displayName, want)
}
