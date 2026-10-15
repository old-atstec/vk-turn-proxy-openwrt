package main

// The window: a page the service serves on 127.0.0.1, shown by the launcher
// in an app window of Edge (launcher_windows.go). Only the service runs
// privileged; the page is the user's, and it gets in with a token.
//
//   - The token is a file in the data directory readable by the users the
//     installer named (and the administrators); the launcher reads it and
//     opens /login?t=<token>, which sets a cookie and sends the page on.
//   - Every /api call needs the cookie, a Host of 127.0.0.1 (a page of
//     another site that rebinds a name to 127.0.0.1 has another Host), and
//     a change needs the X-VKTurn header too — a header no form and no
//     cross-site request without a preflight can send.

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const uiPort = 47447

//go:embed web/index.html
var webFS embed.FS

type ui struct {
	t      *tunnel
	token  string
	daemon string // for genkey / pubkey
	port   int

	keyMu sync.Mutex
	pubOf map[string]string
}

func newUI(t *tunnel, token, daemon string, port int) *ui {
	return &ui{t: t, token: token, daemon: daemon, port: port, pubOf: map[string]string{}}
}

func cookieValue(token string) string {
	sum := sha256.Sum256([]byte("vkturn-ui|" + token))
	return hex.EncodeToString(sum[:])
}

func (u *ui) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", u.index)
	mux.HandleFunc("GET /login", u.login)
	mux.HandleFunc("GET /api/state", u.auth(u.state))
	mux.HandleFunc("GET /api/settings", u.auth(u.getSettings))
	mux.HandleFunc("POST /api/settings", u.auth(u.setSettings))
	mux.HandleFunc("POST /api/connect", u.auth(u.connect))
	mux.HandleFunc("POST /api/disconnect", u.auth(u.disconnect))
	mux.HandleFunc("POST /api/reconnect", u.auth(u.reconnect))
	mux.HandleFunc("POST /api/genkey", u.auth(u.genkey))
	mux.HandleFunc("POST /api/import", u.auth(u.importProfile))
	mux.HandleFunc("POST /api/lists/refresh", u.auth(u.refreshLists))
	mux.HandleFunc("GET /api/log", u.auth(u.logLines))
	return u.hostCheck(mux)
}

func (u *ui) hostCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(r.Host)
		if err != nil || (host != "127.0.0.1" && host != "localhost") || port != strconv.Itoa(u.port) {
			http.Error(w, "wrong host", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (u *ui) authorized(r *http.Request) bool {
	c, err := r.Cookie("vkturn")
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(cookieValue(u.token))) == 1
}

func (u *ui) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !u.authorized(r) {
			http.Error(w, "откройте VK TURN из меню Пуск", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			if r.Header.Get("X-VKTurn") != "1" {
				http.Error(w, "no X-VKTurn", http.StatusForbidden)
				return
			}
			if o := r.Header.Get("Origin"); o != "" && o != fmt.Sprintf("http://127.0.0.1:%d", u.port) && o != fmt.Sprintf("http://localhost:%d", u.port) {
				http.Error(w, "wrong origin", http.StatusForbidden)
				return
			}
		}
		h(w, r)
	}
}

func (u *ui) index(w http.ResponseWriter, r *http.Request) {
	b, _ := webFS.ReadFile("web/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; connect-src 'self'; frame-ancestors 'none'")
	_, _ = w.Write(b)
}

