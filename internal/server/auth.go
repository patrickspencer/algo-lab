package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/patrickspencer/algo-lab-public/internal/store"
)

// Sign-in is by name only: this is a tool for you and a friend on a machine
// or network you both trust, not a public service. The browser keeps a random
// token in a cookie; the token maps to the user in the database.

const sessionCookie = "algolab_session"

type ctxKey int

const userKey ctxKey = 1

// signupCode returns the code required to create new accounts: the
// ALGOLAB_SIGNUP_CODE env var, or the contents of
// ~/.config/algo-lab/signup_code.txt. Empty means registration is open.
func signupCode() string {
	if c := strings.TrimSpace(os.Getenv("ALGOLAB_SIGNUP_CODE")); c != "" {
		return c
	}
	if home, err := os.UserHomeDir(); err == nil {
		if b, err := os.ReadFile(filepath.Join(home, ".config", "algo-lab", "signup_code.txt")); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

func newToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// currentUser resolves the request's cookie to a user.
func (s *Server) currentUser(r *http.Request) (store.User, bool) {
	if u, ok := r.Context().Value(userKey).(store.User); ok {
		return u, true
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return store.User{}, false
	}
	u, ok, err := s.store.UserByToken(r.Context(), c.Value)
	if err != nil || !ok {
		return store.User{}, false
	}
	return u, true
}

// requireUser wraps a handler so it only runs for signed-in users.
func (s *Server) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.currentUser(r)
		if !ok {
			writeErr(w, 401, errors.New("sign in first"))
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	}
}

// userOf returns the signed-in user for handlers behind requireUser.
func userOf(r *http.Request) store.User {
	u, _ := r.Context().Value(userKey).(store.User)
	return u
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Password string `json:"password"`
		Code     string `json:"code"`
		Signup   bool   `json:"signup"`
	}
	if err := readJSON(w, r, &body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeErr(w, 400, errors.New("name required"))
		return
	}
	name := strings.TrimSpace(body.Name)
	if len(name) > 32 {
		name = name[:32]
	}

	u, hash, _, found, err := s.store.FindUser(r.Context(), name)
	if err != nil {
		writeErr(w, 500, err)
		return
	}

	if body.Signup {
		// Creating a new account.
		if found {
			writeErr(w, 409, errors.New("an account with that name already exists — sign in instead"))
			return
		}
		if os.Getenv("ALGOLAB_CLOSED") != "" {
			writeErr(w, 403, errors.New("new accounts are disabled on this server"))
			return
		}
		if code := signupCode(); code != "" && body.Code != code {
			writeErr(w, 403, errors.New("a valid signup code is required to create an account"))
			return
		}
		newHash := ""
		if body.Password != "" {
			b, herr := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
			if herr != nil {
				writeErr(w, 500, herr)
				return
			}
			newHash = string(b)
		}
		u, err = s.store.CreateUser(r.Context(), name, newHash, newToken())
		if err != nil {
			writeErr(w, 500, err)
			return
		}
	} else {
		// Signing in to an existing account.
		if !found {
			writeErr(w, 401, errors.New("no account with that name — create one first"))
			return
		}
		if hash != "" && bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Password)) != nil {
			writeErr(w, 401, errors.New("wrong password"))
			return
		}
	}

	token := newToken()
	if err := s.store.AddSession(r.Context(), u.ID, token); err != nil {
		writeErr(w, 500, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Expires: time.Now().Add(365 * 24 * time.Hour),
	})
	writeJSON(w, 200, map[string]any{"id": u.ID, "name": u.Name, "hasPassword": hash != ""})
}

// handleSetPassword lets a signed-in user set or change their own password.
func (s *Server) handleSetPassword(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := readJSON(w, r, &body); err != nil || len(body.New) < 4 {
		writeErr(w, 400, errors.New("new password must be at least 4 characters"))
		return
	}
	_, hash, _, _, err := s.store.FindUser(r.Context(), u.Name)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if hash != "" && bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Current)) != nil {
		writeErr(w, 403, errors.New("current password is wrong"))
		return
	}
	b, err := bcrypt.GenerateFromPassword([]byte(body.New), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if _, err := s.store.SetPassword(r.Context(), u.Name, string(b)); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Drop just this browser's session; other devices stay signed in.
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		_ = s.store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, ok := s.currentUser(r)
	if !ok {
		writeErr(w, 401, errors.New("sign in first"))
		return
	}
	_, hash, _, _, _ := s.store.FindUser(r.Context(), u.Name)
	writeJSON(w, 200, map[string]any{"id": u.ID, "name": u.Name, "hasPassword": hash != ""})
}
