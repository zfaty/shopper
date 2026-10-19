package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"shopper/internal/commerce/mock"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := mock.LoadFile("../../data/products.json")
	if err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) }
	store.Now = now
	s := New(store)
	s.now = now
	return s
}

func get(t *testing.T, s *Server, url string, out any) int {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s: decode %q: %v", url, rec.Body.String(), err)
		}
	}
	return rec.Code
}

func TestSearchEndpoint(t *testing.T) {
	s := newTestServer(t)

	var res searchResponse
	if code := get(t, s, "/products/search?q=paracetamol&city=riyadh", &res); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if res.Total == 0 || res.Items[0].Currency != "SAR" {
		t.Fatalf("bad response %+v", res)
	}

	var ar searchResponse
	get(t, s, "/products/search?q=paracetamol&lang=ar", &ar)
	if ar.Items[0].Name == res.Items[0].Name {
		t.Error("lang=ar should return Arabic names")
	}

	for _, url := range []string{
		"/products/search?city=cairo",
		"/products/search?max_price=abc",
		"/products/search?page=x",
	} {
		if code := get(t, s, url, nil); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", url, code)
		}
	}
}

func TestProductEndpoints(t *testing.T) {
	s := newTestServer(t)

	var p productView
	if code := get(t, s, "/products/PAIN-001", &p); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if !p.OnSale || p.FinalPrice != 4.95 || p.Price != 6.5 {
		t.Fatalf("pricing %+v", p)
	}
	if code := get(t, s, "/products/COLD-005", nil); code != http.StatusNotFound {
		t.Errorf("disabled product: status %d", code)
	}

	var a availabilityView
	if code := get(t, s, "/products/PAIN-001/availability?city=Jeddah", &a); code != http.StatusOK {
		t.Fatalf("availability status %d", code)
	}
	if len(a.ByCity) != 3 {
		t.Fatalf("by_city %+v", a.ByCity)
	}
	if code := get(t, s, "/products/PAIN-001/availability", nil); code != http.StatusBadRequest {
		t.Errorf("missing city: status %d", code)
	}
}
