package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// handleEvents is the server-sent event stream: presence, invites and duo
// traffic. One stream per browser tab.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, errors.New("streaming unsupported"))
		return
	}
	u := userOf(r)
	c := s.hub.connect(u)
	defer s.hub.disconnect(c)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)

	// Tell this tab about a duo it is already in (e.g. after a reload).
	if sess, partner, st, ok := s.hub.currentSession(u); ok {
		data := map[string]any{"session": sess.ID, "slug": sess.Slug, "title": sess.Title, "partner": partner}
		if st != nil {
			data["partnerState"] = st
		}
		fmt.Fprintf(w, "event: duo-start\ndata: %s\n\n", event{Data: data}.encode())
	}
	flusher.Flush()

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev := <-c.ch:
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, ev.encode())
			flusher.Flush()
		}
	}
}

func (s *Server) handlePresence(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Slug string `json:"slug"`
		Lang string `json:"lang"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.hub.setPresence(userOf(r), body.Slug, s.titleOf(body.Slug), body.Lang)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// titleOf looks a problem title up in the cached list.
func (s *Server) titleOf(slug string) string {
	if slug == "" {
		return ""
	}
	problems, err := s.problemList(context.Background())
	if err != nil {
		return slug
	}
	for _, p := range problems {
		if p.TitleSlug == slug {
			return p.FrontendID + ". " + p.Title
		}
	}
	return slug
}

func (s *Server) handleOnline(w http.ResponseWriter, r *http.Request) {
	s.hub.mu.Lock()
	users := s.hub.presenceLocked()
	s.hub.mu.Unlock()
	writeJSON(w, 200, map[string]any{"users": users})
}

func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	var body struct {
		To   int64  `json:"to"`
		Slug string `json:"slug"`
	}
	if err := readJSON(w, r, &body); err != nil || body.Slug == "" {
		writeErr(w, 400, errors.New("to and slug required"))
		return
	}
	to, ok := s.hub.userByID(body.To)
	if !ok {
		writeErr(w, 404, errors.New("that person is not online"))
		return
	}
	inv, err := s.hub.invite(userOf(r), to, body.Slug, s.titleOf(body.Slug))
	if err != nil {
		writeErr(w, 409, err)
		return
	}
	writeJSON(w, 200, inv)
}

func (s *Server) handleRespond(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID     int64 `json:"id"`
		Accept bool  `json:"accept"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	sess, err := s.hub.respond(userOf(r), body.ID, body.Accept)
	if err != nil {
		writeErr(w, 409, err)
		return
	}
	if sess == nil {
		writeJSON(w, 200, map[string]any{"declined": true})
		return
	}
	writeJSON(w, 200, map[string]any{"session": sess.ID, "slug": sess.Slug})
}

func (s *Server) handleDuoUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Session int64  `json:"session"`
		Code    string `json:"code"`
		Cursor  int    `json:"cursor"`
		Lang    string `json:"lang"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.hub.update(userOf(r), body.Session, duoState{Code: body.Code, Cursor: body.Cursor, Lang: body.Lang}); err != nil {
		writeErr(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleDuoLeave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Session int64 `json:"session"`
	}
	_ = readJSON(w, r, &body)
	s.hub.leave(userOf(r), body.Session)
	writeJSON(w, 200, map[string]any{"ok": true})
}
