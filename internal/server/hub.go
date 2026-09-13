package server

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/patrickspencer/algo-lab-public/internal/store"
)

// The hub tracks who is connected (via the /api/events stream), which
// problem they are looking at, pending duo invites and live duo sessions,
// and fans events out to browsers.

type event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// conn is one open event stream (one browser tab).
type conn struct {
	id   int64
	user store.User
	ch   chan event
}

type presence struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
	Lang  string `json:"lang"`
	Seen  time.Time
}

type invite struct {
	ID      int64      `json:"id"`
	From    store.User `json:"from"`
	To      store.User `json:"to"`
	Slug    string     `json:"slug"`
	Title   string     `json:"title"`
	Created time.Time  `json:"created"`
}

// duoState is what one participant last sent.
type duoState struct {
	Code   string `json:"code"`
	Cursor int    `json:"cursor"` // 1-based line
	Lang   string `json:"lang"`
}

type duoSession struct {
	ID    int64
	Slug  string
	Title string
	Users [2]store.User
	State map[int64]*duoState
}

type hub struct {
	mu       sync.Mutex
	conns    map[int64]*conn
	nextConn int64
	where    map[int64]*presence // by user id
	invites  map[int64]*invite
	nextInv  int64
	sessions map[int64]*duoSession
	nextSess int64
	inDuo    map[int64]int64 // user id -> session id
}

func newHub() *hub {
	return &hub{
		conns:    map[int64]*conn{},
		where:    map[int64]*presence{},
		invites:  map[int64]*invite{},
		sessions: map[int64]*duoSession{},
		inDuo:    map[int64]int64{},
	}
}

const (
	inviteTTL  = 2 * time.Minute
	connBuffer = 64
)

// ---------------------------------------------------------------------------
// Connections

func (h *hub) connect(u store.User) *conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextConn++
	c := &conn{id: h.nextConn, user: u, ch: make(chan event, connBuffer)}
	h.conns[c.id] = c
	if _, ok := h.where[u.ID]; !ok {
		h.where[u.ID] = &presence{Seen: time.Now()}
	}
	h.broadcastPresenceLocked()
	return c
}

// disconnect drops a connection; when a user's last tab closes they go
// offline and any duo session they were in ends.
func (h *hub) disconnect(c *conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.conns, c.id)
	if h.userOnlineLocked(c.user.ID) {
		h.broadcastPresenceLocked()
		return
	}
	delete(h.where, c.user.ID)
	if sid, ok := h.inDuo[c.user.ID]; ok {
		h.endSessionLocked(sid, c.user)
	}
	for id, inv := range h.invites {
		if inv.From.ID == c.user.ID || inv.To.ID == c.user.ID {
			delete(h.invites, id)
		}
	}
	h.broadcastPresenceLocked()
}

func (h *hub) userOnlineLocked(userID int64) bool {
	for _, c := range h.conns {
		if c.user.ID == userID {
			return true
		}
	}
	return false
}

func (h *hub) sendToUserLocked(userID int64, ev event) {
	for _, c := range h.conns {
		if c.user.ID != userID {
			continue
		}
		select {
		case c.ch <- ev:
		default: // slow tab; drop rather than block the hub
		}
	}
}

func (h *hub) broadcastLocked(ev event) {
	for _, c := range h.conns {
		select {
		case c.ch <- ev:
		default:
		}
	}
}

// ---------------------------------------------------------------------------
// Presence

type presenceEntry struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Title string `json:"title"`
	Lang  string `json:"lang"`
	Duo   int64  `json:"duo"` // session id, 0 if none
}

func (h *hub) presenceLocked() []presenceEntry {
	seen := map[int64]bool{}
	var out []presenceEntry
	for _, c := range h.conns {
		if seen[c.user.ID] {
			continue
		}
		seen[c.user.ID] = true
		e := presenceEntry{ID: c.user.ID, Name: c.user.Name, Duo: h.inDuo[c.user.ID]}
		if p := h.where[c.user.ID]; p != nil {
			e.Slug, e.Title, e.Lang = p.Slug, p.Title, p.Lang
		}
		out = append(out, e)
	}
	if out == nil {
		out = []presenceEntry{}
	}
	return out
}

func (h *hub) broadcastPresenceLocked() {
	h.broadcastLocked(event{Type: "presence", Data: map[string]any{"users": h.presenceLocked()}})
}

func (h *hub) setPresence(u store.User, slug, title, lang string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.where[u.ID] = &presence{Slug: slug, Title: title, Lang: lang, Seen: time.Now()}
	h.broadcastPresenceLocked()
}

// ---------------------------------------------------------------------------
// Invites

type hubError string

func (e hubError) Error() string { return string(e) }

