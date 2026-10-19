package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"shopper/internal/commerce"
	"shopper/internal/commerce/mock"
)

var testNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

// fakeLLM replays scripted responses and records every request.
type fakeLLM struct {
	t        *testing.T
	replies  []string // raw Messages API response bodies
	requests []anthropic.MessageNewParams
	err      error
}

func (f *fakeLLM) New(_ context.Context, p anthropic.MessageNewParams, _ ...option.RequestOption) (*anthropic.Message, error) {
	f.requests = append(f.requests, p)
	if f.err != nil {
		return nil, f.err
	}
	if len(f.replies) == 0 {
		f.t.Fatal("unexpected model call")
	}
	var m anthropic.Message
	if err := json.Unmarshal([]byte(f.replies[0]), &m); err != nil {
		f.t.Fatalf("bad scripted reply: %v", err)
	}
	f.replies = f.replies[1:]
	return &m, nil
}

func toolUse(id, name, input string) string {
	return fmt.Sprintf(`{"id":"msg_%s","type":"message","role":"assistant","model":"m",
		"content":[{"type":"tool_use","id":%q,"name":%q,"input":%s}],
		"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`, id, id, name, input)
}

func endTurn(text string) string {
	return fmt.Sprintf(`{"id":"msg_end","type":"message","role":"assistant","model":"m",
		"content":[{"type":"text","text":%q}],
		"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, text)
}

type sentToolResult struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	IsError   bool   `json:"is_error"`
	Content   []struct {
		Text string `json:"text"`
	} `json:"content"`
}

// toolResults decodes the tool_result blocks the agent sent in request i.
func (f *fakeLLM) toolResults(i int) []sentToolResult {
	f.t.Helper()
	msgs := f.requests[i].Messages
	b, err := json.Marshal(msgs[len(msgs)-1])
	if err != nil {
		f.t.Fatal(err)
	}
	var m struct {
		Content []sentToolResult `json:"content"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		f.t.Fatal(err)
	}
	var out []sentToolResult
	for _, c := range m.Content {
		if c.Type == "tool_result" {
			out = append(out, c)
		}
	}
	return out
}

func setup(t *testing.T, replies ...string) (*Agent, *fakeLLM, *mock.Adapter) {
	t.Helper()
	store, err := mock.LoadFile("../../data/products.json")
	if err != nil {
		t.Fatal(err)
	}
	store.Now = func() time.Time { return testNow }
	llm := &fakeLLM{t: t, replies: replies}
	return newAgent(llm, DefaultModel, store, func() time.Time { return testNow }), llm, store
}

func session(cart string) context.Context {
	return commerce.WithSession(context.Background(), commerce.Session{
		CartID: cart, City: commerce.CityRiyadh, Locale: commerce.LocaleEN,
	})
}

func TestChatSearchThenAddToCart(t *testing.T) {
	a, llm, store := setup(t,
		toolUse("t1", "search_products", `{"query":"paracetamol"}`),
		toolUse("t2", "add_to_cart", `{"sku":"PAIN-001","qty":2}`),
		endTurn("Added 2 × Sahra Paracetamol. Subtotal 9.90 SAR."),
	)
	ctx := session("cart-a")

	reply, err := a.Chat(ctx, "add two packs of paracetamol")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "9.90") {
		t.Errorf("reply %q", reply)
	}
	if len(llm.requests) != 3 {
		t.Fatalf("model calls: %d", len(llm.requests))
	}

	// Request 0: model, tools and system prompt are set; the customer text
	// arrives with the store context note.
	p := llm.requests[0]
	if p.Model != DefaultModel || len(p.Tools) != 5 || len(p.System) != 1 {
		t.Fatalf("params: model %q, %d tools, %d system blocks", p.Model, len(p.Tools), len(p.System))
	}
	first, _ := json.Marshal(p.Messages[0])
	if !strings.Contains(string(first), "Delivery city: riyadh") || !strings.Contains(string(first), "add two packs") {
		t.Errorf("first user message %s", first)
	}

	// Request 1 carries the search result for t1, priced by the backend.
	res := llm.toolResults(1)
	if len(res) != 1 || res[0].ToolUseID != "t1" || res[0].IsError {
		t.Fatalf("search result %+v", res)
	}
	if !strings.Contains(res[0].Content[0].Text, `"sku":"PAIN-001"`) || !strings.Contains(res[0].Content[0].Text, `"price_sar":4.95`) {
		t.Errorf("search result text %s", res[0].Content[0].Text)
	}

	// The cart really changed, through the adapter.
	c, err := store.GetCart(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Lines) != 1 || c.Lines[0].Qty != 2 || c.Subtotal != commerce.SAR(9.90) {
		t.Fatalf("cart %+v", c)
	}

	// History is append-only: request 2 starts with request 1's messages.
	r1, _ := json.Marshal(llm.requests[1].Messages)
	r2, _ := json.Marshal(llm.requests[2].Messages[:len(llm.requests[1].Messages)])
	if string(r1) != string(r2) {
		t.Error("history was rewritten between calls")
	}
}

