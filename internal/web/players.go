package web

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ScottSchneck/blockheads-control-panel/internal/servers"
)

func (s *Server) playerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("POST /api/settings", s.saveSettings)
	mux.HandleFunc("GET /api/servers/{id}/players", s.withServer(s.players))
	mux.HandleFunc("POST /api/servers/{id}/allowlist", s.withServer(s.allowlistAdd))
	mux.HandleFunc("DELETE /api/servers/{id}/allowlist/{name}", s.withServer(s.allowlistRemove))
	mux.HandleFunc("POST /api/servers/{id}/allowlist-enabled", s.withServer(s.allowlistEnabled))
	mux.HandleFunc("POST /api/servers/{id}/operators", s.withServer(s.opAdd))
	mux.HandleFunc("DELETE /api/servers/{id}/operators/{name}", s.withServer(s.opRemove))
	mux.HandleFunc("POST /api/servers/{id}/kick", s.withServer(s.kick))
	mux.HandleFunc("POST /api/servers/{id}/message", s.withServer(s.message))
	mux.HandleFunc("DELETE /api/servers/{id}/attempts/{name}", s.withServer(s.dismissAttempt))
}

func (s *Server) withServer(h func(http.ResponseWriter, *http.Request, *servers.Server)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		srv, ok := s.mgr.Get(r.PathValue("id"))
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("no such server"))
			return
		}
		if r.Method != http.MethodGet {
			if err := srv.EditBlocked(); err != nil {
				writeError(w, http.StatusConflict, err)
				return
			}
		}
		h(w, r, srv)
	}
}

// readBody decodes a small JSON request body.
func readBody(w http.ResponseWriter, r *http.Request, v any) bool {
	return readBodyLimit(w, r, v, 16<<10)
}

// readBodyLimit decodes a JSON request body of up to limit bytes.
func readBodyLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("bad request"))
		return false
	}
	return true
}

func (s *Server) respondPlayers(w http.ResponseWriter, srv *servers.Server, err error, code int) {
	if err != nil {
		writeError(w, code, err)
		return
	}
	v, err := srv.Players()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mgr.Settings())
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	var req servers.Settings
	if !readBody(w, r, &req) {
		return
	}
	if err := s.mgr.SetOwnerGamertag(req.OwnerGamertag); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.mgr.Settings())
}

func (s *Server) players(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	s.respondPlayers(w, srv, nil, 0)
}

func (s *Server) allowlistAdd(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	var req struct {
		Name string `json:"name"`
	}
	if !readBody(w, r, &req) {
		return
	}
	s.respondPlayers(w, srv, srv.AllowlistAdd(req.Name, srv.AttemptXUID(req.Name)), http.StatusBadRequest)
}

func (s *Server) allowlistRemove(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	s.respondPlayers(w, srv, srv.AllowlistRemove(r.PathValue("name")), http.StatusBadRequest)
}

func (s *Server) allowlistEnabled(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !readBody(w, r, &req) {
		return
	}
	s.respondPlayers(w, srv, srv.SetAllowlistEnabled(req.Enabled), http.StatusInternalServerError)
}

func (s *Server) opAdd(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	var req struct {
		Name string `json:"name"`
	}
	if !readBody(w, r, &req) {
		return
	}
	_, err := srv.OpAdd(req.Name)
	s.respondPlayers(w, srv, err, http.StatusBadRequest)
}

func (s *Server) opRemove(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	s.respondPlayers(w, srv, srv.OpRemove(r.PathValue("name")), http.StatusBadRequest)
}

func (s *Server) kick(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	var req struct {
		Name   string `json:"name"`
		Reason string `json:"reason"`
	}
	if !readBody(w, r, &req) {
		return
	}
	s.respondPlayers(w, srv, srv.Kick(req.Name, req.Reason), http.StatusConflict)
}

func (s *Server) message(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	var req struct {
		Name string `json:"name"`
		Text string `json:"text"`
	}
	if !readBody(w, r, &req) {
		return
	}
	s.respondPlayers(w, srv, srv.Message(req.Name, req.Text), http.StatusConflict)
}

func (s *Server) dismissAttempt(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	srv.DismissAttempt(r.PathValue("name"))
	s.respondPlayers(w, srv, nil, 0)
}
