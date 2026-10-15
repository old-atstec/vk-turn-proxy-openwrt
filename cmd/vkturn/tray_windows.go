//go:build windows

package main

// The tray icon: green when connected, amber while connecting, grey when
// off. A click opens the window; the menu turns the tunnel on and off. It is
// the user's process and talks to the service as the window does — over
// 127.0.0.1 with the token's cookie. One per user (a named mutex); started
// at logon when "connect when Windows starts" is on (HKCU Run).
//
// Plain Win32 through x/sys (no cgo): a hidden window for the icon's
// messages, Shell_NotifyIconW, a popup menu.

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"runtime"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procRegisterClassExW         = user32.NewProc("RegisterClassExW")
	procCreateWindowExW          = user32.NewProc("CreateWindowExW")
	procDefWindowProcW           = user32.NewProc("DefWindowProcW")
	procGetMessageW              = user32.NewProc("GetMessageW")
	procTranslateMessage         = user32.NewProc("TranslateMessage")
	procDispatchMessageW         = user32.NewProc("DispatchMessageW")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procPostQuitMessage          = user32.NewProc("PostQuitMessage")
	procCreatePopupMenu          = user32.NewProc("CreatePopupMenu")
	procAppendMenuW              = user32.NewProc("AppendMenuW")
	procTrackPopupMenu           = user32.NewProc("TrackPopupMenu")
	procDestroyMenu              = user32.NewProc("DestroyMenu")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procGetCursorPos             = user32.NewProc("GetCursorPos")
	procRegisterWindowMessageW   = user32.NewProc("RegisterWindowMessageW")
	procCreateIconFromResourceEx = user32.NewProc("CreateIconFromResourceEx")
	shell32                      = windows.NewLazySystemDLL("shell32.dll")
	procShellNotifyIconW         = shell32.NewProc("Shell_NotifyIconW")
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	procGetModuleHandleW         = kernel32.NewProc("GetModuleHandleW")
)

const (
	wmApp         = 0x8000
	wmTrayIcon    = wmApp + 1
	wmTrayUpdate  = wmApp + 2
	wmCommand     = 0x0111
	wmDestroy     = 0x0002
	wmLButtonUp   = 0x0202
	wmRButtonUp   = 0x0205
	wmContextMenu = 0x007B
	nimAdd        = 0
	nimModify     = 1
	nimDelete     = 2
	nifMessage    = 0x1
	nifIcon       = 0x2
	nifTip        = 0x4
	mfString      = 0x0
	mfSeparator   = 0x800
	mfGrayed      = 0x1
	tpmReturnCmd  = 0x100
	tpmRightBtn   = 0x2

	cmdOpen   = 1
	cmdToggle = 2
	cmdQuit   = 3
)

type notifyIconData struct {
	CbSize           uint32
	HWnd             windows.HWND
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            windows.Handle
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     windows.Handle
}

// NOTIFYICONDATAW on 64-bit Windows.
var _ = [1]int{}[unsafe.Sizeof(notifyIconData{})-976]

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       windows.Handle
}

type msg struct {
	HWnd    windows.HWND
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
	_       uint32
}

type tray struct {
	hwnd      windows.HWND
	icons     map[string]windows.Handle
	phase     atomic.Value // string
	tip       atomic.Value // string
	taskbar   uint32       // "TaskbarCreated": Explorer restarted, the icon is gone
	cookie    string
	token     string
	client    *http.Client
	autostart atomic.Int32 // -1 unknown, 0 off, 1 on
}

var theTray *tray

// runTray runs the icon until "Выход"; fromWindow: the window was just
// opened, an icon already running is enough.
func runTray(fromWindow bool) error {
	name, _ := windows.UTF16PtrFromString(`Local\VKTurnTray`)
	h, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(h)
		return nil
	}
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	tok, err := readToken()
	if err != nil {
		if fromWindow {
			return err
		}
		return nil // at logon, before an install: no icon, no box
	}
	runtime.LockOSThread()
	t := &tray{token: tok, cookie: cookieValue(tok), client: &http.Client{Timeout: 5 * time.Second}}
	t.phase.Store("")
	t.tip.Store("VK TURN")
	t.autostart.Store(-1)
	theTray = t
	if err := t.create(); err != nil {
		return err
	}
	go t.poll()
	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	t.notify(nimDelete)
	return nil
}

func (t *tray) create() error {
	inst, _, _ := procGetModuleHandleW.Call(0)
	cls, _ := windows.UTF16PtrFromString("VKTurnTrayWindow")
	wc := wndClassEx{LpfnWndProc: windows.NewCallback(wndProc), HInstance: windows.Handle(inst), LpszClassName: cls}
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return err
	}
	title, _ := windows.UTF16PtrFromString("VK TURN")
	hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
		0, 0, 0, 0, 0, 0, 0, inst, 0)
	if hwnd == 0 {
		return err
	}
	t.hwnd = windows.HWND(hwnd)
	tb, _ := windows.UTF16PtrFromString("TaskbarCreated")
	r, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(tb)))
	t.taskbar = uint32(r)
	t.icons = map[string]windows.Handle{}
	for phase, c := range map[string]color.RGBA{
		"":         {0x8a, 0x8f, 0x98, 0xff},
		"starting": {0xe0, 0x9a, 0x1f, 0xff},
		"running":  {0x22, 0xa8, 0x5a, 0xff},
	} {
		t.icons[phase] = makeIcon(c)
	}
	t.notify(nimAdd)
	return nil
}

