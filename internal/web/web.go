// Package web serves the panel's web page and its API.
//
// Sign-in is the owner account from package auth, with a session cookie.
// The page itself (HTML, script, styles) is public; every API call except
// sign-in needs a session.
package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ScottSchneck/blockheads-control-panel/internal/auth"
	"github.com/ScottSchneck/blockheads-control-panel/internal/servers"
)

//go:embed static
var static embed.FS

// Options configures the web server.
type Options struct {
	Port     int    // 8443 by default
	TLS      bool   // serve HTTPS with a self-signed certificate
	Password string // PANEL_PASSWORD from before accounts; accepted as the setup code
	DataDir  string
	HostIP   string // the container's LAN IP, added to the certificate
	ListName string // what consoles show under LAN Games
	Version  string
}

// Server is the panel's web server.
type Server struct {
	opts Options
	mgr  *servers.Manager
	auth *auth.Store
	log  *slog.Logger
}

// New prepares the web server and the owner account store.
func New(opts Options, mgr *servers.Manager) (*Server, error) {
	if opts.Port == 0 {
		opts.Port = 8443
	}
	s := &Server{opts: opts, mgr: mgr, log: slog.Default().With("src", "web")}
	// Before accounts, the panel had one shared password. Until the owner
	// account exists it works as the setup code.
	legacy := opts.Password
	if legacy == "" {
		if b, err := os.ReadFile(filepath.Join(opts.DataDir, "panel-password")); err == nil {
			legacy = strings.TrimSpace(string(b))
		}
	}
	store, err := auth.Open(opts.DataDir, legacy)
	if err != nil {
		return nil, err
	}
	s.auth = store
	code, usesOld, err := store.EnsureSetupCode()
	if err != nil {
		return nil, fmt.Errorf("couldn't make a setup code: %w", err)
	}
	url := fmt.Sprintf("https://%s:%d", hostOr(opts.HostIP), opts.Port)
	if !opts.TLS {
		url = fmt.Sprintf("http://%s:%d", hostOr(opts.HostIP), opts.Port)
	}
	switch {
	case usesOld:
		s.log.Warn("open the panel to create the owner account; the setup code is your old panel password", "url", url)
	case code != "":
		s.log.Warn("open the panel to create the owner account", "url", url, "setup code", code)
	case store.ResetPending():
		s.log.Warn("a password reset is waiting; open the panel and choose \"Forgot your password?\"", "url", url)
	}
	return s, nil
}

