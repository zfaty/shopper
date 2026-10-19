// Package api exposes the CommerceAdapter over HTTP for the chat UI and for
// manual testing. Handlers translate query params to commerce types and
// commerce types to response views. They hold no business rules.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"shopper/internal/commerce"
)

// Server holds the routes. Create it with New.
type Server struct {
	store    commerce.CommerceAdapter
	now      func() time.Time
	mux      *http.ServeMux
	sessions *sessionStore
	chat     Chatter // nil until SetChatter
}

// New returns an http.Handler serving the product, session and cart API.
func New(store commerce.CommerceAdapter) *Server {
	s := &Server{store: store, now: time.Now, mux: http.NewServeMux(), sessions: newSessionStore()}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	s.mux.HandleFunc("GET /products/search", s.search)
	s.mux.HandleFunc("GET /products/{sku}", s.product)
	s.mux.HandleFunc("GET /products/{sku}/availability", s.availability)

	// Session-scoped routes. Catalogue routes stay sessionless so browsing
	// doesn't create sessions.
	s.mux.HandleFunc("GET /session", s.withSession(s.getSession))
	s.mux.HandleFunc("PATCH /session", s.withSession(s.patchSession))
	s.mux.HandleFunc("GET /cart", s.withSession(s.getCart))
	s.mux.HandleFunc("POST /cart/items", s.withSession(s.addCartItem))
	s.mux.HandleFunc("PATCH /cart/items/{sku}", s.withSession(s.updateCartItem))
	s.mux.HandleFunc("DELETE /cart/items/{sku}", s.withSession(s.removeCartItem))
	s.mux.HandleFunc("POST /chat", s.withSession(s.postChat))
	s.mux.HandleFunc("DELETE /chat", s.withSession(s.deleteChat))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// GET /products/search?q=&category=&brand=&city=&in_stock=&max_price=&otc_only=&page=&page_size=&lang=
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	q := commerce.SearchQuery{
		Text:                v.Get("q"),
		Category:            v.Get("category"),
		Brand:               v.Get("brand"),
		InStockOnly:         isTrue(v.Get("in_stock")),
		ExcludePrescription: isTrue(v.Get("otc_only")),
	}
	var err error
	if q.City, err = optionalCity(v.Get("city")); err != nil {
		writeError(w, err)
		return
	}
	if mp := v.Get("max_price"); mp != "" {
		f, err := strconv.ParseFloat(mp, 64)
		if err != nil || f <= 0 {
			writeBadRequest(w, "max_price must be a positive number in SAR")
			return
		}
		q.MaxPrice = commerce.SAR(f)
	}
	if q.Page, err = optionalInt(v.Get("page")); err != nil {
		writeBadRequest(w, "page must be an integer")
		return
	}
	if q.PageSize, err = optionalInt(v.Get("page_size")); err != nil {
		writeBadRequest(w, "page_size must be an integer")
		return
	}

	res, err := s.store.SearchProducts(r.Context(), q)
	if err != nil {
		writeError(w, err)
		return
	}
	loc, now := locale(r), s.now()
	items := make([]productView, len(res.Items))
	for i, p := range res.Items {
		items[i] = toView(p, loc, q.City, now)
	}
	writeJSON(w, http.StatusOK, searchResponse{
		Items: items, Total: res.Total, Page: res.Page, PageSize: res.PageSize,
	})
}

