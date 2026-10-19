package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"shopper/internal/commerce"
)

// Tools the model can call. Rules (see CLAUDE.md):
//   - Arguments never include customer ID, cart ID or city. The session in ctx
//     decides all three, and unknown argument fields are rejected.
//   - Results are small view structs built here. commerce.Product is never
//     marshalled to the model, and exact stock counts stay internal.
//   - Errors go back as is_error tool results in plain words so the model can
//     explain them; the prescription guardrail itself lives in the adapter.

type tool struct {
	def anthropic.ToolParam
	run func(ctx context.Context, t *toolbox, input json.RawMessage) (any, error)
}

type toolbox struct {
	store commerce.CommerceAdapter
	now   func() time.Time
	tools map[string]tool
}

func newToolbox(store commerce.CommerceAdapter, now func() time.Time) *toolbox {
	t := &toolbox{store: store, now: now, tools: map[string]tool{}}
	for _, tl := range allTools {
		t.tools[tl.def.Name] = tl
	}
	return t
}

// params returns the tool definitions in a fixed order so the request prefix
// (and its prompt cache) stays stable.
func (t *toolbox) params() []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, len(allTools))
	for i := range allTools {
		def := allTools[i].def
		out[i] = anthropic.ToolUnionParam{OfTool: &def}
	}
	return out
}

// call runs one tool and returns the JSON text for the tool_result block.
func (t *toolbox) call(ctx context.Context, name string, input json.RawMessage) (string, bool) {
	tl, ok := t.tools[name]
	if !ok {
		return "unknown tool " + name, true
	}
	res, err := tl.run(ctx, t, input)
	if err != nil {
		return toolErrorText(name, err), true
	}
	b, err := json.Marshal(res)
	if err != nil {
		slog.Error("agent: marshal tool result", "tool", name, "err", err)
		return "internal error", true
	}
	return string(b), false
}

func toolErrorText(name string, err error) string {
	var bad badInput
	switch {
	case errors.As(err, &bad):
		return "invalid arguments: " + bad.msg
	case errors.Is(err, commerce.ErrPrescriptionRequired):
		return "This is a prescription-only medicine. You cannot add it to the cart. " +
			"Tell the customer it needs a valid prescription and a pharmacist."
	case errors.Is(err, commerce.ErrNotFound):
		return "No product with that SKU. Search again rather than guessing SKUs."
	case errors.Is(err, commerce.ErrOutOfStock):
		return "Not enough stock in the customer's city for that quantity."
	case errors.Is(err, commerce.ErrInvalidQty):
		return fmt.Sprintf("Quantity must be between 1 and %d per product.", commerce.MaxLineQty)
	case errors.Is(err, commerce.ErrNotInCart):
		return "That product is not in the cart."
	case errors.Is(err, commerce.ErrNoSession), errors.Is(err, commerce.ErrInvalidCity):
		return "The customer's session is not set up. Ask them to reload the page."
	}
	slog.Error("agent: tool failed", "tool", name, "err", err)
	return "internal error"
}

type badInput struct{ msg string }

func (b badInput) Error() string { return b.msg }

// decode parses tool input strictly: unknown fields (e.g. "city", "price",
// "cart_id") are an error the model sees and can correct.
func decode(input json.RawMessage, v any) error {
	if len(bytes.TrimSpace(input)) == 0 {
		input = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badInput{err.Error()}
	}
	return nil
}

func schema(props map[string]any, required ...string) anthropic.ToolInputSchemaParam {
	return anthropic.ToolInputSchemaParam{Properties: props, Required: required}
}

// ---------------------------------------------------------------------------
// Views
// ---------------------------------------------------------------------------

type productSummary struct {
	SKU                  string  `json:"sku"`
	Name                 string  `json:"name"`
	Brand                string  `json:"brand"`
	Category             string  `json:"category"`
	PriceSAR             float64 `json:"price_sar"` // what the customer pays now
	RegularPriceSAR      float64 `json:"regular_price_sar,omitempty"`
	OnSale               bool    `json:"on_sale"`
	RequiresPrescription bool    `json:"requires_prescription"`
	InStock              bool    `json:"in_stock"` // in the customer's city
}

