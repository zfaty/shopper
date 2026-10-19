package api

import (
	"encoding/json"
	"net/http"
	"time"

	"shopper/internal/commerce"
)

// Cart endpoints. The cart is always the one in the session; request bodies
// carry only SKU and quantity. Prices come back from the adapter, freshly
// computed on every call, and unknown body fields (e.g. "price") are rejected.

// GET /cart?lang=
func (s *Server) getCart(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.GetCart(r.Context())
	s.writeCart(w, http.StatusOK, c, err)
}

// POST /cart/items  {"sku": "PAIN-001", "qty": 2}   qty defaults to 1
func (s *Server) addCartItem(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SKU string `json:"sku"`
		Qty *int   `json:"qty"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.SKU == "" {
		writeBadRequest(w, "sku is required")
		return
	}
	qty := 1
	if body.Qty != nil {
		qty = *body.Qty
	}
	c, err := s.store.AddToCart(r.Context(), body.SKU, qty)
	s.writeCart(w, http.StatusOK, c, err)
}

// PATCH /cart/items/{sku}  {"qty": 3}   qty 0 removes the line
func (s *Server) updateCartItem(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Qty *int `json:"qty"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Qty == nil {
		writeBadRequest(w, "qty is required")
		return
	}
	c, err := s.store.UpdateCartItem(r.Context(), r.PathValue("sku"), *body.Qty)
	s.writeCart(w, http.StatusOK, c, err)
}

// DELETE /cart/items/{sku}
func (s *Server) removeCartItem(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.RemoveFromCart(r.Context(), r.PathValue("sku"))
	s.writeCart(w, http.StatusOK, c, err)
}

func (s *Server) writeCart(w http.ResponseWriter, status int, c commerce.Cart, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, status, toCartView(c))
}

// ---------------------------------------------------------------------------
// Views. Money leaves the server as SAR floats, like productView. The cart ID
// stays internal.
// ---------------------------------------------------------------------------

type cartLineView struct {
	SKU       string  `json:"sku"`
	Name      string  `json:"name"`
	Qty       int     `json:"qty"`
	UnitPrice float64 `json:"unit_price"`
	RowTotal  float64 `json:"row_total"`
	OnSale    bool    `json:"on_sale"`
}

type cartView struct {
	City       commerce.City  `json:"city"`
	Lines      []cartLineView `json:"lines"`
	ItemCount  int            `json:"item_count"`
	Subtotal   float64        `json:"subtotal"`
	GrandTotal float64        `json:"grand_total"`
	Currency   string         `json:"currency"`
	PricedAt   time.Time      `json:"priced_at"`
}

func toCartView(c commerce.Cart) cartView {
	lines := make([]cartLineView, len(c.Lines))
	for i, l := range c.Lines {
		lines[i] = cartLineView{
			SKU: l.SKU, Name: l.Name, Qty: l.Qty,
			UnitPrice: l.UnitPrice.Riyals(), RowTotal: l.RowTotal.Riyals(), OnSale: l.OnSale,
		}
	}
	return cartView{
		City: c.City, Lines: lines, ItemCount: c.ItemCount,
		Subtotal: c.Subtotal.Riyals(), GrandTotal: c.GrandTotal.Riyals(),
		Currency: c.Currency, PricedAt: c.PricedAt,
	}
}

// decodeBody reads a small JSON body into v, rejecting unknown fields so a
// caller cannot slip in a price or cart ID. On failure it writes 400 and
// returns false.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeBadRequest(w, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}
