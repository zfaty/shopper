package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// client is a tiny cookie-carrying test client over a Server.
type client struct {
	t      *testing.T
	s      *Server
	cookie *http.Cookie
}

func (c *client) do(method, url, body string, out any) int {
	c.t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, url, nil)
	} else {
		req = httptest.NewRequest(method, url, strings.NewReader(body))
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	c.s.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == sessionCookie {
			c.cookie = ck
		}
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			c.t.Fatalf("%s %s: decode %q: %v", method, url, rec.Body.String(), err)
		}
	}
	return rec.Code
}

func TestSessionCookie(t *testing.T) {
	s := newTestServer(t)
	c := &client{t: t, s: s}

	var cart cartView
	if code := c.do("GET", "/cart", "", &cart); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if c.cookie == nil || !c.cookie.HttpOnly {
		t.Fatalf("expected an HttpOnly session cookie, got %+v", c.cookie)
	}
	if len(cart.Lines) != 0 || cart.City != defaultCity {
		t.Fatalf("new cart %+v", cart)
	}

	// Same cookie: no new cookie issued.
	first := c.cookie.Value
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/cart", nil)
	req.AddCookie(c.cookie)
	s.ServeHTTP(rec, req)
	if len(rec.Result().Cookies()) != 0 {
		t.Error("known session should not get a new cookie")
	}

	// Unknown cookie (e.g. after restart): a fresh session, not an error.
	c.cookie = &http.Cookie{Name: sessionCookie, Value: "forged-or-stale"}
	if code := c.do("GET", "/cart", "", nil); code != http.StatusOK {
		t.Fatalf("stale cookie: status %d", code)
	}
	if c.cookie.Value == "forged-or-stale" || c.cookie.Value == first {
		t.Errorf("stale cookie should be replaced with a new session, got %q", c.cookie.Value)
	}

	// Catalogue routes stay sessionless.
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/products/PAIN-001", nil))
	if len(rec.Result().Cookies()) != 0 {
		t.Error("catalogue routes should not create sessions")
	}
}

func TestCartFlow(t *testing.T) {
	s := newTestServer(t)
	c := &client{t: t, s: s}

	var cart cartView
	if code := c.do("POST", "/cart/items", `{"sku":"pain-001","qty":2}`, &cart); code != http.StatusOK {
		t.Fatalf("add: status %d", code)
	}
	if len(cart.Lines) != 1 || cart.Lines[0].SKU != "PAIN-001" || cart.Lines[0].UnitPrice != 4.95 ||
		cart.Subtotal != 9.9 || cart.ItemCount != 2 || cart.Currency != "SAR" {
		t.Fatalf("after add %+v", cart)
	}

	// qty defaults to 1 and adds to the existing line.
	c.do("POST", "/cart/items", `{"sku":"PAIN-001"}`, &cart)
	if cart.Lines[0].Qty != 3 {
		t.Fatalf("default qty: %+v", cart.Lines)
	}

	c.do("PATCH", "/cart/items/PAIN-001", `{"qty":5}`, &cart)
	if cart.Lines[0].Qty != 5 || cart.Subtotal != 24.75 {
		t.Fatalf("after update %+v", cart)
	}

	// Arabic names come from the session locale, set per request.
	c.do("GET", "/cart?lang=ar", "", &cart)
	if cart.Lines[0].Name == "" || cart.Lines[0].Name[0] < 0x80 {
		t.Errorf("lang=ar should give an Arabic name, got %q", cart.Lines[0].Name)
	}

	c.do("PATCH", "/cart/items/PAIN-001", `{"qty":0}`, &cart)
	if len(cart.Lines) != 0 || cart.Subtotal != 0 {
		t.Fatalf("qty 0 should remove: %+v", cart)
	}

	c.do("POST", "/cart/items", `{"sku":"PAIN-001"}`, nil)
	if code := c.do("DELETE", "/cart/items/PAIN-001", "", &cart); code != http.StatusOK || len(cart.Lines) != 0 {
		t.Fatalf("delete: status %d cart %+v", code, cart)
	}
}

func TestCartErrors(t *testing.T) {
	s := newTestServer(t)
	c := &client{t: t, s: s}

	var e errorBody
	for _, tc := range []struct {
		method, url, body string
		status            int
		code              string
	}{
		{"POST", "/cart/items", `{"sku":"RX-001"}`, http.StatusForbidden, "prescription_required"},
		{"POST", "/cart/items", `{"sku":"COLD-005"}`, http.StatusNotFound, "not_found"},
		{"POST", "/cart/items", `{"sku":"PAIN-001","qty":0}`, http.StatusBadRequest, "invalid_qty"},
		{"POST", "/cart/items", `{"sku":"PAIN-001","qty":11}`, http.StatusBadRequest, "invalid_qty"},
		{"POST", "/cart/items", `{"sku":"PAIN-001","price":0.01}`, http.StatusBadRequest, "bad_request"},
		{"POST", "/cart/items", `{"sku":"PAIN-001","cart_id":"someone-else"}`, http.StatusBadRequest, "bad_request"},
		{"POST", "/cart/items", `{}`, http.StatusBadRequest, "bad_request"},
		{"POST", "/cart/items", `not json`, http.StatusBadRequest, "bad_request"},
		{"PATCH", "/cart/items/PAIN-001", `{}`, http.StatusBadRequest, "bad_request"},
		{"DELETE", "/cart/items/PAIN-002", "", http.StatusNotFound, "not_in_cart"},
		{"PATCH", "/session", `{"city":"cairo"}`, http.StatusBadRequest, "invalid_city"},
	} {
		e = errorBody{}
		code := c.do(tc.method, tc.url, tc.body, &e)
		if code != tc.status || e.Code != tc.code {
			t.Errorf("%s %s %s: got %d %q, want %d %q", tc.method, tc.url, tc.body, code, e.Code, tc.status, tc.code)
		}
	}
}

func TestCartsAreIsolatedAndFollowCity(t *testing.T) {
	s := newTestServer(t)
	alice := &client{t: t, s: s}
	bob := &client{t: t, s: s}

	alice.do("POST", "/cart/items", `{"sku":"PAIN-001"}`, nil)

	var cart cartView
	bob.do("GET", "/cart", "", &cart)
	if len(cart.Lines) != 0 {
		t.Fatalf("bob sees alice's cart: %+v", cart)
	}

	var sv sessionView
	if code := alice.do("PATCH", "/session", `{"city":"Jeddah"}`, &sv); code != http.StatusOK || sv.City != "jeddah" {
		t.Fatalf("patch session: %d %+v", code, sv)
	}
	alice.do("GET", "/cart", "", &cart)
	if cart.City != "jeddah" || len(cart.Lines) != 1 {
		t.Fatalf("alice cart after city change %+v", cart)
	}
	bob.do("GET", "/session", "", &sv)
	if sv.City != defaultCity {
		t.Errorf("bob's city changed to %q", sv.City)
	}
}
