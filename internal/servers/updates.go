package servers

import (
	"context"
	"os"
	"sync"
	"time"
)

// Checking for new Bedrock versions, for the "update ready" notes.

const versionCheckEvery = time.Hour

type versionCache struct {
	mu      sync.Mutex
	version string
	err     error
	at      time.Time
	busy    bool
}

// LatestRelease returns the newest Bedrock release Mojang offers, checking at
// most once an hour. It never waits on the network: the first call starts a
// check and returns "" until it's done.
func (m *Manager) LatestRelease() (string, error) {
	c := &m.latest
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.busy && time.Since(c.at) > versionCheckEvery {
		c.busy = true
		go func() {
			ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
			defer cancel()
			_, v, err := m.latestBedrock(ctx, false)
			c.mu.Lock()
			defer c.mu.Unlock()
			c.busy, c.at = false, time.Now()
			if err != nil {
				c.err = err // keep the last version we knew
				return
			}
			c.version, c.err = v, nil
		}()
	}
	return c.version, c.err
}

// lastBackup returns when the server's newest backup was made (zero if
// none). The dashboard asks every couple of seconds, so the answer is kept for
// a minute (or until a backup is made or deleted) rather than reading the
// backups folder each time, which would keep Unraid's disks awake.
func (s *Server) lastBackup() time.Time {
	s.mu.Lock()
	if !s.lbChecked.IsZero() && time.Since(s.lbChecked) < time.Minute {
		t := s.lbTime
		s.mu.Unlock()
		return t
	}
	s.mu.Unlock()
	var newest time.Time
	if entries, err := os.ReadDir(s.m.backupsDir(s.meta.ID)); err == nil {
		for _, e := range entries {
			if b, ok := s.parseBackup(e); ok && b.Time.After(newest) {
				newest = b.Time
			}
		}
	}
	s.mu.Lock()
	s.lbTime, s.lbChecked = newest, time.Now()
	s.mu.Unlock()
	return newest
}

// forgetLastBackupLocked makes the next lastBackup look again. Caller holds s.mu.
func (s *Server) forgetLastBackupLocked() { s.lbChecked = time.Time{} }

func (s *Server) forgetLastBackup() {
	s.mu.Lock()
	s.forgetLastBackupLocked()
	s.mu.Unlock()
}
