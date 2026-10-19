package api

import (
	"context"
	"crypto/rand"
	"net/http"
	"sync"

	"shopper/internal/commerce"
)

// Sessions are guest-only for the demo: the cookie holds an opaque random ID
// and everything else (cart ID, city) lives server-side, so a client can
// never pick whose cart it touches. Sessions are in memory and lost on
// restart, like the mock carts.

const (
	sessionCookie = "shopper_session"
	defaultCity   = commerce.CityRiyadh
)

type sessionStore struct {
	mu   sync.Mutex
	byID map[string]commerce.Session
}

func newSessionStore() *sessionStore {
	return &sessionStore{byID: map[string]commerce.Session{}}
}

// load returns the session for id, or a fresh one (with a new id) when id is
// empty or unknown, e.g. after a server restart.
func (st *sessionStore) load(id string) (string, commerce.Session) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if s, ok := st.byID[id]; ok && id != "" {
		return id, s
	}
	id = rand.Text()
	s := commerce.Session{CartID: "cart-" + rand.Text(), City: defaultCity, Locale: commerce.LocaleEN}
	st.byID[id] = s
	return id, s
}

func (st *sessionStore) setCity(id string, c commerce.City) commerce.Session {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := st.byID[id]
	s.City = c
	st.byID[id] = s
	return s
}

type sessionIDKey struct{}

// withSession loads or creates the caller's session from the cookie and puts
// it in the request context (commerce.WithSession). Handlers and, later, the
// agent read it with commerce.SessionFrom and nothing else.
func (s *Server) withSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var id string
		if c, err := r.Cookie(sessionCookie); err == nil {
			id = c.Value
		}
		newID, sess := s.sessions.load(id)
		if newID != id {
			http.SetCookie(w, &http.Cookie{
				Name: sessionCookie, Value: newID, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteLaxMode,
			})
		}
		sess.Locale = locale(r) // per request, so ?lang=ar works on cart reads
		ctx := commerce.WithSession(r.Context(), sess)
		ctx = context.WithValue(ctx, sessionIDKey{}, newID)
		next(w, r.WithContext(ctx))
	}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

type sessionView struct {
	City   commerce.City   `json:"city"`
	Locale commerce.Locale `json:"locale"`
}

// GET /session
func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	sess, err := commerce.SessionFrom(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionView{City: sess.City, Locale: sess.Locale})
}

// PATCH /session  {"city": "jeddah"}
// The delivery city is set by the customer through the UI, never by the model.
func (s *Server) patchSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		City string `json:"city"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	city, ok := commerce.ParseCity(body.City)
	if !ok {
		writeError(w, commerce.ErrInvalidCity)
		return
	}
	id, _ := r.Context().Value(sessionIDKey{}).(string)
	sess := s.sessions.setCity(id, city)
	writeJSON(w, http.StatusOK, sessionView{City: sess.City, Locale: locale(r)})
}
