package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"shopper/internal/agent"
)

// Chatter is the shopping assistant (*agent.Agent). The session it acts for
// comes from the request context, set by withSession.
type Chatter interface {
	Chat(ctx context.Context, text string) (string, error)
	Reset(ctx context.Context) error
}

// SetChatter enables the /chat endpoints. Without it they return 503.
func (s *Server) SetChatter(c Chatter) { s.chat = c }

type chatResponse struct {
	Reply string   `json:"reply"`
	Cart  cartView `json:"cart"` // so the UI can refresh the cart after each turn
}

// POST /chat  {"message": "do you have paracetamol?"}
func (s *Server) postChat(w http.ResponseWriter, r *http.Request) {
	if s.chat == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "assistant is not configured", Code: "agent_unavailable"})
		return
	}
	var body struct {
		Message string `json:"message"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	reply, err := s.chat.Chat(r.Context(), strings.TrimSpace(body.Message))
	switch {
	case errors.Is(err, agent.ErrEmptyMessage), errors.Is(err, agent.ErrMessageTooLong):
		writeBadRequest(w, err.Error())
		return
	case errors.Is(err, context.Canceled):
		return // client went away
	case err != nil:
		slog.Error("chat", "err", err)
		writeJSON(w, http.StatusBadGateway, errorBody{Error: "the assistant is unavailable, please try again", Code: "agent_error"})
		return
	}
	c, err := s.store.GetCart(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, chatResponse{Reply: reply, Cart: toCartView(c)})
}

// DELETE /chat  starts a new conversation (the cart is kept).
func (s *Server) deleteChat(w http.ResponseWriter, r *http.Request) {
	if s.chat == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "assistant is not configured", Code: "agent_unavailable"})
		return
	}
	if err := s.chat.Reset(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
