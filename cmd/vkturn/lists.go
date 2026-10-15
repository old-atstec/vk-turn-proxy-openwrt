package main

// The domain lists: sing-box rule-sets (.srs) by URL, fetched into the data
// directory and read with pkg/srs. A list is replaced only by a copy that
// parses — a broken download keeps the old one — and fetched again after
// list_hours (or by the button). The fetch goes the way the system routes
// it: a list that is itself behind a block is fetched through the tunnel
// once its name is in a list.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cacggghp/vk-turn-proxy/pkg/srs"
)

const maxListBytes = 32 << 20

type listState struct {
	URL     string    `json:"url"`
	Updated time.Time `json:"updated"` // the cached copy's time, zero if none
	Error   string    `json:"error,omitempty"`
	Rules   int       `json:"rules"`
	Names   int       `json:"names"` // exact names and suffixes
	Other   int       `json:"other"` // keywords and regexes
	Nets    int       `json:"nets"`  // ip_cidr prefixes
	Skipped int       `json:"skipped,omitempty"`
}

type lists struct {
	dir    string
	client *http.Client

	mu    sync.Mutex
	sets  map[string]*srs.RuleSet
	state map[string]*listState
}

func newLists(dir string) *lists {
	return &lists{
		dir:    dir,
		client: &http.Client{Timeout: 90 * time.Second},
		sets:   map[string]*srs.RuleSet{},
		state:  map[string]*listState{},
	}
}

func (l *lists) cachePath(url string) string {
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(l.dir, hex.EncodeToString(sum[:12])+".srs")
}

// load reads the cached copies of urls (those not read yet).
func (l *lists) load(urls []string) {
	for _, u := range urls {
		l.mu.Lock()
		_, have := l.sets[u]
		l.mu.Unlock()
		if have {
			continue
		}
		p := l.cachePath(u)
		f, err := os.Open(p)
		if err != nil {
			l.setState(u, nil, time.Time{}, nil)
			continue
		}
		fi, _ := f.Stat()
		rs, err := srs.Read(f)
		f.Close()
		if err != nil {
			l.setState(u, nil, time.Time{}, fmt.Errorf("копия на диске: %w", err))
			continue
		}
		l.setState(u, rs, fi.ModTime(), nil)
	}
}

// update fetches the lists older than every (all of them with force) and
// reports whether any changed.
func (l *lists) update(ctx context.Context, urls []string, every time.Duration, force bool) bool {
	l.load(urls)
	changed := false
	for _, u := range urls {
		l.mu.Lock()
		st := l.state[u]
		l.mu.Unlock()
		if !force && st != nil && !st.Updated.IsZero() && (every <= 0 || time.Since(st.Updated) < every) {
			continue
		}
		rs, err := l.fetch(ctx, u)
		if err != nil {
			l.mu.Lock()
			l.stateLocked(u).Error = err.Error()
			l.mu.Unlock()
			continue
		}
		l.setState(u, rs, time.Now(), nil)
		changed = true
	}
	return changed
}

func (l *lists) fetch(ctx context.Context, u string) (*srs.RuleSet, error) {
	if !strings.HasPrefix(u, "https://") {
		return nil, errors.New("только https")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "vkturn-windows")
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxListBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxListBytes {
		return nil, fmt.Errorf("больше %d МиБ", maxListBytes>>20)
	}
	rs, err := srs.Read(strings.NewReader(string(b)))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(l.dir, ".list-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	return rs, os.Rename(tmp.Name(), l.cachePath(u))
}

func (l *lists) stateLocked(u string) *listState {
	st := l.state[u]
	if st == nil {
		st = &listState{URL: u}
		l.state[u] = st
	}
	return st
}

func (l *lists) setState(u string, rs *srs.RuleSet, t time.Time, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.stateLocked(u)
	*st = listState{URL: u, Updated: t}
	if err != nil {
		st.Error = err.Error()
	}
	if rs != nil {
		l.sets[u] = rs
		st.Rules = rs.Stats.Rules
		st.Names = rs.Stats.Domains + rs.Stats.Suffixes
		st.Other = rs.Stats.Keywords + rs.Stats.Regexes
		st.Nets = rs.Stats.Prefixes
		for _, n := range rs.Stats.Skipped {
			st.Skipped += n
		}
	}
}

// states: the lists of urls, in their order.
func (l *lists) states(urls []string) []listState {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]listState, 0, len(urls))
	for _, u := range urls {
		if st := l.state[u]; st != nil {
			out = append(out, *st)
		} else {
			out = append(out, listState{URL: u})
		}
	}
	return out
}

// matcher is the names of urls and the user's own (each a suffix), and the
// prefixes of the lists' ip_cidr rules.
func (l *lists) matcher(urls []string, own []string) (func(string) bool, []netip.Prefix) {
	l.mu.Lock()
	var sets []*srs.RuleSet
	for _, u := range urls {
		if rs := l.sets[u]; rs != nil {
			sets = append(sets, rs)
		}
	}
	l.mu.Unlock()
	suffix := map[string]struct{}{}
	for _, d := range own {
		suffix[d] = struct{}{}
	}
	var nets []netip.Prefix
	for _, rs := range sets {
		nets = append(nets, rs.Prefixes()...)
	}
	return func(name string) bool {
		for n := name; ; {
			if _, ok := suffix[n]; ok {
				return true
			}
			i := strings.IndexByte(n, '.')
			if i < 0 {
				break
			}
			n = n[i+1:]
		}
		for _, rs := range sets {
			if rs.Match(name) {
				return true
			}
		}
		return false
	}, nets
}