type productDetails struct {
	productSummary
	Description      string          `json:"description"`
	ActiveIngredient string          `json:"active_ingredient,omitempty"`
	AgeMin           int             `json:"age_min,omitempty"`
	InStockCities    []commerce.City `json:"in_stock_cities"`
}

type availabilityResult struct {
	SKU                string          `json:"sku"`
	City               commerce.City   `json:"city"`
	InStock            bool            `json:"in_stock"`
	LowStock           bool            `json:"low_stock"`
	OtherCitiesInStock []commerce.City `json:"other_cities_in_stock"`
}

type cartLine struct {
	SKU         string  `json:"sku"`
	Name        string  `json:"name"`
	Qty         int     `json:"qty"`
	UnitSAR     float64 `json:"unit_price_sar"`
	RowTotalSAR float64 `json:"row_total_sar"`
	OnSale      bool    `json:"on_sale"`
}

type cartResult struct {
	City        commerce.City `json:"city"`
	Lines       []cartLine    `json:"lines"`
	ItemCount   int           `json:"item_count"`
	SubtotalSAR float64       `json:"subtotal_sar"`
}

func summarize(p commerce.Product, s commerce.Session, now time.Time) productSummary {
	v := productSummary{
		SKU: p.SKU, Name: p.LocalizedName(s.Locale), Brand: p.Brand, Category: p.Category,
		PriceSAR: p.EffectivePrice(now).Riyals(), OnSale: p.OnSale(now),
		RequiresPrescription: p.RequiresPrescription, InStock: p.QtyIn(s.City) > 0,
	}
	if v.OnSale {
		v.RegularPriceSAR = p.Price.Riyals()
	}
	return v
}

func toCartResult(c commerce.Cart) cartResult {
	out := cartResult{City: c.City, Lines: make([]cartLine, len(c.Lines)), ItemCount: c.ItemCount, SubtotalSAR: c.Subtotal.Riyals()}
	for i, l := range c.Lines {
		out.Lines[i] = cartLine{SKU: l.SKU, Name: l.Name, Qty: l.Qty, UnitSAR: l.UnitPrice.Riyals(), RowTotalSAR: l.RowTotal.Riyals(), OnSale: l.OnSale}
	}
	return out
}

// ---------------------------------------------------------------------------
// Definitions
// ---------------------------------------------------------------------------

const searchPageSize = 8

