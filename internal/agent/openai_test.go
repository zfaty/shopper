package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shopper/internal/commerce/mock"
)

// fakeOpenAI replays chat completions responses and records request bodies.
func fakeOpenAI(t *testing.T, replies ...string) (*httptest.Server, *[]oaiRequest) {
	t.Helper()
	var reqs []oaiRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("request %s auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		b, _ := io.ReadAll(r.Body)
		var req oaiRequest
		if err := json.Unmarshal(b, &req); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		reqs = append(reqs, req)
		if len(replies) == 0 {
			t.Error("unexpected model call")
			http.Error(w, "no more replies", 500)
			return
		}
		io.WriteString(w, replies[0])
		replies = replies[1:]
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

func setupOpenAI(t *testing.T, replies ...string) (*Agent, *[]oaiRequest, *mock.Adapter) {
	t.Helper()
	store, err := mock.LoadFile("../../data/products.json")
	if err != nil {
		t.Fatal(err)
	}
	store.Now = func() time.Time { return testNow }
	srv, reqs := fakeOpenAI(t, replies...)
	llm := NewOpenAICompat(srv.URL+"/v1/", "k")
	return newAgent(llm, "free-model", store, func() time.Time { return testNow }), reqs, store
}

func TestOpenAICompatToolLoop(t *testing.T) {
	a, reqs, store := setupOpenAI(t,
		// Arguments as a JSON string (the spec) and finish_reason "stop"
		// despite the tool call (as some servers do).
		`{"id":"c1","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":null,
			"tool_calls":[{"id":"call_1","type":"function","function":{"name":"add_to_cart","arguments":"{\"sku\":\"PAIN-001\",\"qty\":2}"}}]}}]}`,
		`{"id":"c2","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Added 2. Subtotal 9.90 SAR."}}]}`,
	)
	ctx := session("cart-o")

	reply, err := a.Chat(ctx, "add two paracetamol")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Added 2. Subtotal 9.90 SAR." {
		t.Errorf("reply %q", reply)
	}
	cart, err := store.GetCart(ctx)
	if err != nil || len(cart.Lines) != 1 || cart.Lines[0].Qty != 2 {
		t.Fatalf("cart %+v err %v", cart, err)
	}

	if len(*reqs) != 2 {
		t.Fatalf("model calls: %d", len(*reqs))
	}
	first := (*reqs)[0]
	if first.Model != "free-model" || len(first.Tools) != 5 || first.Tools[0].Type != "function" {
		t.Fatalf("first request: model %q, %d tools", first.Model, len(first.Tools))
	}
	if len(first.Messages) != 2 || first.Messages[0].Role != "system" || first.Messages[1].Role != "user" ||
		!strings.Contains(*first.Messages[1].Content, "Delivery city: riyadh") {
		t.Fatalf("first messages %+v", first.Messages)
	}

	// Second request: system, user, assistant tool call, tool result.
	m := (*reqs)[1].Messages
	if len(m) != 4 || m[2].Role != "assistant" || len(m[2].ToolCalls) != 1 || m[3].Role != "tool" || m[3].ToolCallID != "call_1" {
		t.Fatalf("second messages %+v", m)
	}
	var args string
	if err := json.Unmarshal(m[2].ToolCalls[0].Function.Arguments, &args); err != nil || !strings.Contains(args, "PAIN-001") {
		t.Errorf("echoed arguments %s", m[2].ToolCalls[0].Function.Arguments)
	}
	if !strings.Contains(*m[3].Content, "PAIN-001") || strings.HasPrefix(*m[3].Content, "ERROR") {
		t.Errorf("tool result %q", *m[3].Content)
	}
}

func TestOpenAICompatBadArgumentsReachModelAsError(t *testing.T) {
	a, reqs, _ := setupOpenAI(t,
		`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant",
			"tool_calls":[{"id":"","type":"function","function":{"name":"view_cart","arguments":"not json"}}]}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Your cart is empty."}}]}`,
	)
	if _, err := a.Chat(session("cart-b"), "what's in my cart?"); err != nil {
		t.Fatal(err)
	}
	m := (*reqs)[1].Messages
	last := m[len(m)-1]
	if last.Role != "tool" || !strings.HasPrefix(last.ToolCallID, "call_") || !strings.HasPrefix(*last.Content, "ERROR") {
		t.Errorf("tool message %+v %q", last, *last.Content)
	}
}

func TestOpenAICompatHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()
	store, err := mock.LoadFile("../../data/products.json")
	if err != nil {
		t.Fatal(err)
	}
	a := NewWithLLM(NewOpenAICompat(srv.URL, ""), "m", store)
	_, err = a.Chat(session("cart-e"), "hi")
	if err == nil || !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("err %v", err)
	}
}

