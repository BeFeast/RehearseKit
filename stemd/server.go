// Package main implements stemd, a minimal HTTP server that serves RehearseKit
// stem files with full HTTP Range support. It exists as an architecture probe
// for the streaming playback lab: the browser pulls WAV byte ranges into an
// AudioWorklet ring buffer instead of decoding whole files.
//
// Layout on disk mirrors the FastAPI backend's LOCAL_STORAGE_PATH:
//
//	<root>/stems/<job-uuid>/<stem>.wav
//
// Routes:
//
//	GET  /healthz                    -> {"status":"ok"}
//	GET  /stems/{job}                -> JSON manifest of stems present for the job
//	GET  /stems/{job}/{stem}         -> the WAV file (200 or 206, Accept-Ranges: bytes)
//	HEAD /stems/{job}/{stem}         -> headers only
//	OPTIONS *                        -> CORS preflight
package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	jobIDPattern  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	stemIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

// Config holds the runtime configuration of the server.
type Config struct {
	// Root is the storage root; stems live under Root/stems/<job>/<stem>.wav.
	Root string
	// AllowedOrigins lists CORS origins. "*" allows any origin.
	AllowedOrigins []string
}

// Server serves stems over HTTP.
type Server struct {
	cfg    Config
	mux    *http.ServeMux
	logger *log.Logger
}

// StemInfo describes one stem file in a manifest.
type StemInfo struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	URL   string `json:"url"`
}

// Manifest is the JSON body of GET /stems/{job}.
type Manifest struct {
	JobID string     `json:"job_id"`
	Stems []StemInfo `json:"stems"`
}

// NewServer builds a Server with all routes registered.
func NewServer(cfg Config, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.New(os.Stderr, "stemd ", log.LstdFlags)
	}
	s := &Server{cfg: cfg, mux: http.NewServeMux(), logger: logger}
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /stems/{job}", s.handleManifest)
	s.mux.HandleFunc("GET /stems/{job}/{stem}", s.handleStem)
	s.mux.HandleFunc("HEAD /stems/{job}/{stem}", s.handleStem)
	return s
}

// ServeHTTP applies CORS handling and dispatches to the mux.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.applyCORS(w, r)
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Range, If-Range, Content-Type")
		w.Header().Set("Access-Control-Max-Age", "3600")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) applyCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	h := w.Header()
	// Cross-Origin-Resource-Policy lets a COEP page (require-corp or
	// credentialless) embed or fetch these responses.
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	if origin == "" {
		return
	}
	allowed := ""
	for _, o := range s.cfg.AllowedOrigins {
		if o == "*" {
			allowed = "*"
			break
		}
		if strings.EqualFold(o, origin) {
			allowed = origin
			break
		}
	}
	if allowed == "" {
		return
	}
	h.Set("Access-Control-Allow-Origin", allowed)
	h.Add("Vary", "Origin")
	h.Set("Access-Control-Expose-Headers", "Content-Range, Accept-Ranges, Content-Length, Content-Type")
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) jobDir(job string) (string, bool) {
	if !jobIDPattern.MatchString(job) {
		return "", false
	}
	dir := filepath.Join(s.cfg.Root, "stems", strings.ToLower(job))
	root := filepath.Clean(s.cfg.Root)
	if !strings.HasPrefix(dir, root+string(filepath.Separator)) {
		return "", false
	}
	return dir, true
}

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	job := r.PathValue("job")
	dir, ok := s.jobDir(job)
	if !ok {
		http.Error(w, "invalid job id", http.StatusBadRequest)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}
		s.logger.Printf("manifest %s: %v", job, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	m := Manifest{JobID: strings.ToLower(job), Stems: []StemInfo{}}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".wav") {
			continue
		}
		stem := strings.TrimSuffix(name, ".wav")
		if !stemIDPattern.MatchString(stem) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		m.Stems = append(m.Stems, StemInfo{Name: stem, Bytes: info.Size(), URL: "/stems/" + m.JobID + "/" + stem})
	}
	sort.Slice(m.Stems, func(i, j int) bool { return m.Stems[i].Name < m.Stems[j].Name })
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleStem(w http.ResponseWriter, r *http.Request) {
	job := r.PathValue("job")
	stem := r.PathValue("stem")
	dir, ok := s.jobDir(job)
	if !ok {
		http.Error(w, "invalid job id", http.StatusBadRequest)
		return
	}
	if !stemIDPattern.MatchString(stem) {
		http.Error(w, "invalid stem name", http.StatusBadRequest)
		return
	}
	path := filepath.Join(dir, stem+".wav")
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.Error(w, "stem not found", http.StatusNotFound)
			return
		}
		s.logger.Printf("open %s: %v", path, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.Error(w, "stem not found", http.StatusNotFound)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "audio/wav")
	h.Set("Content-Disposition", `inline; filename="`+stem+`.wav"`)
	h.Set("Cache-Control", "private, no-transform, max-age=3600")
	// http.ServeContent implements Range (single and multipart), If-Range,
	// HEAD, 416 and Accept-Ranges for us.
	http.ServeContent(w, r, stem+".wav", info.ModTime(), f)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
