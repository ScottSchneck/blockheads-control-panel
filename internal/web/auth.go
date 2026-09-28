package web

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/ScottSchneck/blockheads-control-panel/internal/auth"
	"github.com/ScottSchneck/blockheads-control-panel/internal/servers"
)

func (s *Server) authRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/state", s.authState)
	mux.HandleFunc("POST /api/auth/setup", s.authSetup)
	mux.HandleFunc("POST /api/auth/login", s.authLogin)
	mux.HandleFunc("POST /api/auth/reset", s.authReset)
	mux.HandleFunc("POST /api/auth/logout", s.authLogout)
	mux.HandleFunc("POST /api/auth/logout-all", s.authLogoutAll)
	mux.HandleFunc("POST /api/auth/password", s.authPassword)
	mux.HandleFunc("GET /api/setup-guide", s.setupGuide)
	mux.HandleFunc("POST /api/setup-guide/gamertag", s.setupGamertag)
	mux.HandleFunc("POST /api/setup-guide/done", s.setupDone)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) setSession(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(), Value: token, Path: "/",
		HttpOnly: true, Secure: s.opts.TLS, SameSite: http.SameSiteStrictMode,
		MaxAge: int(auth.SessionLength / time.Second),
	})
}

func (s *Server) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: "", Path: "/", HttpOnly: true, Secure: s.opts.TLS, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

func authError(w http.ResponseWriter, err error) {
	var locked *auth.LockedError
	switch {
	case errors.As(err, &locked):
		w.Header().Set("Retry-After", strconv.Itoa(int(locked.Wait/time.Second)+1))
		writeError(w, http.StatusTooManyRequests, err)
	case errors.Is(err, auth.ErrBadLogin), errors.Is(err, auth.ErrBadCode), errors.Is(err, auth.ErrChanged):
		writeError(w, http.StatusUnauthorized, err)
	case errors.Is(err, auth.ErrBusy):
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, err)
	default:
		writeError(w, http.StatusBadRequest, err)
	}
}