func (u *ui) login(w http.ResponseWriter, r *http.Request) {
	t := r.URL.Query().Get("t")
	if subtle.ConstantTimeCompare([]byte(t), []byte(u.token)) != 1 {
		http.Error(w, "неверный токен — откройте VK TURN из меню Пуск", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: "vkturn", Value: cookieValue(u.token), Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 365 * 24 * 3600,
	})
	dest := "/"
	if imp := r.URL.Query().Get("import"); imp != "" {
		dest = "/#import=" + imp
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func (u *ui) state(w http.ResponseWriter, r *http.Request) { writeJSON(w, u.t.View()) }

// settingsOut: the private key never leaves; the public one is shown.
type settingsOut struct {
	Settings
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	HasKey     bool   `json:"has_private_key"`
}

func (u *ui) getSettings(w http.ResponseWriter, r *http.Request) {
	s := u.t.Settings()
	out := settingsOut{Settings: s, HasKey: s.PrivateKey != ""}
	if s.PrivateKey != "" {
		out.PublicKey = u.pubKey(s.PrivateKey)
	}
	writeJSON(w, out)
}

func (u *ui) setSettings(w http.ResponseWriter, r *http.Request) {
	cur := u.t.Settings()
	s := cur
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&s); err != nil {
		writeErr(w, err)
		return
	}
	if s.PrivateKey == "" {
		s.PrivateKey = cur.PrivateKey // not sent back: kept
	}
	s.Links = cleanList(s.Links)
	s.Lists = cleanList(s.Lists)
	if err := u.t.SaveSettings(s); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func cleanList(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (u *ui) connect(w http.ResponseWriter, r *http.Request) {
	if err := u.t.Start(); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (u *ui) disconnect(w http.ResponseWriter, r *http.Request) {
	go u.t.Stop()
	writeJSON(w, map[string]bool{"ok": true})
}

func (u *ui) reconnect(w http.ResponseWriter, r *http.Request) {
	u.t.Reconnect()
	writeJSON(w, map[string]bool{"ok": true})
}

// genkey: a new pair by the daemon (vk-turn-proxyd genkey), as LuCI makes
// one; the private key is saved at once, the public one returned.
func (u *ui) genkey(w http.ResponseWriter, r *http.Request) {
	out, err := u.run(nil, "genkey")
	if err != nil {
		writeErr(w, err)
		return
	}
	priv := strings.TrimSpace(out)
	s := u.t.Settings()
	s.PrivateKey = priv
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := writePrivate(u.t.dirs.Settings(), b); err != nil {
		writeErr(w, err)
		return
	}
	u.t.mu.Lock()
	u.t.settings = s
	u.t.mu.Unlock()
	writeJSON(w, map[string]string{"public_key": u.pubKey(priv)})
}

func (u *ui) pubKey(priv string) string {
	u.keyMu.Lock()
	defer u.keyMu.Unlock()
	if p, ok := u.pubOf[priv]; ok {
		return p
	}
	out, err := u.run(strings.NewReader(priv+"\n"), "pubkey")
	if err != nil {
		return ""
	}
	p := strings.TrimSpace(out)
	u.pubOf[priv] = p
	return p
}

func (u *ui) run(stdin io.Reader, args ...string) (string, error) {
	cmd := exec.Command(u.daemon, args...)
	u.t.plat.prepareCmd(cmd)
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return "", fmt.Errorf("%s: %v %s", args[0], err, strings.TrimSpace(errb.String()))
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		return "", errors.New(args[0] + ": timeout")
	}
	return out.String(), nil
}

// importProfile: {"text": "vkturn://…" or the file's JSON, "apply": bool}.
// Without apply it shows what would change; with it, saves and connects.
func (u *ui) importProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text  string `json:"text"`
		Apply bool   `json:"apply"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}
	name, s, err := importProfile(req.Text, u.t.Settings())
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.Validate(); err != nil {
		writeErr(w, err)
		return
	}
	if req.Apply {
		if err := u.t.SaveSettings(s); err != nil {
			writeErr(w, err)
			return
		}
		go func() {
			if err := u.t.Start(); err != nil {
				u.t.logf("connect: %v", err)
			}
		}()
	}
	writeJSON(w, map[string]any{"name": name, "peer": s.Peer, "address": s.Address, "links": len(s.Links)})
}

func (u *ui) refreshLists(w http.ResponseWriter, r *http.Request) {
	go u.t.RefreshLists()
	writeJSON(w, map[string]bool{"ok": true})
}

func (u *ui) logLines(w http.ResponseWriter, r *http.Request) {
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if err != nil {
		after = -1
	}
	lines, last := u.t.log.since(after)
	writeJSON(w, map[string]any{"lines": lines, "last": last})
}
