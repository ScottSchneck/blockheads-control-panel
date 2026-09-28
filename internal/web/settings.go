package web

import (
	"net/http"

	"github.com/ScottSchneck/blockheads-control-panel/internal/servers"
)

func (s *Server) settingsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/servers/{id}/settings", s.withServer(s.getServerSettings))
	mux.HandleFunc("POST /api/servers/{id}/settings", s.withServer(s.saveServerSettings))
	mux.HandleFunc("GET /api/servers/{id}/properties", s.withServer(s.getProperties))
	mux.HandleFunc("PUT /api/servers/{id}/properties", s.withServer(s.putProperties))
	mux.HandleFunc("POST /api/servers/{id}/rename", s.withServer(s.renameServer))
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
	if err := srv.SaveRawProperties(req.Text); err != nil {
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
	if err := srv.Rename(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, srv.Status())
}
