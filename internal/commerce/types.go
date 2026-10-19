// Package commerce defines the domain types and the adapter contract that
// every backend (mock, magento) implements. It knows nothing about HTTP,
// LLMs or any specific store. The agent and API layers depend on this
// package. This package depends on nothing.
package commerce

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Money
// ---------------------------------------------------------------------------

// Currency is fixed for the MVP.
const Currency = "SAR"

// Money is an amount in halalas (1 SAR = 100 halalas). Integer minor units
// keep cart totals exact. Magento sends floats, so the magento adapter
// converts at the boundary with SAR().
type Money int64

// SAR converts a riyal amount (e.g. 24.95) to Money.
func SAR(riyals float64) Money { return Money(math.Round(riyals * 100)) }

// Riyals returns the amount as a float, for display or Magento payloads only.
func (m Money) Riyals() float64 { return float64(m) / 100 }

func (m Money) String() string {
	sign := ""
	if m < 0 {
		sign, m = "-", -m
	}
	return fmt.Sprintf("%s%d.%02d %s", sign, m/100, m%100, Currency)
}

// ---------------------------------------------------------------------------
// Enums
// ---------------------------------------------------------------------------

// City identifies a stock location (an MSI "source" in Magento terms).
type City string

const (
	CityRiyadh City = "riyadh"
	CityJeddah City = "jeddah"
	CityDammam City = "dammam"
)

// Cities lists every supported city, in display order.
var Cities = []City{CityRiyadh, CityJeddah, CityDammam}

// ParseCity normalises user input ("Riyadh", " JEDDAH ") to a City.
func ParseCity(s string) (City, bool) {
	c := City(strings.ToLower(strings.TrimSpace(s)))
	return c, c.Valid()
}

func (c City) Valid() bool {
	switch c {
	case CityRiyadh, CityJeddah, CityDammam:
		return true
	}
	return false
}

// Status mirrors Magento's product status attribute (1 enabled, 2 disabled).
type Status int

const (
	StatusEnabled  Status = 1
	StatusDisabled Status = 2
)

// Locale selects which language fields are returned.
type Locale string

const (
	LocaleEN Locale = "en"
	LocaleAR Locale = "ar"
)

// ---------------------------------------------------------------------------
// Product
// ---------------------------------------------------------------------------

// Product mirrors the subset of a Magento product the demo needs. Fields that
// live in Magento's custom_attributes array are flattened into typed fields.
// The magento adapter does that mapping, so callers never deal with
// attribute_code/value pairs.
//
// This is the internal model. Tools must not marshal it straight to the LLM.
// They build their own view structs with only the allowed fields.
type Product struct {
	SKU           string `json:"sku"`
	Name          string `json:"name"`
	NameAr        string `json:"name_ar"`
	Description   string `json:"description"`
	DescriptionAr string `json:"description_ar"`
	Brand         string `json:"brand"`
	Category      string `json:"category"` // slug, e.g. "pain-relief"

	Price        Money      `json:"price"`
	SpecialPrice *Money     `json:"special_price,omitempty"`
	SpecialFrom  *time.Time `json:"special_from_date,omitempty"`
	SpecialTo    *time.Time `json:"special_to_date,omitempty"`

	Status Status `json:"status"`

	// Stock is quantity per city. Magento's top-level qty is TotalQty().
	Stock map[City]int `json:"stock"`

	// Custom attributes.
	RequiresPrescription bool   `json:"requires_prescription"`
	ActiveIngredient     string `json:"active_ingredient,omitempty"`
	AgeMin               int    `json:"age_min,omitempty"` // 0 = no minimum
}

// EffectivePrice returns the special price if it is set, lower than the
// regular price and active at now. Otherwise it returns the regular price.
// This is the only place price rules live.
func (p Product) EffectivePrice(now time.Time) Money {
	if p.OnSale(now) {
		return *p.SpecialPrice
	}
	return p.Price
}

// OnSale reports whether a special price applies at now.
func (p Product) OnSale(now time.Time) bool {
	if p.SpecialPrice == nil || *p.SpecialPrice >= p.Price {
		return false
	}
	if p.SpecialFrom != nil && now.Before(*p.SpecialFrom) {
		return false
	}
	// Magento's special_to_date is a date and includes that whole day.
	if p.SpecialTo != nil && !now.Before(p.SpecialTo.AddDate(0, 0, 1)) {
		return false
	}
	return true
}

