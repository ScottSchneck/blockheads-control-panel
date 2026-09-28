// Package activity keeps a log of who did what in the panel ("Ava stopped
// Ava's Server"), so a family can see what happened to a server.
//
// Entries are appended to <data>/activity.jsonl, one JSON object per line.
// The newest entries are also kept in memory; when the file grows past a few
// megabytes it's rewritten with just those.
package activity

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	keep    = 2000    // entries kept in memory and after a rewrite
	maxFile = 4 << 20 // rewrite the file beyond this size
)

// Entry is one thing someone did.
type Entry struct {
	Time     time.Time `json:"time"`
	User     string    `json:"user"`
	ServerID string    `json:"serverId,omitempty"`
	Server   string    `json:"server,omitempty"` // its name at the time
	Text     string    `json:"text"`             // e.g. "stopped the server"
}

// Log is the activity log.
type Log struct {
	path    string
	mu      sync.Mutex
	entries []Entry // oldest first
	size    int64
}

// Open reads the log at path (it's fine if it doesn't exist yet).
func Open(path string) *Log {
	l := &Log{path: path}
	f, err := os.Open(path)
	if err != nil {
		return l
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil && !e.Time.IsZero() {
			l.entries = append(l.entries, e)
			if len(l.entries) > 2*keep {
				l.entries = append([]Entry(nil), l.entries[len(l.entries)-keep:]...)
			}
		}
	}
	if len(l.entries) > keep {
		l.entries = l.entries[len(l.entries)-keep:]
	}
	if st, err := f.Stat(); err == nil {
		l.size = st.Size()
	}
	return l
}

// Add records an entry. Writing the file is best effort: a full disk
// shouldn't stop the panel doing what was asked.
func (l *Log) Add(e Entry) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, e)
	if len(l.entries) > keep {
		l.entries = l.entries[len(l.entries)-keep:]
	}
	b, _ := json.Marshal(e)
	b = append(b, '\n')
	if l.size+int64(len(b)) > maxFile && l.rewriteLocked() {
		return // the rewrite included this entry
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	if n, err := f.Write(b); err == nil {
		l.size += int64(n)
	}
	f.Close()
}

// rewriteLocked writes the file again with just the entries in memory. If
// it can't, the caller appends as usual.
func (l *Log) rewriteLocked() bool {
	f, err := os.CreateTemp(filepath.Dir(l.path), ".activity-")
	if err != nil {
		return false
	}
	var n int64
	w := bufio.NewWriter(f)
	for _, e := range l.entries {
		b, _ := json.Marshal(e)
		m, _ := w.Write(append(b, '\n'))
		n += int64(m)
	}
	ferr := w.Flush()
	if cerr := f.Close(); ferr != nil || cerr != nil {
		os.Remove(f.Name())
		return false
	}
	if os.Rename(f.Name(), l.path) != nil {
		os.Remove(f.Name())
		return false
	}
	l.size = n
	return true
}

// List returns up to limit entries, newest first, that keep says to show.
func (l *Log) List(limit int, show func(Entry) bool) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []Entry{}
	for i := len(l.entries) - 1; i >= 0 && len(out) < limit; i-- {
		if show == nil || show(l.entries[i]) {
			out = append(out, l.entries[i])
		}
	}
	return out
}