func TestPrescriptionGuardrail(t *testing.T) {
	a, llm, store := setup(t,
		toolUse("t1", "add_to_cart", `{"sku":"RX-001"}`),
		endTurn("That needs a prescription."),
	)
	ctx := session("cart-rx")
	if _, err := a.Chat(ctx, "add amoxicillin"); err != nil {
		t.Fatal(err)
	}
	res := llm.toolResults(1)
	if len(res) != 1 || !res[0].IsError || !strings.Contains(res[0].Content[0].Text, "prescription") {
		t.Fatalf("tool result %+v", res)
	}
	c, _ := store.GetCart(ctx)
	if len(c.Lines) != 0 {
		t.Fatalf("Rx product reached the cart: %+v", c)
	}
}

func TestModelCannotOverrideSessionFields(t *testing.T) {
	for _, input := range []string{
		`{"sku":"PAIN-001","city":"jeddah"}`,
		`{"sku":"PAIN-001","cart_id":"someone-else"}`,
		`{"sku":"PAIN-001","price":0.01}`,
	} {
		a, llm, store := setup(t, toolUse("t1", "add_to_cart", input), endTurn("ok"))
		ctx := session("cart-x")
		if _, err := a.Chat(ctx, "hi"); err != nil {
			t.Fatal(err)
		}
		res := llm.toolResults(1)
		if !res[0].IsError || !strings.Contains(res[0].Content[0].Text, "invalid arguments") {
			t.Errorf("%s: want invalid arguments, got %+v", input, res)
		}
		if c, _ := store.GetCart(ctx); len(c.Lines) != 0 {
			t.Errorf("%s: cart changed: %+v", input, c)
		}
	}
}

func TestToolViewsHideInternals(t *testing.T) {
	a, _, _ := setup(t)
	ctx := session("cart-v")
	for _, tc := range []struct{ name, input string }{
		{"search_products", `{"query":"vitamin"}`},
		{"get_product", `{"sku":"PAIN-001"}`},
		{"check_availability", `{"sku":"PAIN-001"}`},
		{"view_cart", `{}`},
	} {
		out, isErr := a.tools.call(ctx, tc.name, json.RawMessage(tc.input))
		if isErr {
			t.Fatalf("%s: %s", tc.name, out)
		}
		for _, leak := range []string{`"stock"`, `"qty_`, `"by_city"`, `"status"`, `"special_price"`, `"cart_id"`, `"id"`} {
			if strings.Contains(out, leak) {
				t.Errorf("%s leaks %s: %s", tc.name, leak, out)
			}
		}
	}
	if out, isErr := a.tools.call(ctx, "get_product", json.RawMessage(`{"sku":"COLD-005"}`)); !isErr || !strings.Contains(out, "No product") {
		t.Errorf("disabled product: %v %s", isErr, out)
	}
}

func TestFailedCallLeavesHistoryClean(t *testing.T) {
	a, llm, _ := setup(t)
	ctx := session("cart-f")
	llm.err = errors.New("boom")
	if _, err := a.Chat(ctx, "hello"); err == nil {
		t.Fatal("want error")
	}
	llm.err = nil
	llm.replies = []string{endTurn("hi")}
	if _, err := a.Chat(ctx, "hello again"); err != nil {
		t.Fatal(err)
	}
	if n := len(llm.requests[1].Messages); n != 1 {
		t.Fatalf("history after failed turn has %d messages, want 1", n)
	}
}

func TestConversationsAreIsolatedAndResettable(t *testing.T) {
	a, llm, _ := setup(t, endTurn("a1"), endTurn("b1"), endTurn("a2"), endTurn("a3"))
	alice, bob := session("cart-alice"), session("cart-bob")
	a.Chat(alice, "one")
	a.Chat(bob, "two")
	a.Chat(alice, "three")
	if n := len(llm.requests[1].Messages); n != 1 {
		t.Errorf("bob sees %d messages, want 1", n)
	}
	if n := len(llm.requests[2].Messages); n != 3 {
		t.Errorf("alice's second turn has %d messages, want 3", n)
	}
	if err := a.Reset(alice); err != nil {
		t.Fatal(err)
	}
	a.Chat(alice, "four")
	if n := len(llm.requests[3].Messages); n != 1 {
		t.Errorf("after reset: %d messages, want 1", n)
	}
}

func TestStepLimitAndInputChecks(t *testing.T) {
	var loop []string
	for i := 0; i < maxSteps; i++ {
		loop = append(loop, toolUse(fmt.Sprint("t", i), "view_cart", `{}`))
	}
	a, _, _ := setup(t, loop...)
	if _, err := a.Chat(session("cart-l"), "hi"); err == nil {
		t.Error("want step-limit error")
	}

	a, _, _ = setup(t)
	if _, err := a.Chat(session("c"), ""); !errors.Is(err, ErrEmptyMessage) {
		t.Errorf("empty: %v", err)
	}
	if _, err := a.Chat(session("c"), strings.Repeat("x", maxMessageLen+1)); !errors.Is(err, ErrMessageTooLong) {
		t.Errorf("too long: %v", err)
	}
	if _, err := a.Chat(context.Background(), "hi"); !errors.Is(err, commerce.ErrNoSession) {
		t.Errorf("no session: %v", err)
	}
}