// GET /products/{sku}?city=&lang=
func (s *Server) product(w http.ResponseWriter, r *http.Request) {
	city, err := optionalCity(r.URL.Query().Get("city"))
	if err != nil {
		writeError(w, err)
		return
	}
	p, err := s.store.GetProduct(r.Context(), r.PathValue("sku"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toView(p, locale(r), city, s.now()))
}

// GET /products/{sku}/availability?city=  (city required)
func (s *Server) availability(w http.ResponseWriter, r *http.Request) {
	city, ok := commerce.ParseCity(r.URL.Query().Get("city"))
	if !ok {
		writeError(w, commerce.ErrInvalidCity)
		return
	}
	a, err := s.store.CheckAvailability(r.Context(), r.PathValue("sku"), city)
	if err != nil {
		writeError(w, err)
		return
	}
	byCity := make(map[commerce.City]bool, len(a.ByCity))
	for c, q := range a.ByCity {
		byCity[c] = q > 0
	}
	writeJSON(w, http.StatusOK, availabilityView{
		SKU: a.SKU, City: a.City, InStock: a.InStock, LowStock: a.InStock && a.Qty < 10, ByCity: byCity,
	})
}

// ---------------------------------------------------------------------------
// Views: the only shapes that leave the server. Exact stock counts stay
// internal; customers see in_stock / low_stock.
// ---------------------------------------------------------------------------

type productView struct {
	SKU                  string                 `json:"sku"`
	Name                 string                 `json:"name"`
	Description          string                 `json:"description"`
	Brand                string                 `json:"brand"`
	Category             string                 `json:"category"`
	Price                float64                `json:"price"`       // regular price, SAR
	FinalPrice           float64                `json:"final_price"` // what the customer pays now
	OnSale               bool                   `json:"on_sale"`
	Currency             string                 `json:"currency"`
	RequiresPrescription bool                   `json:"requires_prescription"`
	ActiveIngredient     string                 `json:"active_ingredient,omitempty"`
	AgeMin               int                    `json:"age_min,omitempty"`
	InStock              bool                   `json:"in_stock"` // in ?city, or anywhere when no city
	Availability         map[commerce.City]bool `json:"availability"`
}

type searchResponse struct {
	Items    []productView `json:"items"`
	Total    int           `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
}

type availabilityView struct {
	SKU      string                 `json:"sku"`
	City     commerce.City          `json:"city"`
	InStock  bool                   `json:"in_stock"`
	LowStock bool                   `json:"low_stock"`
	ByCity   map[commerce.City]bool `json:"by_city"`
}

func toView(p commerce.Product, loc commerce.Locale, city commerce.City, now time.Time) productView {
	avail := make(map[commerce.City]bool, len(commerce.Cities))
	for _, c := range commerce.Cities {
		avail[c] = p.QtyIn(c) > 0
	}
	inStock := p.TotalQty() > 0
	if city != "" {
		inStock = p.QtyIn(city) > 0
	}
	return productView{
		SKU: p.SKU, Name: p.LocalizedName(loc), Description: p.LocalizedDescription(loc),
		Brand: p.Brand, Category: p.Category,
		Price: p.Price.Riyals(), FinalPrice: p.EffectivePrice(now).Riyals(), OnSale: p.OnSale(now),
		Currency: commerce.Currency, RequiresPrescription: p.RequiresPrescription,
		ActiveIngredient: p.ActiveIngredient, AgeMin: p.AgeMin,
		InStock: inStock, Availability: avail,
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func locale(r *http.Request) commerce.Locale {
	if strings.EqualFold(r.URL.Query().Get("lang"), "ar") {
		return commerce.LocaleAR
	}
	return commerce.LocaleEN
}

func optionalCity(s string) (commerce.City, error) {
	if s == "" {
		return "", nil
	}
	c, ok := commerce.ParseCity(s)
	if !ok {
		return "", commerce.ErrInvalidCity
	}
	return c, nil
}

func optionalInt(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.Atoi(s)
}

func isTrue(s string) bool {
	b, _ := strconv.ParseBool(s)
	return b
}

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		slog.Error("write response", "err", err)
	}
}

func writeBadRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, errorBody{Error: msg, Code: "bad_request"})
}

// writeError maps commerce sentinel errors to HTTP statuses and stable codes.
// Unknown errors become 500 with a generic message; details go to the log.
func writeError(w http.ResponseWriter, err error) {
	type mapping struct {
		target error
		status int
		code   string
	}
	for _, m := range []mapping{
		{commerce.ErrNotFound, http.StatusNotFound, "not_found"},
		{commerce.ErrInvalidCity, http.StatusBadRequest, "invalid_city"},
		{commerce.ErrInvalidQty, http.StatusBadRequest, "invalid_qty"},
		{commerce.ErrOutOfStock, http.StatusConflict, "out_of_stock"},
		{commerce.ErrNotInCart, http.StatusNotFound, "not_in_cart"},
		{commerce.ErrPrescriptionRequired, http.StatusForbidden, "prescription_required"},
		{commerce.ErrNoSession, http.StatusUnauthorized, "no_session"},
	} {
		if errors.Is(err, m.target) {
			writeJSON(w, m.status, errorBody{Error: m.target.Error(), Code: m.code})
			return
		}
	}
	slog.Error("internal error", "err", err)
	writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error", Code: "internal"})
}
