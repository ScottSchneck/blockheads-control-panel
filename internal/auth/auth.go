// Package auth keeps the panel's owner account and sign-in sessions.
//
// Phase 1 has one account, the owner. Until it exists the panel is in setup
// mode, and creating it needs the setup code from the container log, so
// nobody else on the network can claim the panel first. The same code file
// is used to reset a forgotten password (see WriteResetCode).
//
// Files in the data folder:
//
//	accounts.json  the owner account; the password is hashed with Argon2id
//	sessions.json  signed-in browsers (only hashes of their tokens)
//	setup-code     present while setup or a password reset is pending
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Mode is what the sign-in page should show.
type Mode string

const (
	ModeSetup Mode = "setup" // no owner yet: create one with the setup code
	ModeLogin Mode = "login"
)

const (
	SessionLength = 30 * 24 * time.Hour // since the browser was last used
	maxSessions   = 50
	lockAfter     = 5 // wrong tries from one address in lockWindow
	lockWindow    = 15 * time.Minute
	maxInFlight   = 2 // password checks at once from one address
	resetCodeLife = time.Hour
	minPassword   = 8
	maxPassword   = 200
	hashWait      = 15 * time.Second
)

var (
	ErrBadLogin   = errors.New("that username and password don't match")
	ErrBadCode    = errors.New("that code isn't right")
	ErrNoCode     = errors.New("no reset code is waiting, or it's more than an hour old; run \"docker exec blockheads blockheads reset-password\" to make a new one")
	ErrHasOwner   = errors.New("the panel already has an owner; sign in instead")
	ErrBusy       = errors.New("the panel is busy checking passwords; try again in a moment")
	ErrChanged    = errors.New("the password was just changed; sign in again")
	reUsername    = regexp.MustCompile(`^[\pL\pN][\pL\pN ._-]{0,31}$`)
	errUsername   = errors.New("pick a username of 1 to 32 letters, numbers, spaces, dots, dashes or underscores")
	errPassLength = fmt.Errorf("use a password of at least %d characters", minPassword)
)

// LockedError means too many wrong tries; wait and try again.
type LockedError struct{ Wait time.Duration }

func (e *LockedError) Error() string {
	m := int(e.Wait.Minutes() + 0.999)
	if m < 1 {
		m = 1
	}
	plural := "s"
	if m == 1 {
		plural = ""
	}
	return fmt.Sprintf("too many wrong tries; try again in %d minute%s", m, plural)
}

// Account is the owner.
type Account struct {
	Username        string    `json:"username"`
	Hash            string    `json:"hash"`
	Created         time.Time `json:"created"`
	PasswordChanged time.Time `json:"passwordChanged"`
}

type session struct {
	Hash     string    `json:"hash"` // SHA-256 of the token
	Username string    `json:"username"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"lastSeen"`
}

type failures struct {
	times    []time.Time
	until    time.Time
	inFlight int
}

// Store holds the account and sessions.
type Store struct {
	dir string

	mu sync.Mutex
	// legacyCode is accepted as the setup code while there's no owner: the
	// old shared panel password (PANEL_PASSWORD or /data/panel-password), so
	// existing installs don't need to look up anything new.
	legacyCode string
	owner      *Account
	sessions   map[string]*session
	fails      map[string]*failures // by address
	hashSem    chan struct{}

	now func() time.Time
}

// Open loads the account and sessions from dir. legacyCode is the old panel
// password, if any.
func Open(dir, legacyCode string) (*Store, error) {
	s := &Store{
		dir: dir, legacyCode: strings.TrimSpace(legacyCode),
		sessions: map[string]*session{}, fails: map[string]*failures{},
		hashSem: make(chan struct{}, 2), // Argon2 uses 64 MB each
		now:     time.Now,
	}
	b, err := os.ReadFile(s.path("accounts.json"))
	switch {
	case err == nil:
		var f struct {
			Owner *Account `json:"owner"`
		}
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("accounts.json is damaged: %w", err)
		}
		if f.Owner != nil && f.Owner.Username != "" && f.Owner.Hash != "" {
			s.owner = f.Owner
		}
	case !os.IsNotExist(err):
		return nil, err
	}
	if b, err := os.ReadFile(s.path("sessions.json")); err == nil {
		var list []*session
		if json.Unmarshal(b, &list) == nil {
			for _, x := range list {
				if s.owner != nil && strings.EqualFold(x.Username, s.owner.Username) && s.now().Sub(x.LastSeen) < SessionLength {
					s.sessions[x.Hash] = x
				}
			}
		}
	}
	return s, nil
}

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

// Mode says whether the panel needs setting up.
func (s *Store) Mode() Mode {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner == nil {
		return ModeSetup
	}
	return ModeLogin
}

