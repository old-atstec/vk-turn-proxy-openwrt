package main

// WireGuard keys without wireguard-tools: `vk-turn-proxyd genkey`,
// `vk-turn-proxyd pubkey` (the private key on stdin), as `wg genkey` /
// `wg pubkey` — LuCI's key generation calls them through the rpcd plugin.

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/curve25519"
)

func genKey() (string, error) {
	var k [32]byte
	if _, err := rand.Read(k[:]); err != nil {
		return "", err
	}
	// Clamped as wg genkey does.
	k[0] &= 248
	k[31] = (k[31] & 127) | 64
	return base64.StdEncoding.EncodeToString(k[:]), nil
}

func pubKey(privB64 string) (string, error) {
	priv, err := base64.StdEncoding.DecodeString(strings.TrimSpace(privB64))
	if err != nil || len(priv) != 32 {
		return "", errors.New("not a base64 WireGuard key")
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

func pubKeyFrom(r io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(r, 1024)).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return pubKey(line)
}

// keyHex turns a base64 WireGuard key into UAPI's hex; the error never
// carries the key.
func keyHex(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return "", errors.New("is not base64")
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("is %d bytes, want 32", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

// uapiConfig: the one peer taking everything, the endpoint the peer address
// (TURNBind ignores it — the proxy carries every packet).
func uapiConfig(c config) (string, error) {
	priv, err := keyHex(c.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("private_key %w", err)
	}
	pub, err := keyHex(c.PublicKey)
	if err != nil {
		return "", fmt.Errorf("public_key %w", err)
	}
	if strings.ContainsAny(c.Peer, "\n\r=") {
		return "", errors.New("peer holds a control character")
	}
	lines := []string{
		"private_key=" + priv,
		"replace_peers=true",
		"public_key=" + pub,
		"endpoint=" + c.Peer,
	}
	if c.Keepalive > 0 {
		lines = append(lines, fmt.Sprintf("persistent_keepalive_interval=%d", c.Keepalive))
	}
	lines = append(lines, "allowed_ip=0.0.0.0/0")
	if c.PresharedKey != "" {
		psk, err := keyHex(c.PresharedKey)
		if err != nil {
			return "", fmt.Errorf("preshared_key %w", err)
		}
		lines = append(lines, "preshared_key="+psk)
	}
	return strings.Join(lines, "\n"), nil
}