func (t *tray) notify(op uintptr) {
	nid := notifyIconData{HWnd: t.hwnd, UID: 1, UFlags: nifMessage | nifIcon | nifTip, UCallbackMessage: wmTrayIcon}
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	ph, _ := t.phase.Load().(string)
	if ph == "stopping" || ph == "off" {
		ph = ""
	}
	nid.HIcon = t.icons[ph]
	tip, _ := t.tip.Load().(string)
	u, _ := windows.UTF16FromString(tip)
	copy(nid.SzTip[:len(nid.SzTip)-1], u)
	procShellNotifyIconW.Call(op, uintptr(unsafe.Pointer(&nid)))
}

func wndProc(hwnd windows.HWND, m uint32, wp, lp uintptr) uintptr {
	t := theTray
	switch {
	case t != nil && m == t.taskbar && m != 0:
		t.notify(nimAdd)
		return 0
	case m == wmTrayIcon:
		switch lp & 0xffff {
		case wmLButtonUp:
			go t.open()
		case wmRButtonUp, wmContextMenu:
			t.menu()
		}
		return 0
	case m == wmTrayUpdate:
		t.notify(nimModify)
		return 0
	case m == wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(m), wp, lp)
	return r
}

func (t *tray) menu() {
	h, _, _ := procCreatePopupMenu.Call()
	defer procDestroyMenu.Call(h)
	add := func(flags, id uintptr, text string) {
		p, _ := windows.UTF16PtrFromString(text)
		procAppendMenuW.Call(h, flags, id, uintptr(unsafe.Pointer(p)))
	}
	add(mfString, cmdOpen, "Открыть VK TURN")
	ph, _ := t.phase.Load().(string)
	switch ph {
	case "off":
		add(mfString, cmdToggle, "Включить")
	case "starting", "running":
		add(mfString, cmdToggle, "Выключить")
	default:
		add(mfString|mfGrayed, cmdToggle, "Сервис недоступен")
	}
	add(mfSeparator, 0, "")
	add(mfString, cmdQuit, "Выключить и закрыть значок")
	var pt struct{ X, Y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(uintptr(t.hwnd))
	cmd, _, _ := procTrackPopupMenu.Call(h, tpmReturnCmd|tpmRightBtn, uintptr(pt.X), uintptr(pt.Y), 0, uintptr(t.hwnd), 0)
	procPostMessageW.Call(uintptr(t.hwnd), 0, 0, 0) // WM_NULL: the menu closes when clicked past
	switch cmd {
	case cmdOpen:
		go t.open()
	case cmdToggle:
		if ph == "off" {
			go t.post("/api/connect")
		} else {
			go t.post("/api/disconnect")
		}
	case cmdQuit:
		go func() {
			t.post("/api/disconnect")
			procPostMessageW.Call(uintptr(t.hwnd), wmDestroy, 0, 0)
		}()
	}
}

func (t *tray) open() { _ = openWindow(t.token, "") }

func (t *tray) req(method, path string) (*http.Response, error) {
	r, err := http.NewRequest(method, "http://127.0.0.1:"+itoa(uiPort)+path, nil)
	if err != nil {
		return nil, err
	}
	r.AddCookie(&http.Cookie{Name: "vkturn", Value: t.cookie})
	if method != http.MethodGet {
		r.Header.Set("X-VKTurn", "1")
	}
	return t.client.Do(r)
}

func (t *tray) post(path string) {
	if resp, err := t.req(http.MethodPost, path); err == nil {
		resp.Body.Close()
	}
	t.refresh()
}

// poll keeps the icon on the service's phase.
func (t *tray) poll() {
	for {
		t.refresh()
		time.Sleep(2 * time.Second)
	}
}

var trayText = map[string]string{"off": "выключен", "starting": "подключение…", "running": "подключён", "stopping": "отключение…"}

func (t *tray) refresh() {
	phase, tip := "", "VK TURN: сервис недоступен"
	if resp, err := t.req(http.MethodGet, "/api/state"); err == nil {
		var v view
		if resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&v) == nil {
			phase = v.Phase
			tip = "VK TURN: " + trayText[v.Phase]
			if v.Error != "" && v.Phase == PhaseOff {
				tip = "VK TURN: " + v.Error
			}
		}
		resp.Body.Close()
		t.syncAutostart()
	}
	if len([]rune(tip)) > 120 {
		tip = string([]rune(tip)[:120]) + "…"
	}
	if old, _ := t.phase.Load().(string); old != phase || t.tip.Load() != tip {
		t.phase.Store(phase)
		t.tip.Store(tip)
		procPostMessageW.Call(uintptr(t.hwnd), wmTrayUpdate, 0, 0)
	}
}

// syncAutostart: the icon at logon goes with "connect when Windows starts".
func (t *tray) syncAutostart() {
	resp, err := t.req(http.MethodGet, "/api/settings")
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var s struct {
		AutoConnect bool `json:"auto_connect"`
	}
	if json.NewDecoder(resp.Body).Decode(&s) != nil {
		return
	}
	want := int32(0)
	if s.AutoConnect {
		want = 1
	}
	if t.autostart.Swap(want) != want {
		autostartTray(s.AutoConnect)
	}
}

// makeIcon: a filled disc with a white ring — drawn here, as PNG, which
// CreateIconFromResourceEx takes (no resource file in the exe).
func makeIcon(c color.RGBA) windows.Handle {
	const n = 32
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			dx, dy := float64(x)-15.5, float64(y)-15.5
			d := dx*dx + dy*dy
			switch {
			case d <= 15.5*15.5 && (d >= 9*9 || d <= 5*5):
				img.SetRGBA(x, y, c)
			case d <= 9*9:
				img.SetRGBA(x, y, color.RGBA{0xff, 0xff, 0xff, 0xff})
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	b := buf.Bytes()
	h, _, _ := procCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 1, 0x00030000, 0, 0, 0)
	return windows.Handle(h)
}

func itoa(n int) string {
	var b [12]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			return string(b[i:])
		}
	}
}