// UsesOldPassword reports whether the old shared panel password works as the
// setup code.
func (s *Store) UsesOldPassword() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.owner == nil && s.legacyCode != ""
}

// ResetPending reports whether a usable password-reset code is waiting.
func (s *Store) ResetPending() bool {
	s.mu.Lock()
	has := s.owner != nil
	s.mu.Unlock()
	return has && s.savedCode(true) != ""
}

// savedCode reads the code file ("" if there's none, it can't be read, or,
// for a reset, it's too old).
func (s *Store) savedCode(reset bool) string {
	f, err := os.Open(s.path("setup-code"))
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || (reset && s.now().Sub(st.ModTime()) > resetCodeLife) {
		return ""
	}
	b := make([]byte, 128)
	n, _ := f.Read(b)
	return normCode(string(b[:n]))
}

// EnsureSetupCode makes sure a setup code exists while there's no owner,
// and returns it ("" when there's an owner, or when the old panel password
// is the code).
func (s *Store) EnsureSetupCode() (code string, legacy bool, err error) {
	s.mu.Lock()
	has, legacy := s.owner != nil, s.legacyCode != ""
	s.mu.Unlock()
	if has {
		return "", false, nil
	}
	if legacy {
		return "", true, nil
	}
	if b, err := os.ReadFile(s.path("setup-code")); err == nil && strings.TrimSpace(string(b)) != "" {
		return strings.TrimSpace(string(b)), false, nil
	}
	code, err = WriteResetCode(s.dir)
	return code, false, err
}

// WriteResetCode makes a new setup code and saves it in dir. It's used for
// first-time setup and, from the command line, to reset a forgotten
// password. The code works once, and for a reset, for an hour.
func WriteResetCode(dir string) (string, error) {
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	raw := strings.ToLower(base32.StdEncoding.EncodeToString(buf)) // 16 characters
	code := raw[:4] + "-" + raw[4:8] + "-" + raw[8:12] + "-" + raw[12:]
	path := filepath.Join(dir, "setup-code")
	tmp, err := os.CreateTemp(dir, ".setup-code-")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(code + "\n"); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	// Run as root (docker exec -u 0), the file would be unreadable by the
	// panel; give it to whoever owns the data folder.
	if os.Geteuid() == 0 {
		if st, err := os.Stat(dir); err == nil {
			if sys, ok := st.Sys().(*syscall.Stat_t); ok {
				_ = os.Chown(tmp.Name(), int(sys.Uid), int(sys.Gid))
			}
		}
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return code, nil
}

func normCode(c string) string {
	return strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(c)))
}

// codeMatches compares a typed code with the saved one or, during first
// setup, the old panel password. Caller holds mu, so a code can only be used
// once.
func (s *Store) codeMatchesLocked(code string, setup bool) error {
	if setup && s.legacyCode != "" {
		typed := strings.TrimSpace(code)
		if subtle.ConstantTimeCompare([]byte(typed), []byte(s.legacyCode)) == 1 {
			return nil
		}
		// Phones like to capitalise the first letter; generated passwords
		// were all lower case.
		if s.legacyCode == strings.ToLower(s.legacyCode) && subtle.ConstantTimeCompare([]byte(strings.ToLower(typed)), []byte(s.legacyCode)) == 1 {
			return nil
		}
	}
	want := s.savedCode(!setup)
	if want == "" {
		if setup {
			return ErrBadCode
		}
		return ErrNoCode
	}
	if subtle.ConstantTimeCompare([]byte(normCode(code)), []byte(want)) != 1 {
		return ErrBadCode
	}
	return nil
}

func checkPassword(p string) error {
	if utf8.RuneCountInString(p) < minPassword {
		return errPassLength
	}
	if len(p) > maxPassword {
		return errors.New("that password is too long")
	}
	return nil
}

// Setup creates the owner account. It returns a session token.
func (s *Store) Setup(ctx context.Context, ip, code, username, password string) (string, error) {
	username = strings.TrimSpace(username)
	if !reUsername.MatchString(username) {
		return "", errUsername
	}
	if err := checkPassword(password); err != nil {
		return "", err
	}
	done, err := s.begin(ip)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	if s.owner != nil {
		s.mu.Unlock()
		done(tryNeutral)
		return "", ErrHasOwner
	}
	err = s.codeMatchesLocked(code, true)
	s.mu.Unlock()
	if err != nil {
		done(tryWrong)
		return "", err
	}
	done(tryRight)
	hash, err := s.hash(ctx, password)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner != nil {
		return "", ErrHasOwner // two setups at once
	}
	if err := s.codeMatchesLocked(code, true); err != nil {
		return "", err // used meanwhile
	}
	now := s.now().UTC()
	acct := &Account{Username: username, Hash: hash, Created: now, PasswordChanged: now}
	if err := s.saveAccountLocked(acct); err != nil {
		return "", err
	}
	s.owner = acct
	_ = os.Remove(s.path("setup-code"))
	_ = os.Remove(s.path("panel-password")) // the old shared password is gone for good
	s.legacyCode = ""
	return s.newSessionLocked(username)
}

