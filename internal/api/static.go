package api

// HTTP plumbing for the explorer API: JSON helpers (with ETag caching) and
// the static file handler for the built UI.

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		http.Error(w, `{"error":"encode failed"}`, http.StatusInternalServerError)
	}
}

// writeJSONCached: JSON with an ETag (FNV-64 of the body; 304 on
// If-None-Match) and Cache-Control max-age (0 = no-cache, always revalidate).
// Prepared days are immutable, so data responses carry a long max-age.
func writeJSONCached(w http.ResponseWriter, r *http.Request, maxAge int, data interface{}) {
	body, err := json.Marshal(data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encode failed")
		return
	}
	h := fnv.New64a()
	h.Write(body)
	etag := fmt.Sprintf(`"%x"`, h.Sum64())
	w.Header().Set("ETag", etag)
	if maxAge > 0 {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAge))
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
	w.Write([]byte("\n"))
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// Static serves the built UI: Vite assets under /assets/ carry a content
// hash in their name and are cached hard; every other path that is not an
// existing file falls back to index.html (SPA routing).
func Static(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			files.ServeHTTP(w, r)
			return
		}
		rel := strings.TrimPrefix(r.URL.Path, "/")
		if rel != "" && rel != "index.html" {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err == nil {
				w.Header().Set("Cache-Control", "no-cache")
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, filepath.Join(dir, "index.html"))
	})
}