func (s *Server) authState(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"version": s.opts.Version}
	if user := s.user(r); user != "" {
		// Using the panel keeps you signed in: renew the cookie too.
		if c, err := r.Cookie(s.cookieName()); err == nil {
			s.setSession(w, c.Value)
		}
		resp["mode"] = "signedIn"
		resp["username"] = user
		resp["setupDone"] = s.mgr.Settings().SetupDone
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp["mode"] = string(s.auth.Mode())
	resp["usesOldPassword"] = s.auth.UsesOldPassword()
	resp["oldPasswordFromEnv"] = s.opts.Password != ""
	resp["resetPending"] = s.auth.ResetPending()
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) authSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code     string `json:"code"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readBody(w, r, &req) {
		return
	}
	token, err := s.auth.Setup(r.Context(), clientIP(r), req.Code, req.Username, req.Password)
	if err != nil {
		s.log.Warn("owner setup refused", "from", clientIP(r), "error", err)
		authError(w, err)
		return
	}
	s.log.Info("owner account created", "username", req.Username, "from", clientIP(r))
	s.setSession(w, token)
	writeJSON(w, http.StatusOK, map[string]string{"username": s.auth.Username()})
}

func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readBody(w, r, &req) {
		return
	}
	token, err := s.auth.Login(r.Context(), clientIP(r), req.Username, req.Password)
	if err != nil {
		// Only name the owner: a password typed into the username box
		// shouldn't end up in the log.
		who := "someone else"
		if s.auth.IsOwnerName(req.Username) {
			who = s.auth.Username()
		}
		s.log.Warn("sign-in failed", "username", who, "from", clientIP(r), "error", err)
		authError(w, err)
		return
	}
	s.log.Info("signed in", "username", s.auth.Username(), "from", clientIP(r))
	s.setSession(w, token)
	writeJSON(w, http.StatusOK, map[string]string{"username": s.auth.Username()})
}

func (s *Server) authReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code     string `json:"code"`
		Password string `json:"password"`
	}
	if !readBody(w, r, &req) {
		return
	}
	token, err := s.auth.Reset(r.Context(), clientIP(r), req.Code, req.Password)
	if err != nil {
		s.log.Warn("password reset refused", "from", clientIP(r), "error", err)
		authError(w, err)
		return
	}
	s.log.Warn("owner password was reset; every browser was signed out", "from", clientIP(r))
	s.setSession(w, token)
	writeJSON(w, http.StatusOK, map[string]string{"username": s.auth.Username()})
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.cookieName()); err == nil {
		s.auth.Logout(c.Value)
	}
	s.clearSession(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) authLogoutAll(w http.ResponseWriter, r *http.Request) {
	s.auth.LogoutAll()
	s.clearSession(w)
	s.log.Info("signed out everywhere", "from", clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) authPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !readBody(w, r, &req) {
		return
	}
	token := ""
	if c, err := r.Cookie(s.cookieName()); err == nil {
		token = c.Value
	}
	if err := s.auth.ChangePassword(r.Context(), clientIP(r), token, req.Current, req.New); err != nil {
		authError(w, err)
		return
	}
	s.log.Info("owner password changed; other browsers were signed out", "from", clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- setup guide ----

func (s *Server) setupGuide(w http.ResponseWriter, r *http.Request) {
	settings := s.mgr.Settings()
	zone := time.Local.String()
	if zone == "Local" {
		zone, _ = time.Now().Zone()
	}
	utc := zone == "UTC" || zone == "Etc/UTC" || zone == "GMT"
	list := s.mgr.List()
	var backupsOff, neverBackedUp []string
	for _, st := range list {
		srv, ok := s.mgr.Get(st.ID)
		if !ok {
			continue
		}
		v := srv.Backups()
		if v.Plan.Every == "off" {
			backupsOff = append(backupsOff, st.Name)
		} else if len(v.Backups) == 0 {
			neverBackedUp = append(neverBackedUp, st.Name)
		}
	}
	importDir := s.mgr.ImportDir()
	toImport := 0
	if importDir != "" {
		if c, err := s.mgr.ScanImports(); err == nil {
			for _, x := range c {
				if !x.Imported {
					toImport++
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"setupDone":       settings.SetupDone,
		"ownerGamertag":   settings.OwnerGamertag,
		"listIP":          s.opts.HostIP,
		"listName":        s.opts.ListName,
		"timeZone":        zone,
		"timeZoneIsUTC":   utc,
		"servers":         len(list),
		"backupsOff":      nonNil(backupsOff),
		"neverBackedUp":   nonNil(neverBackedUp),
		"importMounted":   importDir != "",
		"importWaiting":   toImport,
		"panelURLExample": "https://" + hostOr(s.opts.HostIP) + ":" + strconv.Itoa(s.opts.Port),
	})
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (s *Server) setupGamertag(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Gamertag   string `json:"gamertag"`
		Everywhere bool   `json:"everywhere"`
	}
	if !readBody(w, r, &req) {
		return
	}
	if err := s.mgr.SetOwnerGamertag(req.Gamertag); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	added, skipped := []string{}, []string{}
	if req.Everywhere && req.Gamertag != "" {
		name := s.mgr.Settings().OwnerGamertag
		for _, st := range s.mgr.List() {
			srv, ok := s.mgr.Get(st.ID)
			if !ok {
				continue
			}
			if err := s.addOwner(srv, name); err != nil {
				skipped = append(skipped, st.Name+": "+err.Error())
				continue
			}
			added = append(added, st.Name)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "skipped": skipped})
}

// addOwner puts the owner on a server's allowlist and makes them an operator
// (now if the panel knows their Xbox ID, otherwise when they next join).
func (s *Server) addOwner(srv *servers.Server, name string) error {
	if err := srv.EditBlocked(); err != nil {
		return err
	}
	if err := srv.AllowlistAdd(name, ""); err != nil {
		return err
	}
	_, err := srv.OpAdd(name)
	return err
}

func (s *Server) setupDone(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Done bool `json:"done"`
	}
	if !readBody(w, r, &req) {
		return
	}
	if err := s.mgr.SetSetupDone(req.Done); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"setupDone": req.Done})
}