// QtyIn returns stock in one city.
func (p Product) QtyIn(c City) int { return p.Stock[c] }

// TotalQty returns stock across all cities.
func (p Product) TotalQty() int {
	n := 0
	for _, q := range p.Stock {
		n += q
	}
	return n
}

// Enabled reports whether the product may be shown to customers.
func (p Product) Enabled() bool { return p.Status == StatusEnabled }

// LocalizedName returns the name for loc, falling back to English.
func (p Product) LocalizedName(loc Locale) string {
	if loc == LocaleAR && p.NameAr != "" {
		return p.NameAr
	}
	return p.Name
}

// LocalizedDescription returns the description for loc, falling back to English.
func (p Product) LocalizedDescription(loc Locale) string {
	if loc == LocaleAR && p.DescriptionAr != "" {
		return p.DescriptionAr
	}
	return p.Description
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

const (
	DefaultPageSize = 10
	MaxPageSize     = 50
)

// SearchQuery holds catalogue filters. Zero values mean "no filter".
type SearchQuery struct {
	Text        string `json:"q,omitempty"`
	Category    string `json:"category,omitempty"`
	Brand       string `json:"brand,omitempty"`
	City        City   `json:"city,omitempty"`          // with InStockOnly, checks this city
	InStockOnly bool   `json:"in_stock_only,omitempty"` // without City, checks any city
	MaxPrice    Money  `json:"max_price,omitempty"`     // compares the effective price
	// ExcludePrescription hides Rx products, e.g. for OTC-only answers.
	ExcludePrescription bool `json:"exclude_prescription,omitempty"`

	Page     int `json:"page,omitempty"`      // 1-based
	PageSize int `json:"page_size,omitempty"` // clamped to [1, MaxPageSize]
}

// Normalize fills defaults and clamps paging. Adapters call it first, so
// neither the HTTP layer nor the model can request 10,000 rows.
func (q SearchQuery) Normalize() SearchQuery {
	q.Text = strings.TrimSpace(q.Text)
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 {
		q.PageSize = DefaultPageSize
	}
	if q.PageSize > MaxPageSize {
		q.PageSize = MaxPageSize
	}
	return q
}

// SearchResult is one page of matching products.
type SearchResult struct {
	Items    []Product `json:"items"`
	Total    int       `json:"total"` // matches across all pages
	Page     int       `json:"page"`
	PageSize int       `json:"page_size"`
}

// ---------------------------------------------------------------------------
// Availability
// ---------------------------------------------------------------------------

// Availability is the stock picture for one SKU from one city's point of view.
type Availability struct {
	SKU     string       `json:"sku"`
	City    City         `json:"city"`
	Qty     int          `json:"qty"`
	InStock bool         `json:"in_stock"`
	ByCity  map[City]int `json:"by_city"` // lets the agent suggest "available in Jeddah"
}

// ---------------------------------------------------------------------------
// Cart
// ---------------------------------------------------------------------------

// MaxLineQty caps the quantity of one SKU in a cart.
const MaxLineQty = 10

// CartLine is one SKU in a cart. UnitPrice and RowTotal are computed by the
// adapter on every read and are never taken from the caller.
type CartLine struct {
	SKU       string `json:"sku"`
	Name      string `json:"name"`
	Qty       int    `json:"qty"`
	UnitPrice Money  `json:"unit_price"`
	RowTotal  Money  `json:"row_total"`
	OnSale    bool   `json:"on_sale"`
}

// Cart is a priced snapshot. Adapters store only SKU + qty and re-price on
// every GetCart/AddToCart/etc., so a stale price can never reach checkout.
type Cart struct {
	ID         string     `json:"id"`
	City       City       `json:"city"`
	Lines      []CartLine `json:"lines"`
	ItemCount  int        `json:"item_count"` // sum of quantities
	Subtotal   Money      `json:"subtotal"`
	GrandTotal Money      `json:"grand_total"` // = Subtotal until shipping/discounts exist
	Currency   string     `json:"currency"`
	PricedAt   time.Time  `json:"priced_at"`
}
