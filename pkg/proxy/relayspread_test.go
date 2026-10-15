package proxy

// Connections spread over the call's relays (relayspread.go): with
// SpreadRelays twenty connections on two credentials of two relays each sit
// ten per relay and five per (credential, relay) bucket; without it, or in
// cookie mode, all on Addresses[0] as in the app. The cache keeps the
// credential's relays across a restart; a file from before the field loads
// with its one address. Sabotage seen red: resolveTURNAddr not spreading;
// spreading in cookie mode; the probe (connIdx -1) spread; Addresses not
// saved; a saved list not starting with the entry's address taken as is.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	spreadRelayA = "203.0.113.11:19302"
	spreadRelayB = "198.51.100.12:19302"
)

func twoRelayCreds(name string) *TURNCreds {
	return &TURNCreds{Username: fmt.Sprintf("%d:%s", time.Now().Add(8*time.Hour).Unix(), name), Password: "pw",
		Address: spreadRelayA, Addresses: []string{spreadRelayA, spreadRelayB}}
}

func TestSpreadRelayPicksByConnection(t *testing.T) {
	c := twoRelayCreds("x")
	for i, want := range []string{spreadRelayA, spreadRelayB, spreadRelayA, spreadRelayB} {
		if got := spreadRelay(spreadRelayA, c, i); got != want {
			t.Errorf("conn %d: %s, want %s", i, got, want)
		}
	}
	if got := spreadRelay(spreadRelayA, c, -1); got != spreadRelayA {
		t.Errorf("the probe (conn -1) dials %s, want the slot's %s", got, spreadRelayA)
	}
	one := &TURNCreds{Address: spreadRelayA, Addresses: []string{spreadRelayA}}
	if got := spreadRelay(spreadRelayA, one, 1); got != spreadRelayA {
		t.Errorf("one relay: %s", got)
	}
	if got := spreadRelay(spreadRelayA, nil, 1); got != spreadRelayA {
		t.Errorf("no creds: %s", got)
	}
}

func TestResolveTURNAddrSpreadsConnectionsOverTheCallsRelays(t *testing.T) {
	SetVKCookieAuth(false, "", nil)
	t.Cleanup(func() { SetVKCookieAuth(false, "", nil) })

	run := func(t *testing.T, spread bool) (perRelay map[string]int, perBucket map[string]int) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cp := newCredPool(ctx, 2, 2*time.Minute, "", func(bool, int) (string, *TURNCreds, error) {
			return "", nil, fmt.Errorf("no mint: both slots are filled")
		})
		cp.mu.Lock()
		for len(cp.pool) < cp.size {
			cp.pool = append(cp.pool, credPoolEntry{})
		}
		for s := 0; s < 2; s++ {
			cp.pool[s] = credPoolEntry{addr: spreadRelayA, creds: twoRelayCreds(fmt.Sprintf("slot-%d", s)), ts: time.Now()}
		}
		cp.mu.Unlock()
		p := &Proxy{credPool: cp, config: Config{SpreadRelays: spread}}

		perRelay, perBucket = map[string]int{}, map[string]int{}
		for i := 0; i < 20; i++ {
			addr, _, slot, err := p.resolveTURNAddr(i, false)
			if err != nil {
				t.Fatalf("conn %d: %v", i, err)
			}
			perRelay[addr]++
			perBucket[fmt.Sprintf("%d %s", slot, addr)]++
		}
		return perRelay, perBucket
	}

	t.Run("spread", func(t *testing.T) {
		perRelay, perBucket := run(t, true)
		if perRelay[spreadRelayA] != 10 || perRelay[spreadRelayB] != 10 {
			t.Fatalf("relays %v, want ten on each", perRelay)
		}
		for b, n := range perBucket {
			if n > connsPerSlot/2 {
				t.Errorf("bucket %s holds %d, want at most %d", b, n, connsPerSlot/2)
			}
		}
	})
	t.Run("off", func(t *testing.T) {
		if perRelay, _ := run(t, false); perRelay[spreadRelayA] != 20 {
			t.Fatalf("relays %v, want all twenty on the first", perRelay)
		}
	})
	t.Run("cookie mode", func(t *testing.T) {
		SetVKCookieAuth(true, "remixsid=x", []string{"https://vk.ru/call/join/aaaaaa"})
		defer SetVKCookieAuth(false, "", nil)
		if perRelay, _ := run(t, true); perRelay[spreadRelayA] != 20 {
			t.Fatalf("relays %v, want all twenty on the slot's own relay", perRelay)
		}
	})
}

func TestTheCacheKeepsTheCallsRelays(t *testing.T) {
	SetVKCookieAuth(false, "", nil)
	// Not t.TempDir: the pool's background saver may write once more after
	// Close (see credcache_mode_test.go).
	dir, err := os.MkdirTemp("", "credpool-relays-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ctx := context.Background()

	saved := filepath.Join(dir, "saved.json")
	a := NewCredPool(ctx, CredPoolConfig{NumConns: 10, CachePath: saved, Fetch: func(bool, int) (string, *TURNCreds, error) {
		return spreadRelayA, twoRelayCreds("minted"), nil
	}})
	if _, _, _, err := a.Acquire(0); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	a.Close()
	f := readCacheFile(t, saved)
	if len(f.Creds) != 1 || len(f.Creds[0].Addresses) != 2 {
		t.Fatalf("saved %+v, want one entry with both relays", f.Creds)
	}

	noMint := func(bool, int) (string, *TURNCreds, error) { return "", nil, fmt.Errorf("must not mint") }
	loaded := func(path string) []string {
		t.Helper()
		p := NewCredPool(ctx, CredPoolConfig{NumConns: 10, CachePath: path, Fetch: noMint})
		defer p.Close()
		p.cp.mu.Lock()
		defer p.cp.mu.Unlock()
		if len(p.cp.pool) == 0 || p.cp.pool[0].creds == nil {
			t.Fatalf("%s: nothing loaded", filepath.Base(path))
		}
		return p.cp.pool[0].creds.Addresses
	}
	reloaded := filepath.Join(dir, "reloaded.json")
	copyFile(t, saved, reloaded)
	if got := loaded(reloaded); len(got) != 2 || got[0] != spreadRelayA || got[1] != spreadRelayB {
		t.Errorf("loaded relays %v, want both", got)
	}

	write := func(name string, e credCacheEntry) string {
		t.Helper()
		e.Username = fmt.Sprintf("%d:%s", time.Now().Add(8*time.Hour).Unix(), name)
		e.Password = "pw"
		b, err := json.Marshal(credCacheFile{Version: credCacheVersion, SavedAt: time.Now().Unix(), Creds: []credCacheEntry{e}})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name+".json")
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if got := loaded(write("old", credCacheEntry{Address: spreadRelayA})); len(got) != 1 || got[0] != spreadRelayA {
		t.Errorf("a file from before the field: relays %v, want the entry's address alone", got)
	}
	if got := loaded(write("odd", credCacheEntry{Address: spreadRelayA, Addresses: []string{spreadRelayB}})); len(got) != 1 || got[0] != spreadRelayA {
		t.Errorf("a list not starting with the entry's address: relays %v, want the address alone", got)
	}
}
