package web

import (
	"errors"
	"net/http"

	"github.com/ScottSchneck/blockheads-control-panel/internal/servers"
)

func (s *Server) importRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/import", s.listImports)
	mux.HandleFunc("POST /api/import", s.startImport)
}

func (s *Server) listImports(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"available":  true,
		"candidates": []servers.ImportCandidate{},
		"results":    s.mgr.ImportResults(),
	}
	list, err := s.mgr.ScanImports()
	switch {
	case errors.Is(err, servers.ErrNoImportDir):
		resp["available"] = false
		resp["message"] = err.Error()
	case err != nil:
		resp["message"] = "Some folders couldn't be read: " + err.Error()
	}
	if list != nil {
		resp["candidates"] = list
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) startImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path  string `json:"path"`
		Name  string `json:"name"`
		Port  int    `json:"port"`
		Start bool   `json:"start"`
	}
	if !readBody(w, r, &req) {
		return
	}
	st, err := s.mgr.Import(req.Path, req.Name, req.Port, req.Start)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}
