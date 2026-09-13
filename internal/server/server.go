// Package server exposes the store and the AI backends over a small JSON API
// and serves the embedded frontend. The problem set is built entirely from the
// bundled dataset, so the server makes no external network calls.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/patrickspencer/algo-lab-public/internal/ai"
	"github.com/patrickspencer/algo-lab-public/internal/catalog"
	"github.com/patrickspencer/algo-lab-public/internal/dataset"
	"github.com/patrickspencer/algo-lab-public/internal/store"
)

// Server holds the dependencies for the handlers.
type Server struct {
	store  *store.Store
	ai     ai.Provider
	aiErr  error
	static fs.FS

	hub *hub

	mu       sync.Mutex
	problems []catalog.Problem
}

// New builds the server. static is the directory holding index.html etc.
func New(st *store.Store, static fs.FS) *Server {
	provider, aiErr := ai.Detect()
	return &Server{store: st, ai: provider, aiErr: aiErr, static: static, hub: newHub()}
}

// Handler returns the routed HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	auth := s.requireUser
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("POST /api/password", s.requireUser(s.handleSetPassword))
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/languages", s.handleLanguages)
	mux.HandleFunc("GET /api/problems", auth(s.handleProblems))
	mux.HandleFunc("GET /api/lists", auth(s.handleLists))
	mux.HandleFunc("GET /api/lists/{slug}", auth(s.handleList))
	mux.HandleFunc("GET /api/problems/{slug}", auth(s.handleProblem))
	mux.HandleFunc("PUT /api/problems/{slug}/draft", auth(s.handleDraft))
	mux.HandleFunc("POST /api/problems/{slug}/attempts", auth(s.handleSaveAttempt))
	mux.HandleFunc("DELETE /api/attempts/{id}", auth(s.handleDeleteAttempt))
	mux.HandleFunc("POST /api/problems/{slug}/progress", auth(s.handleSetProgress))
	mux.HandleFunc("POST /api/problems/{slug}/hint", auth(s.handleHint))
	mux.HandleFunc("POST /api/problems/{slug}/review", auth(s.handleReview))
	mux.HandleFunc("POST /api/run", auth(s.handleRun))
	mux.HandleFunc("GET /api/scratches", auth(s.handleScratches))
	mux.HandleFunc("POST /api/scratches", auth(s.handleSaveScratch))
	mux.HandleFunc("PUT /api/scratches/{id}", auth(s.handleSaveScratch))
	mux.HandleFunc("DELETE /api/scratches/{id}", auth(s.handleDeleteScratch))
	mux.HandleFunc("GET /api/settings", auth(s.handleGetSettings))
	mux.HandleFunc("PUT /api/settings", auth(s.handlePutSettings))
	mux.HandleFunc("GET /api/account", auth(s.handleAccount))
	mux.HandleFunc("GET /api/events", auth(s.handleEvents))
	mux.HandleFunc("POST /api/presence", auth(s.handlePresence))
	mux.HandleFunc("GET /api/online", auth(s.handleOnline))
	mux.HandleFunc("POST /api/duo/invite", auth(s.handleInvite))
	mux.HandleFunc("POST /api/duo/respond", auth(s.handleRespond))
	mux.HandleFunc("POST /api/duo/update", auth(s.handleDuoUpdate))
	mux.HandleFunc("POST /api/duo/leave", auth(s.handleDuoLeave))
	mux.Handle("GET /", http.FileServerFS(s.static))
	return logging(mux)
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}

// ---------------------------------------------------------------------------
// Helpers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	return dec.Decode(v)
}

// problemList returns the problem set, built from the embedded dataset.
func (s *Server) problemList(_ context.Context) ([]catalog.Problem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.problems == nil {
		s.problems = datasetProblems()
	}
	return s.problems, nil
}

func parseID(s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("bad id")
	}
	return id, nil
}

// datasetProblems builds the problem list from the embedded original dataset.
func datasetProblems() []catalog.Problem {
	out := make([]catalog.Problem, 0, dataset.Count())
	for _, p := range dataset.All() {
		tags := make([]catalog.TopicTag, 0, len(p.Tags))
		for _, t := range p.Tags {
			tags = append(tags, catalog.TopicTag{Name: t})
		}
		out = append(out, catalog.Problem{FrontendID: p.ID, Title: p.Title, TitleSlug: p.Slug, Difficulty: p.Difficulty, TopicTags: tags})
	}
	return out
}
