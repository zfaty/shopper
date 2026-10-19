// Package mock is an in-memory CommerceAdapter backed by data/products.json.
// Carts live in memory and are lost on restart. Stock is read-only (there is
// no checkout yet), so it never decreases.
package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"shopper/internal/commerce"
)

// Adapter implements commerce.CommerceAdapter.
type Adapter struct {
	// Now is the clock used for special-price dates. Tests override it.
	Now func() time.Time

	products []commerce.Product // catalogue order, immutable after New
	bySKU    map[string]int     // sku → index in products
	index    []searchDoc        // parallel to products

	mu    sync.Mutex
	carts map[string]*cart // keyed by Session.CartID
}

var _ commerce.CommerceAdapter = (*Adapter)(nil)

// searchDoc holds pre-tokenised fields, weighted by where a match counts most.
type searchDoc struct {
	name   []string // name, name_ar
	strong []string // sku, brand, active ingredient
	medium []string // category
	weak   []string // descriptions
}

// cart stores only SKU and quantity. Prices are computed on every read.
type cart struct {
	order []string // SKUs in the order they were added
	qty   map[string]int
}

// LoadFile reads a products JSON file and returns an Adapter.
func LoadFile(path string) (*Adapter, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read catalogue: %w", err)
	}
	var products []commerce.Product
	if err := json.Unmarshal(data, &products); err != nil {
		return nil, fmt.Errorf("parse catalogue %s: %w", path, err)
	}
	return New(products)
}

// New builds an Adapter from products. It rejects empty or duplicate SKUs.
func New(products []commerce.Product) (*Adapter, error) {
	a := &Adapter{
		Now:      time.Now,
		products: products,
		bySKU:    make(map[string]int, len(products)),
		index:    make([]searchDoc, len(products)),
		carts:    map[string]*cart{},
	}
	for i, p := range products {
		if p.SKU == "" {
			return nil, fmt.Errorf("product %d has no sku", i)
		}
		if _, dup := a.bySKU[p.SKU]; dup {
			return nil, fmt.Errorf("duplicate sku %q", p.SKU)
		}
		a.bySKU[p.SKU] = i
		a.index[i] = searchDoc{
			name:   tokenize(p.Name + " " + p.NameAr),
			strong: tokenize(strings.Join([]string{p.SKU, p.Brand, p.ActiveIngredient}, " ")),
			medium: tokenize(p.Category),
			weak:   tokenize(p.Description + " " + p.DescriptionAr),
		}
	}
	return a, nil
}

// lookup returns an enabled product. Disabled products look like ErrNotFound.
func (a *Adapter) lookup(sku string) (commerce.Product, error) {
	i, ok := a.bySKU[strings.ToUpper(strings.TrimSpace(sku))]
	if !ok || !a.products[i].Enabled() {
		return commerce.Product{}, fmt.Errorf("%w: %s", commerce.ErrNotFound, sku)
	}
	return a.products[i], nil
}

// ---------------------------------------------------------------------------
// Catalogue
// ---------------------------------------------------------------------------

func (a *Adapter) SearchProducts(ctx context.Context, q commerce.SearchQuery) (commerce.SearchResult, error) {
	if err := ctx.Err(); err != nil {
		return commerce.SearchResult{}, err
	}
	q = q.Normalize()
	if q.City != "" && !q.City.Valid() {
		return commerce.SearchResult{}, commerce.ErrInvalidCity
	}
	terms := tokenize(q.Text)
	now := a.Now()

	type hit struct {
		i     int
		score int
	}
	var hits []hit
	for i, p := range a.products {
		if !p.Enabled() ||
			(q.Category != "" && !strings.EqualFold(p.Category, q.Category)) ||
			(q.Brand != "" && !strings.EqualFold(p.Brand, q.Brand)) ||
			(q.ExcludePrescription && p.RequiresPrescription) ||
			(q.MaxPrice > 0 && p.EffectivePrice(now) > q.MaxPrice) {
			continue
		}
		if q.InStockOnly {
			if q.City != "" && p.QtyIn(q.City) == 0 || q.City == "" && p.TotalQty() == 0 {
				continue
			}
		}
		score, ok := a.score(i, terms)
		if !ok {
			continue
		}
		hits = append(hits, hit{i, score})
	}

	// Best match first; among equals, in-stock before sold out, then by name.
	inStock := func(p commerce.Product) bool {
		if q.City != "" {
			return p.QtyIn(q.City) > 0
		}
		return p.TotalQty() > 0
	}
	slices.SortStableFunc(hits, func(x, y hit) int {
		if x.score != y.score {
			return y.score - x.score
		}
		px, py := a.products[x.i], a.products[y.i]
		if sx, sy := inStock(px), inStock(py); sx != sy {
			if sx {
				return -1
			}
			return 1
		}
		return strings.Compare(px.Name, py.Name)
	})

	res := commerce.SearchResult{Total: len(hits), Page: q.Page, PageSize: q.PageSize, Items: []commerce.Product{}}
	start := (q.Page - 1) * q.PageSize
	for k := start; k < len(hits) && k < start+q.PageSize; k++ {
		res.Items = append(res.Items, a.products[hits[k].i])
	}
	return res, nil
}

