package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager/usage-service/internal/store"
)

type providerKeyNamesRequest struct {
	Items []store.ProviderKeyName `json:"items"`
}

func (s *Server) handleProviderKeyNames(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeIfConfigured(w, r) {
		return
	}

	path := strings.TrimRight(r.URL.Path, "/")
	const basePath = "/v0/management/provider-key-names"
	switch {
	case path == basePath && r.Method == http.MethodGet:
		names, err := s.store.LoadProviderKeyNames(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": names})
	case path == basePath && r.Method == http.MethodPut:
		var req providerKeyNamesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.Items == nil {
			writeError(w, http.StatusBadRequest, errors.New("provider key names are required"))
			return
		}
		if err := s.store.UpsertProviderKeyNames(r.Context(), req.Items); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		names, err := s.store.LoadProviderKeyNames(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": names})
	case strings.HasPrefix(path, basePath+"/") && r.Method == http.MethodDelete:
		keyHash := strings.TrimPrefix(path, basePath+"/")
		if err := s.store.DeleteProviderKeyName(r.Context(), keyHash); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		methodNotAllowed(w)
	}
}
