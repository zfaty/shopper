package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"shopper/internal/agent"
	"shopper/internal/commerce"
)

type fakeChatter struct {
	carts []string // CartID seen on each call
	err   error
	reset int
}

func (f *fakeChatter) Chat(ctx context.Context, text string) (string, error) {
	s, err := commerce.SessionFrom(ctx)
	if err != nil {
		return "", err
	}
	f.carts = append(f.carts, s.CartID)
	if f.err != nil {
		return "", f.err
	}
	if text == "" {
		return "", agent.ErrEmptyMessage
	}
	return "echo: " + text, nil
}

func (f *fakeChatter) Reset(context.Context) error { f.reset++; return nil }

func TestChatEndpoint(t *testing.T) {
	s := newTestServer(t)
	c := &client{t: t, s: s}

	var e errorBody
	if code := c.do("POST", "/chat", `{"message":"hi"}`, &e); code != http.StatusServiceUnavailable || e.Code != "agent_unavailable" {
		t.Fatalf("no chatter: %d %+v", code, e)
	}

	fc := &fakeChatter{}
	s.SetChatter(fc)

	c.do("POST", "/cart/items", `{"sku":"PAIN-001"}`, nil)
	var res chatResponse
	if code := c.do("POST", "/chat", `{"message":"  hello  "}`, &res); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if res.Reply != "echo: hello" || len(res.Cart.Lines) != 1 {
		t.Fatalf("response %+v", res)
	}

	// Same cookie, same session; another client gets another one.
	c.do("POST", "/chat", `{"message":"again"}`, nil)
	(&client{t: t, s: s}).do("POST", "/chat", `{"message":"other"}`, nil)
	if fc.carts[0] != fc.carts[1] || fc.carts[0] == fc.carts[2] {
		t.Errorf("sessions %v", fc.carts)
	}

	for _, tc := range []struct {
		body   string
		status int
		code   string
	}{
		{`{"message":"   "}`, http.StatusBadRequest, "bad_request"},
		{`{"message":"hi","city":"jeddah"}`, http.StatusBadRequest, "bad_request"},
	} {
		e = errorBody{}
		if code := c.do("POST", "/chat", tc.body, &e); code != tc.status || e.Code != tc.code {
			t.Errorf("%s: %d %q", tc.body, code, e.Code)
		}
	}

	fc.err = errors.New("upstream down")
	if code := c.do("POST", "/chat", `{"message":"hi"}`, &e); code != http.StatusBadGateway || e.Code != "agent_error" {
		t.Errorf("upstream error: %d %+v", code, e)
	}

	if code := c.do("DELETE", "/chat", "", nil); code != http.StatusNoContent || fc.reset != 1 {
		t.Errorf("reset: %d, resets %d", code, fc.reset)
	}
}
