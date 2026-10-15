package main

// The status file: what LuCI shows, read by the rpcd plugin
// (/usr/share/rpcd/ucode/vkturn) from /var/run/vkturn/<iface>/status.json.
// A file rather than a ubus object of the daemon's own: rpcd already runs
// ucode plugins on the router, and a cgo-free ubus client in Go would be new
// code on the hot path for nothing a file cannot carry. Written whole to a
// temporary file and renamed over, so a reader never sees half of it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/cacggghp/vk-turn-proxy/pkg/proxy"
)

type status struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Iface     string `json:"iface"`
	PID       int    `json:"pid"`
	State     string `json:"state"` // starting | connecting | handshake | up | down | stopping
	LinkUp    bool   `json:"link_up"`
	StateFrom int64  `json:"state_since"` // unix seconds
	Updated   int64  `json:"updated"`
	Peer      string `json:"peer"`
	Transport string `json:"transport"`

	ConnsConfigured int   `json:"conns_configured"`
	ConnsActive     int32 `json:"conns_active"`
	SessionsTotal   int32 `json:"sessions_total"`
	Reconnects      int64 `json:"watchdog_restarts"`

	TxBytes int64 `json:"tx_bytes"` // TURN side, as the proxy counts
	RxBytes int64 `json:"rx_bytes"`

	WGTxBytes       int64 `json:"wg_tx_bytes"`
	WGRxBytes       int64 `json:"wg_rx_bytes"`
	WGLastHandshake int64 `json:"wg_last_handshake"` // unix seconds, 0 = never

	TurnRTTms        float64 `json:"turn_rtt_ms"`
	PoolSize         int32   `json:"pool_size"`
	PoolWithCreds    int32   `json:"pool_with_creds"`
	PoolFresh        int32   `json:"pool_fresh"`
	PoolRelays       int32   `json:"pool_relays"`
	QuotaRefusals    int64   `json:"quota_refusals"`
	MintPausedSec    int32   `json:"mint_paused_sec"`
	CaptchaNeeded    bool    `json:"captcha_needed"`
	CaptchaURL       string  `json:"captcha_url,omitempty"`
	AuthError        string  `json:"auth_error,omitempty"`
	LastError        string  `json:"last_error,omitempty"`
	WAN              string  `json:"wan,omitempty"`
	UptimeSec        int64   `json:"proxy_uptime_sec"`
	DownAfterSeconds int     `json:"down_after"`
}

// fill copies the proxy's counters in.
func (s *status) fill(ps proxy.Stats, wg wgState) {
	s.ConnsActive = ps.ActiveConns
	s.SessionsTotal = ps.TotalConns
	s.Reconnects = ps.Reconnects
	s.TxBytes, s.RxBytes = ps.TxBytes, ps.RxBytes
	s.TurnRTTms = ps.TurnRTTms
	s.PoolSize, s.PoolWithCreds, s.PoolFresh, s.PoolRelays = ps.CredPoolSize, ps.CredPoolWithCreds, ps.CredPoolFilled, ps.CredPoolDistinctRelays
	s.QuotaRefusals = ps.CredPoolQuotaRefusals
	s.MintPausedSec = ps.CredPoolMintPausedSec
	s.CaptchaNeeded = ps.CaptchaImageURL != ""
	s.CaptchaURL = ps.CaptchaImageURL
	s.AuthError = ps.AuthError
	s.UptimeSec = ps.TunnelUptimeSec
	s.WGTxBytes, s.WGRxBytes = wg.txBytes, wg.rxBytes
	s.WGLastHandshake = 0
	if !wg.lastHandshake.IsZero() {
		s.WGLastHandshake = wg.lastHandshake.Unix()
	}
}

// writeStatus writes s to path atomically, 0600.
func writeStatus(path string, s *status) error {
	s.Updated = time.Now().Unix()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(b, '\n'))
}

// writeAtomic writes b to path through a 0600 temporary file in its directory
// and a rename: a reader sees the old file or the new one, never a half.
func writeAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op after the rename
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