var allTools = []tool{
	{
		def: anthropic.ToolParam{
			Name: "search_products",
			Description: anthropic.String("Search the store catalogue (English or Arabic). Returns up to " +
				fmt.Sprint(searchPageSize) + " products with current prices in SAR and whether each is in stock " +
				"in the customer's city. Use it before recommending or adding any product."),
			InputSchema: schema(map[string]any{
				"query":         map[string]any{"type": "string", "description": "Product name, symptom, brand or active ingredient"},
				"category":      map[string]any{"type": "string", "description": "Optional category filter, e.g. pain-relief, vitamins, skin-care"},
				"max_price_sar": map[string]any{"type": "number", "description": "Optional maximum price in SAR"},
				"in_stock_only": map[string]any{"type": "boolean", "description": "Only products in stock in the customer's city"},
			}, "query"),
		},
		run: func(ctx context.Context, t *toolbox, input json.RawMessage) (any, error) {
			var in struct {
				Query       string   `json:"query"`
				Category    string   `json:"category"`
				MaxPriceSAR *float64 `json:"max_price_sar"`
				InStockOnly bool     `json:"in_stock_only"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			s, err := commerce.SessionFrom(ctx)
			if err != nil {
				return nil, err
			}
			q := commerce.SearchQuery{
				Text: in.Query, Category: in.Category, City: s.City,
				InStockOnly: in.InStockOnly, PageSize: searchPageSize,
			}
			if in.MaxPriceSAR != nil {
				if *in.MaxPriceSAR <= 0 {
					return nil, badInput{"max_price_sar must be positive"}
				}
				q.MaxPrice = commerce.SAR(*in.MaxPriceSAR)
			}
			res, err := t.store.SearchProducts(ctx, q)
			if err != nil {
				return nil, err
			}
			now := t.now()
			items := make([]productSummary, len(res.Items))
			for i, p := range res.Items {
				items[i] = summarize(p, s, now)
			}
			return struct {
				Total    int              `json:"total_matches"`
				Products []productSummary `json:"products"`
			}{res.Total, items}, nil
		},
	},
	{
		def: anthropic.ToolParam{
			Name:        "get_product",
			Description: anthropic.String("Full details for one product by SKU: description, active ingredient, minimum age, price and the cities where it is in stock."),
			InputSchema: schema(map[string]any{
				"sku": map[string]any{"type": "string", "description": "Product SKU from search results, e.g. PAIN-001"},
			}, "sku"),
		},
		run: func(ctx context.Context, t *toolbox, input json.RawMessage) (any, error) {
			var in struct {
				SKU string `json:"sku"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			s, err := commerce.SessionFrom(ctx)
			if err != nil {
				return nil, err
			}
			p, err := t.store.GetProduct(ctx, in.SKU)
			if err != nil {
				return nil, err
			}
			d := productDetails{
				productSummary: summarize(p, s, t.now()),
				Description:    p.LocalizedDescription(s.Locale), ActiveIngredient: p.ActiveIngredient, AgeMin: p.AgeMin,
				InStockCities: []commerce.City{},
			}
			for _, c := range commerce.Cities {
				if p.QtyIn(c) > 0 {
					d.InStockCities = append(d.InStockCities, c)
				}
			}
			return d, nil
		},
	},
	{
		def: anthropic.ToolParam{
			Name:        "check_availability",
			Description: anthropic.String("Check whether a product is in stock in the customer's delivery city, whether stock is low, and which other cities have it."),
			InputSchema: schema(map[string]any{
				"sku": map[string]any{"type": "string"},
			}, "sku"),
		},
		run: func(ctx context.Context, t *toolbox, input json.RawMessage) (any, error) {
			var in struct {
				SKU string `json:"sku"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			s, err := commerce.SessionFrom(ctx)
			if err != nil {
				return nil, err
			}
			a, err := t.store.CheckAvailability(ctx, in.SKU, s.City)
			if err != nil {
				return nil, err
			}
			res := availabilityResult{SKU: a.SKU, City: a.City, InStock: a.InStock, LowStock: a.InStock && a.Qty < 10, OtherCitiesInStock: []commerce.City{}}
			for _, c := range commerce.Cities {
				if c != a.City && a.ByCity[c] > 0 {
					res.OtherCitiesInStock = append(res.OtherCitiesInStock, c)
				}
			}
			return res, nil
		},
	},
	{
		def: anthropic.ToolParam{
			Name: "add_to_cart",
			Description: anthropic.String("Add a product to the customer's cart and return the updated cart. " +
				"Only do this when the customer has asked for it. Prescription-only products are refused."),
			InputSchema: schema(map[string]any{
				"sku": map[string]any{"type": "string"},
				"qty": map[string]any{"type": "integer", "minimum": 1, "maximum": commerce.MaxLineQty, "description": "Defaults to 1"},
			}, "sku"),
		},
		run: func(ctx context.Context, t *toolbox, input json.RawMessage) (any, error) {
			var in struct {
				SKU string `json:"sku"`
				Qty *int   `json:"qty"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			qty := 1
			if in.Qty != nil {
				qty = *in.Qty
			}
			c, err := t.store.AddToCart(ctx, in.SKU, qty)
			if err != nil {
				return nil, err
			}
			return toCartResult(c), nil
		},
	},
	{
		def: anthropic.ToolParam{
			Name:        "view_cart",
			Description: anthropic.String("Show the customer's cart with current prices and the subtotal in SAR."),
			InputSchema: schema(map[string]any{}),
		},
		run: func(ctx context.Context, t *toolbox, input json.RawMessage) (any, error) {
			if err := decode(input, &struct{}{}); err != nil {
				return nil, err
			}
			c, err := t.store.GetCart(ctx)
			if err != nil {
				return nil, err
			}
			return toCartResult(c), nil
		},
	},
}