// Login checks a username and password and returns a session token.
func (s *Store) Login(ctx context.Context, ip, username, password string) (string, error) {
	username = strings.TrimSpace(username)
	done, err := s.begin(ip)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	owner := s.owner
	s.mu.Unlock()
	var ok bool
	if owner != nil && strings.EqualFold(owner.Username, username) {
		ok, err = s.verify(ctx, owner.Hash, password)
	} else {
		_, err = s.verify(ctx, dummyHash(), password) // same time either way
	}
	if err != nil {
		done(tryNeutral) // not the caller's fault
		return "", err
	}
	done(result(ok))
	if !ok {
		return "", ErrBadLogin
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner != owner {
		return "", ErrChanged // reset or changed while this was checked
	}
	return s.newSessionLocked(owner.Username)
}

// Reset sets a new owner password using a code from WriteResetCode. Every
// browser is signed out, and a new session is returned.
func (s *Store) Reset(ctx context.Context, ip, code, password string) (string, error) {
	if err := checkPassword(password); err != nil {
		return "", err
	}
	done, err := s.begin(ip)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	if s.owner == nil {
		s.mu.Unlock()
		done(tryNeutral)
		return "", errors.New("the panel has no owner yet; set it up instead")
	}
	err = s.codeMatchesLocked(code, false)
	s.mu.Unlock()
	if err != nil {
		done(tryWrong)
		return "", err
	}
	done(tryRight)
	hash, err := s.hash(ctx, password)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.codeMatchesLocked(code, false); err != nil {
		return "", err // used meanwhile
	}
	acct := *s.owner
	acct.Hash, acct.PasswordChanged = hash, s.now().UTC()
	if err := s.saveAccountLocked(&acct); err != nil {
		return "", err
	}
	s.owner = &acct
	_ = os.Remove(s.path("setup-code"))
	s.sessions = map[string]*session{}
	return s.newSessionLocked(acct.Username)
}

// ChangePassword changes the owner's password. Other browsers are signed
// out; keep is the current session's token.
func (s *Store) ChangePassword(ctx context.Context, ip, keep, current, next string) error {
	if err := checkPassword(next); err != nil {
		return err
	}
	done, err := s.begin(ip)
	if err != nil {
		return err
	}
	s.mu.Lock()
	owner := s.owner
	s.mu.Unlock()
	if owner == nil {
		done(tryNeutral)
		return errors.New("no account")
	}
	ok, err := s.verify(ctx, owner.Hash, current)
	if err != nil {
		done(tryNeutral)
		return err
	}
	done(result(ok))
	if !ok {
		return errors.New("your current password isn't right")
	}
	hash, err := s.hash(ctx, next)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner != owner {
		return ErrChanged
	}
	acct := *s.owner
	acct.Hash, acct.PasswordChanged = hash, s.now().UTC()
	if err := s.saveAccountLocked(&acct); err != nil {
		return err
	}
	s.owner = &acct
	keepHash := tokenHash(keep)
	for h := range s.sessions {
		if h != keepHash {
			delete(s.sessions, h)
		}
	}
	return s.saveSessionsLocked()
}

// Check returns the username for a session token, or "" if it isn't valid.
// Using a session keeps it alive.
func (s *Store) Check(token string) string {
	if token == "" {
		return ""
	}
	h := tokenHash(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	x, ok := s.sessions[h]
	if !ok || s.owner == nil {
		return ""
	}
	now := s.now()
	if now.Sub(x.LastSeen) >= SessionLength {
		delete(s.sessions, h)
		_ = s.saveSessionsLocked()
		return ""
	}
	if now.Sub(x.LastSeen) > time.Hour {
		x.LastSeen = now.UTC()
		_ = s.saveSessionsLocked()
	}
	return s.owner.Username
}

// Logout ends one session.
func (s *Store) Logout(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, tokenHash(token))
	_ = s.saveSessionsLocked()
}

// LogoutAll ends every session.
func (s *Store) LogoutAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = map[string]*session{}
	_ = s.saveSessionsLocked()
}

// Username returns the owner's name ("" before setup).
func (s *Store) Username() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner == nil {
		return ""
	}
	return s.owner.Username
}

// IsOwnerName reports whether name is the owner's username (for logging
// without writing down whatever else people type).
func (s *Store) IsOwnerName(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.owner != nil && strings.EqualFold(strings.TrimSpace(name), s.owner.Username)
}

