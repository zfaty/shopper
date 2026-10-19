package commerce

import (
	"context"
	"errors"
)

// CommerceAdapter is the only way the agent and the HTTP API reach store
// data. Implementations: mock (in-memory, from data/products.json) and,
// later, magento (REST). Nothing outside an adapter touches a database.
//
// Rules every implementation must follow:
//   - Cart methods take the cart from the Session in ctx, never from an
//     argument. The model cannot choose whose cart it touches.
//   - Prices are computed here (EffectivePrice) on every read. Callers never
//     pass a price in.
//   - Disabled products behave as ErrNotFound for customers.
//   - Errors are the sentinel errors below (wrapped is fine), so the API can
//     map them to status codes and the agent can explain them in plain words.
type CommerceAdapter interface {
	// Catalogue (no session needed).
	SearchProducts(ctx context.Context, q SearchQuery) (SearchResult, error)
	GetProduct(ctx context.Context, sku string) (Product, error)
	CheckAvailability(ctx context.Context, sku string, city City) (Availability, error)

	// Cart (requires a Session in ctx).
	GetCart(ctx context.Context) (Cart, error)
	AddToCart(ctx context.Context, sku string, qty int) (Cart, error)
	UpdateCartItem(ctx context.Context, sku string, qty int) (Cart, error) // qty 0 removes
	RemoveFromCart(ctx context.Context, sku string) (Cart, error)
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

var (
	ErrNotFound             = errors.New("product not found")
	ErrOutOfStock           = errors.New("not enough stock in this city")
	ErrInvalidQty           = errors.New("quantity must be between 1 and the per-item limit")
	ErrInvalidCity          = errors.New("unknown city")
	ErrPrescriptionRequired = errors.New("product requires a prescription and cannot be added by the assistant")
	ErrNoSession            = errors.New("no authenticated session")
	ErrNotInCart            = errors.New("item is not in the cart")
)

// ---------------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------------

// Session is the authenticated caller. HTTP middleware builds it from the
// session cookie and stores it in the request context. Tool arguments from
// the LLM never contain these fields.
type Session struct {
	CustomerID string // empty for guests
	CartID     string
	City       City // delivery city, drives stock checks and cart pricing
	Locale     Locale
}

type sessionKey struct{}

// WithSession returns a copy of ctx carrying s.
func WithSession(ctx context.Context, s Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

// SessionFrom returns the Session in ctx, or ErrNoSession.
func SessionFrom(ctx context.Context) (Session, error) {
	s, ok := ctx.Value(sessionKey{}).(Session)
	if !ok || s.CartID == "" {
		return Session{}, ErrNoSession
	}
	return s, nil
}
