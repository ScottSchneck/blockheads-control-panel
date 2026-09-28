// Package web serves the panel's web page and its API.
//
// This is the first, bare-bones page: a list of servers with start, stop,
// update and a live console. Sign-in is a single panel password for now; user
// accounts and roles come later in phase 1.
package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/base32"
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

	"github.com/ScottSchneck/blockheads-control-panel/internal/servers"
)

//go:embed static
var static embed.FS

// Options configures the web server.
type Options struct {
	Port     int    // 8443 by default
	TLS      bool   // serve HTTPS with a self-signed certificate
	Password string // from PANEL_PASSWORD; generated and saved if empty
	DataDir  string
	HostIP   string // the container's LAN IP, added to the certificate
	Version  string
}

// Server is the panel's web server.
type Server struct {
	opts     Options
	mgr      *servers.Manager
	password string
	log      *slog.Logger
}

// New prepares the web server, loading or creating the panel password.
func New(opts Options, mgr *servers.Manager) (*Server, error) {
	if opts.Port == 0 {
		opts.Port = 8443
	}
	s := &Server{opts: opts, mgr: mgr, log: slog.Default().With("src", "web")}
	pw, err := s.loadPassword()
	if err != nil {
		return nil, err
	}
	s.password = pw
	return s, nil
}

func (s *Server) loadPassword() (string, error) {
	if s.opts.Password != "" {
		return s.opts.Password, nil
	}
	path := filepath.Join(s.opts.DataDir, "panel-password")
	if b, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(b)) != "" {
		pw := strings.TrimSpace(string(b))
		s.log.Info("panel password is in the data folder", "file", path)
		return pw, nil
	}
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	pw := strings.ToLower(base32.StdEncoding.EncodeToString(buf)) // 16 letters and digits
	if err := os.WriteFile(path, []byte(pw+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("couldn't save the panel password: %w", err)
	}
	s.log.Warn("created a panel password; sign in with any username and this password (set PANEL_PASSWORD to choose your own)", "password", pw, "savedIn", path)
	return pw, nil
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
	mux.Handle("GET /", http.FileServerFS(sub))
	mux.HandleFunc("GET /api/info", s.info)
	mux.HandleFunc("GET /api/servers", s.listServers)
	mux.HandleFunc("POST /api/servers", s.createServer)
	mux.HandleFunc("POST /api/servers/{id}/{action}", s.serverAction)
	mux.HandleFunc("GET /api/servers/{id}/console", s.console)
	s.playerRoutes(mux)
	return s.secure(mux)
}

// secure checks the panel password and blocks cross-site requests.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'")
		_, pw, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(pw), []byte(s.password)) != 1 {
			if ok {
				s.log.Warn("wrong panel password", "from", r.RemoteAddr)
				time.Sleep(time.Second) // slow down guessing
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="Blockheads Control Panel", charset="UTF-8"`)
			http.Error(w, "Sign in with any username and the panel password.", http.StatusUnauthorized)
			return
		}
		// A custom header can't be sent cross-site without the browser asking
		// first, and we never allow that, so other websites can't press
		// buttons with the saved password.
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Blockheads") != "1" {
			http.Error(w, "missing X-Blockheads header", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
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
