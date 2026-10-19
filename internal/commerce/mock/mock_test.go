package mock

import (
	"context"
	"errors"
	"testing"
	"time"

	"shopper/internal/commerce"
)

var testNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func load(t *testing.T) *Adapter {
	t.Helper()
	a, err := LoadFile("../../../data/products.json")
	if err != nil {
		t.Fatal(err)
	}
	a.Now = func() time.Time { return testNow }
	return a
}

func session(city commerce.City) context.Context {
	return commerce.WithSession(context.Background(), commerce.Session{
		CustomerID: "c1", CartID: "cart-1", City: city, Locale: commerce.LocaleEN,
	})
}

func skus(r commerce.SearchResult) []string {
	out := make([]string, len(r.Items))
	for i, p := range r.Items {
		out[i] = p.SKU
	}
	return out
}

func TestCatalogueLoads(t *testing.T) {
	a := load(t)
	if len(a.products) != 150 {
		t.Fatalf("got %d products, want 150", len(a.products))
	}
}

func TestSearch(t *testing.T) {
	a := load(t)
	ctx := context.Background()

	tests := []struct {
		name    string
		q       commerce.SearchQuery
		wantMin int
		first   string // expected top hit, "" to skip
	}{
		{"english name", commerce.SearchQuery{Text: "paracetamol"}, 4, ""},
		{"prefix", commerce.SearchQuery{Text: "paracet"}, 4, ""},
		{"arabic", commerce.SearchQuery{Text: "باراسيتامول"}, 4, ""},
		{"arabic without hamza", commerce.SearchQuery{Text: "اقراص باراسيتامول"}, 2, ""},
		{"description word", commerce.SearchQuery{Text: "headache"}, 1, ""},
		{"multi-word AND", commerce.SearchQuery{Text: "children ibuprofen"}, 2, ""},
		{"brand", commerce.SearchQuery{Text: "ghaim"}, 5, ""},
		{"category filter", commerce.SearchQuery{Category: "allergy"}, 10, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := a.SearchProducts(ctx, tt.q)
			if err != nil {
				t.Fatal(err)
			}
			if r.Total < tt.wantMin {
				t.Fatalf("total %d, want >= %d (%v)", r.Total, tt.wantMin, skus(r))
			}
		})
	}

	t.Run("no match", func(t *testing.T) {
		r, _ := a.SearchProducts(ctx, commerce.SearchQuery{Text: "zzzz"})
		if r.Total != 0 || r.Items == nil {
			t.Fatalf("want empty non-nil items, got %d", r.Total)
		}
	})

	t.Run("disabled products hidden", func(t *testing.T) {
		r, _ := a.SearchProducts(ctx, commerce.SearchQuery{PageSize: commerce.MaxPageSize, Page: 1})
		if r.Total != 147 {
			t.Fatalf("total %d, want 147 (150 minus 3 disabled)", r.Total)
		}
	})

	t.Run("exclude prescription", func(t *testing.T) {
		r, _ := a.SearchProducts(ctx, commerce.SearchQuery{Category: "prescription", ExcludePrescription: true})
		if r.Total != 0 {
			t.Fatalf("got %v", skus(r))
		}
	})

	t.Run("max price uses effective price", func(t *testing.T) {
		// PAIN-001 is 6.50 with an active 4.95 special price.
		r, _ := a.SearchProducts(ctx, commerce.SearchQuery{Text: "PAIN-001 paracetamol", MaxPrice: commerce.SAR(5)})
		found := false
		for _, p := range r.Items {
			if p.SKU == "PAIN-001" {
				found = true
			}
			if p.EffectivePrice(testNow) > commerce.SAR(5) {
				t.Errorf("%s costs %s, above max", p.SKU, p.EffectivePrice(testNow))
			}
		}
		if !found {
			t.Error("PAIN-001 should match at its sale price")
		}
	})

	t.Run("page size clamped", func(t *testing.T) {
		r, _ := a.SearchProducts(ctx, commerce.SearchQuery{PageSize: 10_000})
		if len(r.Items) != commerce.MaxPageSize {
			t.Fatalf("got %d items", len(r.Items))
		}
	})

	t.Run("in stock in city", func(t *testing.T) {
		r, _ := a.SearchProducts(ctx, commerce.SearchQuery{City: commerce.CityJeddah, InStockOnly: true, PageSize: 50})
		for _, p := range r.Items {
			if p.QtyIn(commerce.CityJeddah) == 0 {
				t.Errorf("%s has no Jeddah stock", p.SKU)
			}
		}
	})

	t.Run("invalid city", func(t *testing.T) {
		_, err := a.SearchProducts(ctx, commerce.SearchQuery{City: "cairo"})
		if !errors.Is(err, commerce.ErrInvalidCity) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestSpecialPriceDates(t *testing.T) {
	a := load(t)
	ctx := context.Background()
	cases := map[string]bool{
		"PAIN-001": true,  // window 2026-01-01..2027-12-31
		"VITA-001": false, // expired 2026-02-01
		"SKIN-003": false, // starts 2027-06-01
	}
	for sku, want := range cases {
		p, err := a.GetProduct(ctx, sku)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.OnSale(testNow); got != want {
			t.Errorf("%s on sale = %v, want %v", sku, got, want)
		}
	}

	// The end date includes its whole day.
	p, _ := a.GetProduct(ctx, "PAIN-001")
	if !p.OnSale(time.Date(2027, 12, 31, 23, 59, 0, 0, time.UTC)) {
		t.Error("sale should still apply late on its last day")
	}
	if p.OnSale(time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("sale should end after its last day")
	}
}

func TestGetProduct(t *testing.T) {
	a := load(t)
	if _, err := a.GetProduct(context.Background(), "pain-001"); err != nil {
		t.Errorf("sku lookup should be case-insensitive: %v", err)
	}
	if _, err := a.GetProduct(context.Background(), "COLD-005"); !errors.Is(err, commerce.ErrNotFound) {
		t.Errorf("disabled product: got %v, want ErrNotFound", err)
	}
}

func TestAvailability(t *testing.T) {
	a := load(t)
	av, err := a.CheckAvailability(context.Background(), "PAIN-001", commerce.CityRiyadh)
	if err != nil {
		t.Fatal(err)
	}
	if len(av.ByCity) != 3 || av.InStock != (av.Qty > 0) {
		t.Fatalf("bad availability %+v", av)
	}
}

func TestCart(t *testing.T) {
	a := load(t)
	ctx := session(commerce.CityJeddah)

	t.Run("requires session", func(t *testing.T) {
		if _, err := a.AddToCart(context.Background(), "PAIN-001", 1); !errors.Is(err, commerce.ErrNoSession) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("prescription blocked", func(t *testing.T) {
		if _, err := a.AddToCart(ctx, "RX-001", 1); !errors.Is(err, commerce.ErrPrescriptionRequired) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("add, re-price, update, remove", func(t *testing.T) {
		c, err := a.AddToCart(ctx, "PAIN-001", 2)
		if err != nil {
			t.Fatal(err)
		}
		if c.Subtotal != commerce.SAR(9.90) || !c.Lines[0].OnSale {
			t.Fatalf("2 x 4.95 sale price: got %s", c.Subtotal)
		}
		c, _ = a.AddToCart(ctx, "PAIN-001", 1)
		if c.Lines[0].Qty != 3 {
			t.Fatalf("add should accumulate, got qty %d", c.Lines[0].Qty)
		}

		// Sale ends: the same cart re-prices at full price on the next read.
		a.Now = func() time.Time { return time.Date(2028, 1, 2, 0, 0, 0, 0, time.UTC) }
		c, _ = a.GetCart(ctx)
		if c.Subtotal != commerce.SAR(19.50) {
			t.Fatalf("3 x 6.50 after sale: got %s", c.Subtotal)
		}
		a.Now = func() time.Time { return testNow }

		c, _ = a.UpdateCartItem(ctx, "PAIN-001", 1)
		if c.ItemCount != 1 {
			t.Fatalf("item count %d", c.ItemCount)
		}
		c, _ = a.UpdateCartItem(ctx, "PAIN-001", 0)
		if len(c.Lines) != 0 {
			t.Fatalf("qty 0 should remove, got %+v", c.Lines)
		}
		if _, err := a.RemoveFromCart(ctx, "PAIN-001"); !errors.Is(err, commerce.ErrNotInCart) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("quantity limits", func(t *testing.T) {
		for _, q := range []int{0, -1, commerce.MaxLineQty + 1} {
			if _, err := a.AddToCart(ctx, "PAIN-002", q); !errors.Is(err, commerce.ErrInvalidQty) {
				t.Errorf("qty %d: got %v", q, err)
			}
		}
	})

	t.Run("stock checked in session city", func(t *testing.T) {
		var sku string
		for _, p := range a.products {
			if p.Enabled() && !p.RequiresPrescription && p.QtyIn(commerce.CityJeddah) == 0 {
				sku = p.SKU
				break
			}
		}
		if sku == "" {
			t.Skip("no product sold out in Jeddah")
		}
		if _, err := a.AddToCart(ctx, sku, 1); !errors.Is(err, commerce.ErrOutOfStock) {
			t.Fatalf("%s: got %v", sku, err)
		}
	})

	t.Run("carts are isolated by session", func(t *testing.T) {
		_, _ = a.AddToCart(ctx, "PAIN-002", 1)
		other := commerce.WithSession(context.Background(), commerce.Session{CartID: "cart-2", City: commerce.CityJeddah})
		c, _ := a.GetCart(other)
		if len(c.Lines) != 0 {
			t.Fatalf("cart-2 sees cart-1 items: %+v", c.Lines)
		}
	})
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"أقراص":     "اقراص",
		"إيبو":      "ايبو",
		"مُرطِّب":   "مرطب",
		"فيتامين ٣": "فيتامين 3",
		"قطرة":      "قطره",
		"PARA":      "para",
	}
	for in, want := range cases {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSearchRanksNameMatchesFirst(t *testing.T) {
	a := load(t)
	r, _ := a.SearchProducts(context.Background(), commerce.SearchQuery{Text: "paracetamol"})
	if r.Items[0].Category != "pain-relief" {
		t.Fatalf("top hit %s (%s): plain paracetamol should beat combination products", r.Items[0].SKU, r.Items[0].Name)
	}
}