// Run serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", s.opts.Port),
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	scheme := "http"
	var err error
	if s.opts.TLS {
		scheme = "https"
		cert, cerr := s.certificate()
		if cerr != nil {
			return cerr
		}
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		s.log.Info("web panel ready", "url", fmt.Sprintf("%s://%s:%d", scheme, hostOr(s.opts.HostIP), s.opts.Port))
		err = srv.ListenAndServeTLS("", "")
	} else {
		s.log.Info("web panel ready", "url", fmt.Sprintf("%s://%s:%d", scheme, hostOr(s.opts.HostIP), s.opts.Port))
		err = srv.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func hostOr(ip string) string {
	if ip == "" {
		return "<this container's IP>"
	}
	return ip
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	files := http.FileServerFS(sub)
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The page is tiny and changes with every update, so browsers must
		// check for a new copy each time rather than show a stale one.
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /api/info", s.info)
	mux.HandleFunc("GET /api/servers", s.listServers)
	mux.HandleFunc("POST /api/servers", s.createServer)
	mux.HandleFunc("POST /api/servers/{id}/{action}", s.serverAction)
	mux.HandleFunc("GET /api/servers/{id}/console", s.console)
	s.playerRoutes(mux)
	s.settingsRoutes(mux)
	s.authRoutes(mux)
	s.importRoutes(mux)
	s.backupRoutes(mux)
	return s.secure(mux)
}

// cookieName is the session cookie's name. Over HTTPS the __Host- prefix
// makes browsers refuse it unless it's secure and for this whole site, so
// nothing else on the same host can set or shadow it.
func (s *Server) cookieName() string {
	if s.opts.TLS {
		return "__Host-bh_session"
	}
	return "bh_session"
}

// publicAPI can be used without signing in.
var publicAPI = map[string]bool{
	"/api/auth/state": true, "/api/auth/setup": true, "/api/auth/login": true, "/api/auth/reset": true,
}

// secure checks the session and blocks cross-site requests.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'")
		// A custom header can't be sent cross-site without the browser asking
		// first, and we never allow that, so other websites can't press
		// buttons (or sign you in to their account) through your browser.
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Blockheads") != "1" {
			http.Error(w, "missing X-Blockheads header", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && !publicAPI[r.URL.Path] {
			if s.user(r) == "" {
				writeError(w, http.StatusUnauthorized, errors.New("sign in first"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// user returns who's signed in on this request ("" for nobody).
func (s *Server) user(r *http.Request) string {
	c, err := r.Cookie(s.cookieName())
	if err != nil {
		return ""
	}
	return s.auth.Check(c.Value)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": s.opts.Version, "hostIP": s.opts.HostIP})
}

func (s *Server) listServers(w http.ResponseWriter, r *http.Request) {
	list := s.mgr.List()
	if list == nil {
		list = []servers.Status{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createServer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`
		Preview    bool   `json:"preview"`
		AcceptEULA bool   `json:"acceptEula"`
		Owner      string `json:"ownerGamertag"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("bad request"))
		return
	}
	if !req.AcceptEULA {
		writeError(w, http.StatusBadRequest, errors.New("accept the Minecraft EULA to download the server"))
		return
	}
	if owner := strings.TrimSpace(req.Owner); owner != "" && owner != s.mgr.Settings().OwnerGamertag {
		if err := s.mgr.SetOwnerGamertag(req.Owner); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	st, err := s.mgr.Create(req.Name, req.Preview)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.log.Info("server created from the panel", "id", st.ID, "name", st.Name, "from", r.RemoteAddr)
	writeJSON(w, http.StatusAccepted, st)
}

func (s *Server) serverAction(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.mgr.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("no such server"))
		return
	}
	if srv.Importing() {
		writeError(w, http.StatusConflict, servers.ErrImporting)
		return
	}
	var err error
	switch action := r.PathValue("action"); action {
	case "start":
		err = srv.Start()
	case "stop":
		err = srv.Stop()
	case "restart":
		go func() { _ = srv.Restart() }()
	case "update":
		err = srv.Update()
	case "command":
		var req struct {
			Command string `json:"command"`
		}
		if jerr := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); jerr != nil {
			writeError(w, http.StatusBadRequest, errors.New("bad request"))
			return
		}
		err = srv.Command(req.Command)
	default:
		writeError(w, http.StatusNotFound, errors.New("unknown action"))
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, srv.Status())
}

// console streams a server's console as server-sent events: everything so
// far, then new lines as they happen.
func (s *Server) console(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.mgr.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("no such server"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming not supported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	backlog, lines, stop := srv.Subscribe()
	defer stop()
	send := func(line string) {
		fmt.Fprintf(w, "data: %s\n\n", strings.NewReplacer("\r", " ", "\n", " ").Replace(line))
	}
	for _, l := range backlog {
		send(l)
	}
	flusher.Flush()
	keepAlive := time.NewTicker(20 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case l := <-lines:
			send(l)
			flusher.Flush()
		case <-keepAlive.C:
			if s.user(r) == "" {
				return // signed out meanwhile
			}
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

// certificate loads or creates the panel's self-signed certificate.
func (s *Server) certificate() (tls.Certificate, error) {
	certPath := filepath.Join(s.opts.DataDir, "panel-cert.pem")
	keyPath := filepath.Join(s.opts.DataDir, "panel-key.pem")
	if c, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return c, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Blockheads Control Panel"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost", "blockheads"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	if ip := net.ParseIP(s.opts.HostIP); ip != nil {
		tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	s.log.Info("created a self-signed certificate for the web panel; your browser will ask you to trust it once")
	return tls.X509KeyPair(certPEM, keyPEM)
}