func (h *hub) invite(from, to store.User, slug, title string) (*invite, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if from.ID == to.ID {
		return nil, hubError("you cannot invite yourself")
	}
	if !h.userOnlineLocked(to.ID) {
		return nil, hubError("that person is not online")
	}
	if _, busy := h.inDuo[to.ID]; busy {
		return nil, hubError("that person is already in a duo")
	}
	if _, busy := h.inDuo[from.ID]; busy {
		return nil, hubError("leave your current duo first")
	}
	h.expireInvitesLocked()
	for _, inv := range h.invites {
		if inv.From.ID == from.ID && inv.To.ID == to.ID {
			return inv, nil // already pending
		}
	}
	h.nextInv++
	inv := &invite{ID: h.nextInv, From: from, To: to, Slug: slug, Title: title, Created: time.Now()}
	h.invites[inv.ID] = inv
	h.sendToUserLocked(to.ID, event{Type: "invite", Data: inv})
	return inv, nil
}

func (h *hub) expireInvitesLocked() {
	for id, inv := range h.invites {
		if time.Since(inv.Created) > inviteTTL {
			delete(h.invites, id)
		}
	}
}

// respond accepts or declines an invite; accepting starts a session.
func (h *hub) respond(u store.User, inviteID int64, accept bool) (*duoSession, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expireInvitesLocked()
	inv, ok := h.invites[inviteID]
	if !ok || inv.To.ID != u.ID {
		return nil, hubError("that invite is no longer valid")
	}
	delete(h.invites, inviteID)
	if !accept {
		h.sendToUserLocked(inv.From.ID, event{Type: "invite-declined", Data: map[string]any{"id": inv.ID, "by": u}})
		return nil, nil
	}
	if !h.userOnlineLocked(inv.From.ID) {
		return nil, hubError("the inviter went offline")
	}
	h.nextSess++
	sess := &duoSession{ID: h.nextSess, Slug: inv.Slug, Title: inv.Title, Users: [2]store.User{inv.From, inv.To}, State: map[int64]*duoState{}}
	h.sessions[sess.ID] = sess
	h.inDuo[inv.From.ID] = sess.ID
	h.inDuo[inv.To.ID] = sess.ID
	for i, participant := range sess.Users {
		partner := sess.Users[1-i]
		h.sendToUserLocked(participant.ID, event{Type: "duo-start", Data: map[string]any{
			"session": sess.ID, "slug": sess.Slug, "title": sess.Title, "partner": partner,
		}})
	}
	h.broadcastPresenceLocked()
	return sess, nil
}

// ---------------------------------------------------------------------------
// Sessions

func (h *hub) update(u store.User, sessionID int64, st duoState) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	sess, ok := h.sessions[sessionID]
	if !ok {
		return hubError("that duo has ended")
	}
	partner, ok := sess.partnerOf(u.ID)
	if !ok {
		return hubError("you are not in that duo")
	}
	cp := st
	sess.State[u.ID] = &cp
	h.sendToUserLocked(partner.ID, event{Type: "duo-update", Data: map[string]any{
		"session": sess.ID, "from": u, "code": st.Code, "cursor": st.Cursor, "lang": st.Lang,
	}})
	return nil
}

// partnerState returns what the other participant last sent, for a tab that
// joins (or reloads) mid-session.
func (h *hub) partnerState(u store.User, sessionID int64) (*duoSession, store.User, *duoState, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sess, ok := h.sessions[sessionID]
	if !ok {
		return nil, store.User{}, nil, false
	}
	partner, ok := sess.partnerOf(u.ID)
	if !ok {
		return nil, store.User{}, nil, false
	}
	return sess, partner, sess.State[partner.ID], true
}

func (h *hub) leave(u store.User, sessionID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if sid, ok := h.inDuo[u.ID]; ok && (sessionID == 0 || sid == sessionID) {
		h.endSessionLocked(sid, u)
		h.broadcastPresenceLocked()
	}
}

func (h *hub) endSessionLocked(sessionID int64, by store.User) {
	sess, ok := h.sessions[sessionID]
	if !ok {
		return
	}
	delete(h.sessions, sessionID)
	for _, u := range sess.Users {
		delete(h.inDuo, u.ID)
		h.sendToUserLocked(u.ID, event{Type: "duo-end", Data: map[string]any{"session": sess.ID, "by": by}})
	}
}

func (s *duoSession) partnerOf(userID int64) (store.User, bool) {
	switch userID {
	case s.Users[0].ID:
		return s.Users[1], true
	case s.Users[1].ID:
		return s.Users[0], true
	}
	return store.User{}, false
}

// currentSession reports the session a user is in, if any.
func (h *hub) currentSession(u store.User) (*duoSession, store.User, *duoState, bool) {
	h.mu.Lock()
	sid, ok := h.inDuo[u.ID]
	h.mu.Unlock()
	if !ok {
		return nil, store.User{}, nil, false
	}
	return h.partnerState(u, sid)
}

// userByID finds an online user.
func (h *hub) userByID(id int64) (store.User, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.conns {
		if c.user.ID == id {
			return c.user, true
		}
	}
	return store.User{}, false
}

func (ev event) encode() []byte {
	b, _ := json.Marshal(ev.Data)
	return b
}