// score returns how well product i matches every term. All terms must match
// somewhere (AND). No terms matches everything with score 0.
func (a *Adapter) score(i int, terms []string) (int, bool) {
	d := a.index[i]
	total := 0
	for _, t := range terms {
		switch {
		case matchesAny(d.name, t):
			total += 4
		case matchesAny(d.strong, t):
			total += 3
		case matchesAny(d.medium, t):
			total += 2
		case matchesAny(d.weak, t):
			total += 1
		default:
			return 0, false
		}
	}
	return total, true
}

func (a *Adapter) GetProduct(ctx context.Context, sku string) (commerce.Product, error) {
	if err := ctx.Err(); err != nil {
		return commerce.Product{}, err
	}
	return a.lookup(sku)
}

func (a *Adapter) CheckAvailability(ctx context.Context, sku string, city commerce.City) (commerce.Availability, error) {
	if err := ctx.Err(); err != nil {
		return commerce.Availability{}, err
	}
	if !city.Valid() {
		return commerce.Availability{}, commerce.ErrInvalidCity
	}
	p, err := a.lookup(sku)
	if err != nil {
		return commerce.Availability{}, err
	}
	byCity := make(map[commerce.City]int, len(commerce.Cities))
	for _, c := range commerce.Cities {
		byCity[c] = p.QtyIn(c)
	}
	return commerce.Availability{
		SKU: p.SKU, City: city, Qty: p.QtyIn(city), InStock: p.QtyIn(city) > 0, ByCity: byCity,
	}, nil
}

// ---------------------------------------------------------------------------
// Cart
// ---------------------------------------------------------------------------

func (a *Adapter) GetCart(ctx context.Context) (commerce.Cart, error) {
	s, err := commerce.SessionFrom(ctx)
	if err != nil {
		return commerce.Cart{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.price(s, a.carts[s.CartID]), nil
}

func (a *Adapter) AddToCart(ctx context.Context, sku string, qty int) (commerce.Cart, error) {
	return a.setQty(ctx, sku, func(current int) int { return current + qty }, qty)
}

func (a *Adapter) UpdateCartItem(ctx context.Context, sku string, qty int) (commerce.Cart, error) {
	if qty == 0 {
		return a.RemoveFromCart(ctx, sku)
	}
	return a.setQty(ctx, sku, func(int) int { return qty }, qty)
}

func (a *Adapter) RemoveFromCart(ctx context.Context, sku string) (commerce.Cart, error) {
	s, err := commerce.SessionFrom(ctx)
	if err != nil {
		return commerce.Cart{}, err
	}
	sku = strings.ToUpper(strings.TrimSpace(sku))
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.carts[s.CartID]
	if c == nil || c.qty[sku] == 0 {
		return commerce.Cart{}, fmt.Errorf("%w: %s", commerce.ErrNotInCart, sku)
	}
	delete(c.qty, sku)
	c.order = slices.DeleteFunc(c.order, func(x string) bool { return x == sku })
	return a.price(s, c), nil
}

// setQty validates and applies a quantity change. next maps the current
// quantity to the new one; requested is the caller's raw qty for validation.
func (a *Adapter) setQty(ctx context.Context, sku string, next func(int) int, requested int) (commerce.Cart, error) {
	s, err := commerce.SessionFrom(ctx)
	if err != nil {
		return commerce.Cart{}, err
	}
	if !s.City.Valid() {
		return commerce.Cart{}, commerce.ErrInvalidCity
	}
	if requested < 1 || requested > commerce.MaxLineQty {
		return commerce.Cart{}, commerce.ErrInvalidQty
	}
	p, err := a.lookup(sku)
	if err != nil {
		return commerce.Cart{}, err
	}
	// Guardrail lives here, not in the prompt: the assistant can never put an
	// Rx product in the cart, whatever the model decides.
	if p.RequiresPrescription {
		return commerce.Cart{}, fmt.Errorf("%w: %s", commerce.ErrPrescriptionRequired, p.SKU)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.carts[s.CartID]
	if c == nil {
		c = &cart{qty: map[string]int{}}
		a.carts[s.CartID] = c
	}
	n := next(c.qty[p.SKU])
	if n > commerce.MaxLineQty {
		return commerce.Cart{}, commerce.ErrInvalidQty
	}
	if n > p.QtyIn(s.City) {
		return commerce.Cart{}, fmt.Errorf("%w: %s has %d in %s", commerce.ErrOutOfStock, p.SKU, p.QtyIn(s.City), s.City)
	}
	if c.qty[p.SKU] == 0 {
		c.order = append(c.order, p.SKU)
	}
	c.qty[p.SKU] = n
	return a.price(s, c), nil
}

// price builds a fresh snapshot from current catalogue prices. Caller holds mu.
// Lines whose product has since been disabled are dropped.
func (a *Adapter) price(s commerce.Session, c *cart) commerce.Cart {
	now := a.Now()
	out := commerce.Cart{
		ID: s.CartID, City: s.City, Lines: []commerce.CartLine{},
		Currency: commerce.Currency, PricedAt: now,
	}
	if c == nil {
		return out
	}
	for _, sku := range c.order {
		p, err := a.lookup(sku)
		if err != nil {
			continue
		}
		qty := c.qty[sku]
		unit := p.EffectivePrice(now)
		out.Lines = append(out.Lines, commerce.CartLine{
			SKU: p.SKU, Name: p.LocalizedName(s.Locale), Qty: qty,
			UnitPrice: unit, RowTotal: unit * commerce.Money(qty), OnSale: p.OnSale(now),
		})
		out.ItemCount += qty
		out.Subtotal += unit * commerce.Money(qty)
	}
	out.GrandTotal = out.Subtotal
	return out
}
