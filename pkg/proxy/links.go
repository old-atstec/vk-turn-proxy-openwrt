package proxy

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// Several call links (Config.VKLinks): a reserve, not a second tunnel.
//
// A credential slot has a home link — slot i mints from link i mod N — so
// every link carries connections all the time and the reserve is warm: when
// VK closes one call, the connections on its credentials move to the other
// links' slots the way they move off any cooling slot, with no cold start.
// A mint that meets CallUnavailableError (the call ended, was deleted, or the
// link is invalid) marks that link dead for linkDeadFor and is retried at
// once from the next live link; while a link is dead its slots mint from a
// live one. Only when every link is dead does the error reach the caller —
// the daemon's "the call link is dead" at the first connection.
//
// Quotas do not care: VK's ~10 allocations are per (identity, relay), and
// every mint is a fresh anonymous identity whatever link it came from.
// Cookie mode is apart (cookieCredForSlot maps slots to relays over all its
// links already).
//
// Links are named by their position (link 1, link 2) in logs and the status:
// a call link is a key to the call.

const linkDeadFor = 10 * time.Minute

type linkSet struct {
	ids []string // link ids (parseVKLinkID), in the configured order

	mu        sync.Mutex
	deadUntil []time.Time
	mints     []int64
	failures  []int64
	lastErr   []string
	now       func() time.Time
}

func newLinkSet(ids []string) *linkSet {
	n := len(ids)
	return &linkSet{
		ids:       ids,
		deadUntil: make([]time.Time, n),
		mints:     make([]int64, n),
		failures:  make([]int64, n),
		lastErr:   make([]string, n),
		now:       time.Now,
	}
}

// forSlot is the link a mint for slot uses: the slot's home link, or, while
// that one is dead, the first live link after it. All dead: the home link
// (its error is the one to report). Slot < 0 (a probe) is slot 0.
func (ls *linkSet) forSlot(slot int) int {
	n := len(ls.ids)
	if n <= 1 {
		return 0
	}
	if slot < 0 {
		slot = 0
	}
	home := slot % n
	ls.mu.Lock()
	defer ls.mu.Unlock()
	now := ls.now()
	for k := 0; k < n; k++ {
		i := (home + k) % n
		if !now.Before(ls.deadUntil[i]) {
			return i
		}
	}
	return home
}

// note records a mint's outcome on link i. A CallUnavailableError kills the
// link for linkDeadFor; a success revives it.
func (ls *linkSet) note(i int, err error) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if err == nil {
		ls.mints[i]++
		ls.deadUntil[i] = time.Time{}
		return
	}
	ls.failures[i]++
	ls.lastErr[i] = ls.redactedErr(err)
	var cu *CallUnavailableError
	if errors.As(err, &cu) {
		ls.deadUntil[i] = ls.now().Add(linkDeadFor)
	}
}

// mint gets a credential for slot from its link (forSlot), and from the next
// live link at once when VK says that call is gone; any other error — a
// captcha, a rate limit, the network — is the caller's, as before. first is
// false on a retry (a captcha's tokens belong to the first request). The
// credential is stamped with its link.
func (ls *linkSet) mint(slot int, get func(linkID string, first bool) (*TURNCreds, error)) (*TURNCreds, error) {
	var err error
	for try := 0; try < len(ls.ids); try++ {
		li := ls.forSlot(slot)
		var c *TURNCreds
		c, err = get(ls.ids[li], try == 0)
		ls.note(li, err)
		if err == nil {
			c.Link = li
			return c, nil
		}
		var cu *CallUnavailableError
		if !errors.As(err, &cu) || ls.alive() == 0 {
			return nil, err
		}
		log.Printf("vk: link %d/%d: the call is gone (VK %d) — its slots mint from another link for %s", li+1, len(ls.ids), cu.Code, linkDeadFor)
	}
	return nil, err
}

// alive is how many links are not dead now.
func (ls *linkSet) alive() int {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	now, n := ls.now(), 0
	for _, d := range ls.deadUntil {
		if !now.Before(d) {
			n++
		}
	}
	return n
}

// redactedErr is a mint error as the status may show it: secrets masked
// (redactBody's patterns), at most 200 bytes, and no link id in it — a link
// is named by its position.
func (ls *linkSet) redactedErr(err error) string {
	msg := redactBody([]byte(err.Error()), 200)
	for i, id := range ls.ids {
		if id != "" {
			msg = strings.ReplaceAll(msg, id, fmt.Sprintf("<link %d>", i+1))
		}
	}
	return msg
}

// LinkStats is one call link's line of Stats.Links, by position.
type LinkStats struct {
	Mints      int64  `json:"mints"`
	Failures   int64  `json:"failures"`
	DeadForSec int32  `json:"dead_for_sec"` // > 0: VK said the call is gone; its slots mint from another link meanwhile
	LastError  string `json:"last_error,omitempty"`
	Creds      int32  `json:"creds"`  // pool slots holding a credential minted from it
	Active     int32  `json:"active"` // connections on those credentials
}

// linkStats snapshots the links and what the pool holds of each.
func (p *Proxy) linkStats() []LinkStats {
	ls := p.links
	if ls == nil || len(ls.ids) == 0 {
		return nil
	}
	out := make([]LinkStats, len(ls.ids))
	ls.mu.Lock()
	now := ls.now()
	for i := range out {
		out[i].Mints, out[i].Failures, out[i].LastError = ls.mints[i], ls.failures[i], ls.lastErr[i]
		if d := ls.deadUntil[i].Sub(now); d > 0 {
			out[i].DeadForSec = int32((d + time.Second - 1) / time.Second)
		}
	}
	ls.mu.Unlock()
	if cp := p.credPool; cp != nil {
		cp.mu.Lock()
		for _, e := range cp.pool {
			if e.creds == nil || e.creds.Link < 0 || e.creds.Link >= len(out) {
				continue
			}
			out[e.creds.Link].Creds++
			out[e.creds.Link].Active += int32(e.active)
		}
		cp.mu.Unlock()
	}
	return out
}

// firstLink is the link Config names first: VKLinks[0], or VKLink.
func firstLink(cfg Config) string {
	if len(cfg.VKLinks) > 0 {
		return cfg.VKLinks[0]
	}
	return cfg.VKLink
}

// parseVKLinkIDs is every configured link's id, in order.
func parseVKLinkIDs(cfg Config) []string {
	links := cfg.VKLinks
	if len(links) == 0 {
		links = []string{cfg.VKLink}
	}
	ids := make([]string, len(links))
	for i, l := range links {
		ids[i] = parseVKLinkID(l)
	}
	return ids
}
