package main

// The log the window shows: the daemon's lines and the service's own, the
// last logLines of them, each with a sequence number so that the page asks
// only for what it has not seen (and appends, never redraws — ui.go).

import (
	"strings"
	"sync"
	"time"
)

const logLines = 2000

type logRing struct {
	mu    sync.Mutex
	lines []string
	next  int64 // the sequence number of the next line
}

func (l *logRing) add(line string) {
	line = strings.TrimRight(line, "\r\n")
	stamp := time.Now().Format("15:04:05 ")
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, stamp+line)
	if len(l.lines) > logLines {
		l.lines = append(l.lines[:0:0], l.lines[len(l.lines)-logLines:]...)
	}
	l.next++
}

// since returns the lines after sequence number after (-1: all kept), and
// the number to ask from next time.
func (l *logRing) since(after int64) ([]string, int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	first := l.next - int64(len(l.lines)) // the sequence number of lines[0]
	from := after + 1
	if from < first {
		from = first
	}
	if from >= l.next {
		return nil, l.next - 1
	}
	return append([]string(nil), l.lines[from-first:]...), l.next - 1
}

// last is the last line, without its time.
func (l *logRing) last() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lines) == 0 {
		return ""
	}
	return l.lines[len(l.lines)-1][len("15:04:05 "):]
}

// Write lets the log package write here, a line per call.
func (l *logRing) Write(p []byte) (int, error) {
	for _, s := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		l.add(s)
	}
	return len(p), nil
}
