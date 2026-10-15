package main

// A connection issued by the server's panel (vkturn-panel in
// anton48/vk-turn-proxy, type Profile in panel/profile.go): one JSON object —
// the .vkturn file — or, base64url'd, the link its QR code carries:
//
//	vkturn://import?c=<base64url(JSON)>
//
//	{"v":1, "name":"…", "peer":"203.0.113.10:56004", "server_key":"…",
//	 "private_key":"…", "psk":"…", "address":"10.66.66.2/24", "dns":"1.1.1.1",
//	 "mtu":1280, "conns":20, "transport":"udp", "vk_links":["https://vk.com/call/join/…"]}
//
// The third reader of the format after the Android app (ProfileImport.kt)
// and LuCI (parseProfile in vkturn.js); the same rules: version 1 only, the
// required fields, the rest of the settings (mode, routes, lists) kept.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var profileLink = regexp.MustCompile(`vkturn://import\?c=([A-Za-z0-9_-]+)`)

type profile struct {
	V          int      `json:"v"`
	Name       string   `json:"name"`
	Peer       string   `json:"peer"`
	ServerKey  string   `json:"server_key"`
	PrivateKey string   `json:"private_key"`
	PSK        string   `json:"psk"`
	Address    string   `json:"address"`
	DNS        string   `json:"dns"`
	MTU        int      `json:"mtu"`
	Conns      int      `json:"conns"`
	Transport  string   `json:"transport"`
	VKLinks    []string `json:"vk_links"`
}

// importProfile reads a link (anywhere in the text: a message may hold
// more) or the file's JSON onto base; it returns the profile's name too.
func importProfile(text string, base Settings) (string, Settings, error) {
	var raw []byte
	if m := profileLink.FindStringSubmatch(text); m != nil {
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(m[1], "="))
		if err != nil {
			return "", base, errors.New("Ссылка vkturn:// повреждена — скопируйте её целиком")
		}
		raw = b
	} else if t := strings.TrimSpace(text); strings.HasPrefix(t, "{") {
		raw = []byte(t)
	} else {
		return "", base, errors.New("Это не ссылка vkturn:// и не файл .vkturn")
	}
	var p profile
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", base, errors.New("Повреждённые данные подключения")
	}
	if p.V != 1 {
		return "", base, fmt.Errorf("Версия подключения %d не поддерживается — обновите приложение", p.V)
	}
	for _, f := range []struct{ k, v string }{{"peer", p.Peer}, {"server_key", p.ServerKey}, {"private_key", p.PrivateKey}, {"address", p.Address}} {
		if strings.TrimSpace(f.v) == "" {
			return "", base, fmt.Errorf("В подключении нет поля %s", f.k)
		}
	}
	var links []string
	for _, l := range p.VKLinks {
		if l = strings.TrimSpace(l); l != "" {
			links = append(links, l)
		}
	}
	if len(links) == 0 {
		return "", base, errors.New("В подключении нет ссылок на звонки VK")
	}
	s := base
	s.Peer = strings.TrimSpace(p.Peer)
	s.ServerKey = strings.TrimSpace(p.ServerKey)
	s.PrivateKey = strings.TrimSpace(p.PrivateKey)
	s.PSK = strings.TrimSpace(p.PSK)
	s.Address = strings.TrimSpace(p.Address)
	s.Links = links
	if d := strings.TrimSpace(p.DNS); d != "" {
		s.TunnelDNS = d
	}
	if p.MTU > 0 {
		s.MTU = p.MTU
	}
	if p.Conns > 0 {
		s.Conns = p.Conns
	}
	switch p.Transport {
	case "tcp":
		s.TCP = true
	case "udp":
		s.TCP = false
	}
	return strings.TrimSpace(p.Name), s, nil
}