func TestCoerceArgs(t *testing.T) {
	var s toolSchema
	if err := json.Unmarshal([]byte(`{"type":"object","properties":{
		"query":{"type":"string"},"category":{"type":"string"},"max_price_sar":{"type":"number"},
		"in_stock_only":{"type":"boolean"},"qty":{"type":"integer"}},"required":["query"]}`), &s); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ in, want string }{
		// What llama3.2:3b actually sent.
		{`{"query":"paracetamol","in_stock_only":"false","max_price_sar":"null","category":"null"}`,
			`{"in_stock_only":false,"query":"paracetamol"}`},
		{`{"query":"x","qty":"2","max_price_sar":"12.5"}`, `{"max_price_sar":12.5,"qty":2,"query":"x"}`},
		{`{"query":"x","qty":2.0,"category":null}`, `{"qty":2,"query":"x"}`},
		// Already fine: returned unchanged, byte for byte.
		{`{"query":"x","qty":2}`, `{"query":"x","qty":2}`},
		// Unknown fields and unconvertible values are left for the strict decoder.
		{`{"query":"x","price":"1","qty":"two"}`, `{"query":"x","price":"1","qty":"two"}`},
		// A required field is never dropped, even if it looks null.
		{`{"query":"null"}`, `{"query":"null"}`},
	}
	for _, c := range cases {
		if got := string(coerceArgs("t", json.RawMessage(c.in), s)); got != c.want {
			t.Errorf("coerceArgs(%s) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestOpenAICompatCoercesSmallModelArguments(t *testing.T) {
	a, reqs, store := setupOpenAI(t,
		`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant",
			"tool_calls":[{"id":"c1","type":"function","function":{"name":"add_to_cart","arguments":"{\"qty\":\"2\",\"sku\":\"PAIN-001\"}"}}]}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Added."}}]}`,
	)
	ctx := session("cart-c")
	if _, err := a.Chat(ctx, "add 2"); err != nil {
		t.Fatal(err)
	}
	cart, err := store.GetCart(ctx)
	if err != nil || len(cart.Lines) != 1 || cart.Lines[0].Qty != 2 {
		t.Fatalf("cart %+v err %v", cart, err)
	}
	if m := (*reqs)[1].Messages; strings.HasPrefix(*m[len(m)-1].Content, "ERROR") {
		t.Errorf("tool result %q", *m[len(m)-1].Content)
	}
}

func TestOpenAICompatEchoesThoughtSignature(t *testing.T) {
	a, reqs, _ := setupOpenAI(t,
		`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant",
			"tool_calls":[{"id":"g1","type":"function","function":{"name":"view_cart","arguments":"{}"},
				"extra_content":{"google":{"thought_signature":"sig-abc"}}}]}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Empty."}}]}`,
	)
	if _, err := a.Chat(session("cart-g"), "cart?"); err != nil {
		t.Fatal(err)
	}
	m := (*reqs)[1].Messages
	call := m[len(m)-2].ToolCalls[0]
	if call.ID != "g1" || !strings.Contains(string(call.ExtraContent), `"thought_signature":"sig-abc"`) {
		t.Errorf("echoed call %+v extra %s", call, call.ExtraContent)
	}
}