func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (s *Store) newSessionLocked(username string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	now := s.now().UTC()
	s.sessions[tokenHash(token)] = &session{Hash: tokenHash(token), Username: username, Created: now, LastSeen: now}
	if len(s.sessions) > maxSessions {
		var list []*session
		for _, x := range s.sessions {
			list = append(list, x)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].LastSeen.Before(list[j].LastSeen) })
		for _, x := range list[:len(list)-maxSessions] {
			delete(s.sessions, x.Hash)
		}
	}
	if err := s.saveSessionsLocked(); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Store) saveSessionsLocked() error {
	list := make([]*session, 0, len(s.sessions))
	for _, x := range s.sessions {
		list = append(list, x)
	}
	b, _ := json.MarshalIndent(list, "", "  ")
	return writeFile(s.path("sessions.json"), b)
}

func (s *Store) saveAccountLocked(a *Account) error {
	b, _ := json.MarshalIndent(map[string]*Account{"owner": a}, "", "  ")
	return writeFile(s.path("accounts.json"), b)
}

func writeFile(path string, b []byte) error {
	if err := os.WriteFile(path+".tmp", b, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// ---- wrong-password limits ----

// addrKey groups addresses: one IPv6 home network gets many addresses, so
// they count together.
func addrKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap()
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.String()
	}
	return a.String()
}

// Outcomes of a try, for begin's done.
type outcome int

const (
	tryRight   outcome = iota // correct password or code: forget earlier wrong tries
	tryWrong                  // counts toward the lockout
	tryNeutral                // nothing was checked (busy, cancelled, wrong mode)
)

func result(ok bool) outcome {
	if ok {
		return tryRight
	}
	return tryWrong
}

// begin starts a password or code check from an address. It refuses while
// the address is locked out, or has too many checks running (so sending
// many at once can't get round the limit). Call done with the outcome.
func (s *Store) begin(ip string) (done func(outcome), err error) {
	key := addrKey(ip)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	f := s.fails[key]
	if f == nil {
		f = &failures{}
		s.fails[key] = f
	}
	if now.Before(f.until) {
		return nil, &LockedError{Wait: f.until.Sub(now)}
	}
	recent := f.times[:0]
	for _, t := range f.times {
		if now.Sub(t) < lockWindow {
			recent = append(recent, t)
		}
	}
	f.times = recent
	if f.inFlight >= maxInFlight || len(f.times)+f.inFlight >= lockAfter {
		if len(f.times) >= lockAfter {
			f.until = now.Add(lockWindow)
			return nil, &LockedError{Wait: lockWindow}
		}
		return nil, ErrBusy
	}
	f.inFlight++
	once := sync.Once{}
	return func(o outcome) {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			f.inFlight--
			switch o {
			case tryRight:
				f.times = nil
			case tryWrong:
				f.times = append(f.times, s.now())
				if len(f.times) >= lockAfter {
					f.until = s.now().Add(lockWindow)
					f.times = nil
				}
			}
			s.pruneFailsLocked()
		})
	}, nil
}

// pruneFailsLocked forgets addresses that have nothing going on.
func (s *Store) pruneFailsLocked() {
	if len(s.fails) < 1000 {
		return
	}
	now := s.now()
	for k, f := range s.fails {
		if f.inFlight == 0 && now.After(f.until) && (len(f.times) == 0 || now.Sub(f.times[len(f.times)-1]) > lockWindow) {
			delete(s.fails, k)
		}
	}
}

// ---- password hashing ----

const (
	argonTime    = 2
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 2
	argonKeyLen  = 32
)

// dummyHash is checked against when the username is wrong, so a wrong name
// takes as long as a wrong password. Made on first use, so the command line
// tools don't pay for it.
var dummyHash = sync.OnceValue(func() string {
	return hashWith("not-a-real-password", make([]byte, 16))
})

// slot waits for one of the hashing slots (each Argon2 run uses 64 MB).
func (s *Store) slot(ctx context.Context) (func(), error) {
	t := time.NewTimer(hashWait)
	defer t.Stop()
	select {
	case s.hashSem <- struct{}{}:
		return func() { <-s.hashSem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.C:
		return nil, ErrBusy
	}
}

func (s *Store) hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	release, err := s.slot(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	return hashWith(password, salt), nil
}

func hashWith(password string, salt []byte) string {
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// verify checks a password against a stored hash. An error means it
// couldn't be checked (busy, or the request went away).
func (s *Store) verify(ctx context.Context, encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, nil
	}
	var mem, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil || mem == 0 || mem > 1<<20 || t == 0 || t > 20 || p == 0 {
		return false, nil
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, nil
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 || len(want) > 64 {
		return false, nil
	}
	release, err := s.slot(ctx)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, t, mem, p, uint32(len(want)))
	release()
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
