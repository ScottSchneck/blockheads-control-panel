package auth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newOwner(t *testing.T) (*Store, string, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	code, legacy, err := s.EnsureSetupCode()
	if err != nil || legacy || len(code) != 19 {
		t.Fatalf("code %q %v %v", code, legacy, err)
	}
	token, err := s.Setup(context.Background(), "ip", code, "Scott", "long enough pw")
	if err != nil {
		t.Fatal(err)
	}
	return s, dir, token
}

func TestPasswordIsHashed(t *testing.T) {
	_, dir, _ := newOwner(t)
	b, _ := os.ReadFile(filepath.Join(dir, "accounts.json"))
	if strings.Contains(string(b), "long enough pw") || !strings.Contains(string(b), "$argon2id$") {
		t.Errorf("accounts.json: %s", b)
	}
	st, _ := os.Stat(filepath.Join(dir, "accounts.json"))
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	sb, _ := os.ReadFile(filepath.Join(dir, "sessions.json"))
	if len(sb) == 0 || strings.Contains(string(sb), "token") {
		t.Errorf("sessions.json: %s", sb)
	}
}

func TestSessionsExpireWhenUnused(t *testing.T) {
	s, dir, token := newOwner(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	if s.Check(token) != "Scott" {
		t.Fatal("fresh session")
	}
	now = now.Add(20 * 24 * time.Hour)
	if s.Check(token) != "Scott" {
		t.Fatal("used within 30 days")
	}
	now = now.Add(20 * 24 * time.Hour) // 20 days after last use
	if s.Check(token) != "Scott" {
		t.Fatal("using it should have kept it alive")
	}
	now = now.Add(31 * 24 * time.Hour)
	if s.Check(token) != "" {
		t.Fatal("unused for 31 days")
	}
	if s.Check("made-up") != "" || s.Check("") != "" {
		t.Fatal("unknown token")
	}
	// Reopened, still gone.
	s2, _ := Open(dir, "")
	if s2.Check(token) != "" {
		t.Fatal("expired session came back")
	}
}

func TestLockoutEnds(t *testing.T) {
	s, _, _ := newOwner(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	for i := 0; i < lockAfter; i++ {
		if _, err := s.Login(context.Background(), "1.2.3.4", "Scott", "wrong"); err != ErrBadLogin {
			t.Fatalf("try %d: %v", i, err)
		}
	}
	_, err := s.Login(context.Background(), "1.2.3.4", "Scott", "long enough pw")
	if _, ok := err.(*LockedError); !ok {
		t.Fatalf("should be locked: %v", err)
	}
	// Other addresses aren't locked out.
	if _, err := s.Login(context.Background(), "5.6.7.8", "Scott", "long enough pw"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(lockWindow + time.Second)
	if _, err := s.Login(context.Background(), "1.2.3.4", "Scott", "long enough pw"); err != nil {
		t.Fatalf("after the wait: %v", err)
	}
	// A wrong username is refused the same way as a wrong password.
	if _, err := s.Login(context.Background(), "9.9.9.9", "Nobody", "long enough pw"); err != ErrBadLogin {
		t.Fatal(err)
	}
}

func TestBadStoredHashIsRefused(t *testing.T) {
	s, _, _ := newOwner(t)
	for _, h := range []string{"", "plain", "$argon2id$v=19$m=999999999,t=1,p=1$AAAA$AAAA", "$bcrypt$x$y$z$w"} {
		if ok, _ := s.verify(context.Background(), h, "x"); ok {
			t.Errorf("%q verified", h)
		}
	}
}

func TestParallelTriesDontGetRoundTheLimit(t *testing.T) {
	s, _, _ := newOwner(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	checked := 0
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Login(context.Background(), "1.2.3.4", "Scott", "wrong")
			if err == ErrBadLogin {
				mu.Lock()
				checked++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if checked > lockAfter {
		t.Errorf("%d wrong passwords were checked; the limit is %d", checked, lockAfter)
	}
	// Keep going one at a time: the total checked still stops at the limit.
	for i := 0; i < 10; i++ {
		if _, err := s.Login(context.Background(), "1.2.3.4", "Scott", "wrong"); err == ErrBadLogin {
			checked++
		}
	}
	if checked != lockAfter {
		t.Errorf("%d checked in all", checked)
	}
	if _, err := s.Login(context.Background(), "1.2.3.4", "Scott", "long enough pw"); err == nil {
		t.Error("should be locked now")
	}
	// Addresses in one IPv6 /64 count together.
	for i := 0; i < lockAfter; i++ {
		s.Login(context.Background(), "2001:db8::"+string(rune('a'+i)), "Scott", "wrong")
	}
	if _, err := s.Login(context.Background(), "2001:db8::ffff", "Scott", "long enough pw"); err == nil {
		t.Error("the /64 should be locked")
	}
}

func TestResetCodeExpiresAndWorksOnce(t *testing.T) {
	s, dir, _ := newOwner(t)
	code, err := WriteResetCode(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !s.ResetPending() {
		t.Fatal("pending")
	}
	now := time.Now()
	s.now = func() time.Time { return now.Add(2 * time.Hour) }
	if s.ResetPending() {
		t.Error("a two-hour-old code shouldn't count")
	}
	if _, err := s.Reset(context.Background(), "ip2", code, "a new password"); err != ErrNoCode {
		t.Errorf("old code: %v", err)
	}
	s.now = time.Now
	if _, err := s.Reset(context.Background(), "ip3", strings.ToUpper(code), "a new password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reset(context.Background(), "ip4", code, "another password"); err == nil {
		t.Error("used twice")
	}
}

func TestOldPasswordWithACapital(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, "abcdefghijklmnop")
	if _, err := s.Setup(context.Background(), "ip", "Abcdefghijklmnop", "Scott", "long enough pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "setup-code")); err == nil {
		t.Error("no setup-code file should be made when the old password is the code")
	}
}

func TestNothingElseClearsWrongTries(t *testing.T) {
	s, _, _ := newOwner(t)
	for round := 0; round < 3; round++ {
		for i := 0; i < lockAfter-1; i++ {
			s.Login(context.Background(), "1.2.3.4", "Scott", "wrong")
		}
		// Setup when there's already an owner must not wipe the count.
		s.Setup(context.Background(), "1.2.3.4", "x", "Eve", "long enough pw")
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // nor must a cancelled check
		s.Login(ctx, "1.2.3.4", "Scott", "wrong")
	}
	if _, err := s.Login(context.Background(), "1.2.3.4", "Scott", "long enough pw"); err == nil {
		t.Error("should have been locked out long ago")
	}
}

func TestOtherAccounts(t *testing.T) {
	s, dir, owner := newOwner(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, "Ava", "ava password 1", false); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, "SCOTT", "ava password 1", false); err == nil {
		t.Error("an account with the owner's name")
	}
	if err := s.SetUser("ava", nil, map[string]string{"a": RoleCare, "b": RoleStart, "c": ""}); err != nil {
		t.Fatal(err)
	}
	ava, err := s.Login(ctx, "ip2", "AVA", "ava password 1")
	if err != nil {
		t.Fatal(err)
	}
	p := s.Who(ava)
	if p == nil || p.Name != "Ava" || p.Owner || p.Level("a") != 3 || p.Level("b") != 1 || p.Level("c") != 0 {
		t.Fatalf("Ava: %+v", p)
	}
	if o := s.Who(owner); o == nil || !o.Owner || o.Level("anything") != OwnerLevel {
		t.Fatalf("owner: %+v", o)
	}
	// Ava changing her password signs out her other browsers, not the owner.
	ava2, _ := s.Login(ctx, "ip2", "Ava", "ava password 1")
	if err := s.ChangePassword(ctx, "ip2", ava, "ava password 1", "ava password 2"); err != nil {
		t.Fatal(err)
	}
	if s.Who(ava2) != nil || s.Who(ava) == nil || s.Who(owner) == nil {
		t.Error("wrong sessions ended")
	}
	s.LogoutAll(ava)
	if s.Who(ava) != nil || s.Who(owner) == nil {
		t.Error("LogoutAll")
	}
	// Grants survive reopening, and the owner can't be given a role.
	if err := s.Grant("scott", "x", RoleRun); err != nil {
		t.Error(err)
	}
	s2, err := Open(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	users := s2.Users()
	if len(users) != 1 || users[0].Grants["a"] != RoleCare || len(users[0].Grants) != 2 {
		t.Errorf("users: %+v", users)
	}
	if s2.Who(owner) == nil {
		t.Error("owner signed out by reopening")
	}
	if _, err := s2.Login(ctx, "ip3", "ava", "ava password 2"); err != nil {
		t.Error(err)
	}
	if err := s2.DeleteUser("Ava"); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Login(ctx, "ip3", "ava", "ava password 2"); err == nil {
		t.Error("removed account signed in")
	}
}

func TestRemovingByAnUntidyNameSignsOut(t *testing.T) {
	s, _, _ := newOwner(t)
	ctx := context.Background()
	s.CreateUser(ctx, "Ava", "ava password 1", false)
	tok, _ := s.Login(ctx, "ip", "ava", "ava password 1")
	// Permission changes don't count as a password change.
	s.SetUser("Ava", nil, map[string]string{"x": RoleRun})
	s.SetUser("Ava", nil, map[string]string{"y": RoleStart})
	if u := s.Users(); len(u[0].Grants) != 2 {
		t.Errorf("changes undid each other: %v", u[0].Grants)
	}
	if err := s.CreateUser(ctx, "ſam", "sam password 1", false); err == nil {
		t.Error("a name that looks like another")
	}
	if err := s.DeleteUser(" AVA "); err != nil {
		t.Fatal(err)
	}
	if s.Who(tok) != nil {
		t.Error("session outlived its account")
	}
}
