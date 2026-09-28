package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/ScottSchneck/blockheads-control-panel/internal/servers"
)

func (s *Server) settingsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/servers/{id}/settings", s.withServer(levelRun, s.getServerSettings))
	mux.HandleFunc("POST /api/servers/{id}/settings", s.withServer(levelRun, s.saveServerSettings))
	mux.HandleFunc("GET /api/servers/{id}/properties", s.withServer(levelRun, s.getProperties))
	mux.HandleFunc("PUT /api/servers/{id}/properties", s.withServer(levelRun, s.putProperties))
	mux.HandleFunc("POST /api/servers/{id}/rename", s.withServer(levelRun, s.renameServer))
}

func (s *Server) getServerSettings(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	v, err := srv.Settings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) saveServerSettings(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	var req struct {
		Values map[string]string `json:"values"`
	}
	if !readBody(w, r, &req) {
		return
	}
	changes, err := srv.SaveSettings(req.Values)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(changes) > 0 {
		var what []string
		for _, c := range changes {
			what = append(what, c.Label)
		}
		s.note(r, srv, "changed settings: "+strings.Join(what, ", "))
	}
	v, err := srv.Settings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if changes == nil {
		changes = []servers.SettingsChange{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": changes, "settings": v})
}

func (s *Server) getProperties(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	text, err := srv.RawProperties()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

func (s *Server) putProperties(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	var req struct {
		Text string `json:"text"`
	}
	// The editor sends the whole file (JSON-escaped), so allow more than usual.
	if !readBodyLimit(w, r, &req, 1<<20) {
		return
	}
	if err := s.did(r, srv, srv.SaveRawProperties(req.Text), "edited server.properties"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.getProperties(w, r, srv)
}

func (s *Server) renameServer(w http.ResponseWriter, r *http.Request, srv *servers.Server) {
	var req struct {
		Name string `json:"name"`
	}
	if !readBody(w, r, &req) {
		return
	}
	old := srv.Status().Name
	if err := s.did(r, srv, srv.Rename(req.Name), "renamed the server from "+strconv.Quote(old)); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, srv.Status())
}
