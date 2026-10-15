package proxy

// Several call links (links.go): slot i mints from link i mod N; a call VK
// says is gone is skipped for linkDeadFor and the same mint goes to the next
// live link at once; only all links dead reaches the caller; other errors are
// not a link's death; a success revives; the credential knows its link, the
// cache keeps it, the status counts by it and never names a link. Sabotage
// seen red: the home link ignored (always link 0); a dead link not skipped;
// any error killing a link; the retry missing (the dead link's error
// returned while another is live); Link not stamped / not saved; a link id
// left in the status's error.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeLinks struct {
	dead  map[string]bool
	other map[string]error
	calls []string
}

func (f *fakeLinks) get(linkID string, first bool) (*TURNCreds, error) {
	f.calls = append(f.calls, linkID)
	if f.dead[linkID] {
		return nil, &CallUnavailableError{Code: 9000, Message: "Call not found"}
	}
	if err := f.other[linkID]; err != nil {
		return nil, err
	}
	return &TURNCreds{Username: "u-" + linkID}, nil
}

func TestASlotMintsFromItsHomeLink(t *testing.T) {
	ls := newLinkSet([]string{"aaa", "bbb"})
	f := &fakeLinks{}
	for slot, want := range []int{0, 1, 0, 1} {
		c, err := ls.mint(slot, f.get)
		if err != nil || c.Link != want {
			t.Fatalf("slot %d: link %v, err %v; want link %d", slot, c, err, want)
		}
	}
	if one := newLinkSet([]string{"aaa"}); one.forSlot(7) != 0 {
		t.Fatal("one link: slot 7 is not on it")
	}
}

func TestADeadCallMovesTheMintOnAndStaysSkipped(t *testing.T) {
	ls := newLinkSet([]string{"aaa", "bbb"})
	now := time.Unix(1_800_000_000, 0)
	ls.now = func() time.Time { return now }
	f := &fakeLinks{dead: map[string]bool{"aaa": true}}

	c, err := ls.mint(0, f.get)
	if err != nil || c.Link != 1 || strings.Join(f.calls, ",") != "aaa,bbb" {
		t.Fatalf("slot 0 with link 1 dead: creds %+v err %v calls %v", c, err, f.calls)
	}
	f.calls = nil
	if c, err := ls.mint(2, f.get); err != nil || c.Link != 1 || len(f.calls) != 1 {
		t.Fatalf("a dead link is asked again within %s: calls %v (creds %+v, err %v)", linkDeadFor, f.calls, c, err)
	}
	if st := (&Proxy{links: ls}).linkStats(); st[0].DeadForSec <= 0 || st[1].DeadForSec != 0 || st[0].Failures != 1 || st[1].Mints != 2 {
		t.Fatalf("stats %+v", st)
	}

	// After linkDeadFor the home link is asked again; a success revives it.
	now = now.Add(linkDeadFor)
	f.dead = nil
	f.calls = nil
	if c, err := ls.mint(0, f.get); err != nil || c.Link != 0 || f.calls[0] != "aaa" {
		t.Fatalf("after %s: creds %+v err %v calls %v", linkDeadFor, c, err, f.calls)
	}
	if ls.alive() != 2 {
		t.Fatal("a success did not revive the link")
	}
}

func TestOnlyEveryLinkDeadReachesTheCaller(t *testing.T) {
	ls := newLinkSet([]string{"aaa", "bbb"})
	f := &fakeLinks{dead: map[string]bool{"aaa": true, "bbb": true}}
	_, err := ls.mint(0, f.get)
	var cu *CallUnavailableError
	if !errors.As(err, &cu) {
		t.Fatalf("all dead: %v, want CallUnavailableError", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("calls %v, want both links tried once", f.calls)
	}
}

func TestOtherErrorsAreNotALinksDeath(t *testing.T) {
	ls := newLinkSet([]string{"aaa", "bbb"})
	f := &fakeLinks{other: map[string]error{"aaa": &CaptchaRequiredError{ImageURL: "https://example.invalid/c?session_token=s3cret"}}}
	if _, err := ls.mint(0, f.get); err == nil || len(f.calls) != 1 {
		t.Fatalf("a captcha on link 1: err %v calls %v — want it returned, no other link asked", err, f.calls)
	}
	if ls.alive() != 2 {
		t.Fatal("a captcha killed the link")
	}
}

func TestTheStatusNamesNoLink(t *testing.T) {
	ls := newLinkSet([]string{"Zx9QpLmN0aBcDeFgHiJkLmNoPq", "bbb"})
	ls.note(0, fmt.Errorf("vk: join Zx9QpLmN0aBcDeFgHiJkLmNoPq failed: \"access_token\":\"tok123\""))
	got := ls.lastErr[0]
	if strings.Contains(got, "Zx9QpLmN0aBcDeFgHiJkLmNoPq") || strings.Contains(got, "tok123") || !strings.Contains(got, "<link 1>") {
		t.Fatalf("status error %q", got)
	}
}

func TestTheCacheKeepsEachCredentialsLink(t *testing.T) {
	SetVKCookieAuth(false, "", nil)
	dir, err := os.MkdirTemp("", "credpool-links-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "c.json")
	ctx := context.Background()
	a := NewCredPool(ctx, CredPoolConfig{NumConns: 10, CachePath: path, Fetch: func(bool, int) (string, *TURNCreds, error) {
		c := twoRelayCreds("linked")
		c.Link = 1
		return c.Address, c, nil
	}})
	if _, _, _, err := a.Acquire(0); err != nil {
		t.Fatal(err)
	}
	a.Close()
	var f credCacheFile
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &f); err != nil || len(f.Creds) != 1 || f.Creds[0].Link != 1 {
		t.Fatalf("saved %+v (%v), want link 1", f.Creds, err)
	}
	r := NewCredPool(ctx, CredPoolConfig{NumConns: 10, CachePath: path, Fetch: func(bool, int) (string, *TURNCreds, error) {
		return "", nil, fmt.Errorf("must not mint")
	}})
	defer r.Close()
	r.cp.mu.Lock()
	defer r.cp.mu.Unlock()
	if len(r.cp.pool) == 0 || r.cp.pool[0].creds == nil || r.cp.pool[0].creds.Link != 1 {
		t.Fatal("the loaded credential lost its link")
	}
}
