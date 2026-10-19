package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// OpenAICompat lets the agent run on any OpenAI-compatible chat completions
// API (Gemini, Groq, OpenRouter, Ollama, ...). It translates Anthropic
// requests to chat completions and the replies back, so the loop, history and
// tools stay written against the Anthropic types and switching back is a
// flag. Anthropic-only features (prompt caching, thinking, effort) are
// dropped on the way out.
type OpenAICompat struct {
	BaseURL string // e.g. http://localhost:11434/v1; "/chat/completions" is appended
	APIKey  string // sent as a Bearer token when set
	HTTP    *http.Client

	// extra keeps each tool call's provider-specific extra_content (Gemini 3's
	// thought_signature) by call ID, to send back with the call on the next
	// request. Anthropic tool_use blocks have nowhere to carry it.
	mu     sync.Mutex
	extra  map[string]json.RawMessage
	nextID atomic.Int64
}

// NewOpenAICompat returns a client for baseURL with a 2-minute timeout.
func NewOpenAICompat(baseURL, apiKey string) *OpenAICompat {
	return &OpenAICompat{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 2 * time.Minute},
	}
}

// Anthropic request shape, decoded from the SDK params' own JSON so we don't
// depend on the SDK's union internals.
type antRequest struct {
	Model  string `json:"model"`
	System []struct {
		Text string `json:"text"`
	} `json:"system"`
	Messages []struct {
		Role    string     `json:"role"`
		Content []antBlock `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	} `json:"tools"`
}

type antBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // tool_result: string or text blocks
	IsError   bool            `json:"is_error,omitempty"`
}

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    *string       `json:"content"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type oaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"` // a JSON string per spec; some servers send an object
	} `json:"function"`
	ExtraContent json.RawMessage `json:"extra_content,omitempty"`
}

type oaiTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type oaiRequest struct {
	Model    string       `json:"model"`
	Messages []oaiMessage `json:"messages"`
	Tools    []oaiTool    `json:"tools,omitempty"`
}

type oaiResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message      oaiMessage `json:"message"`
		FinishReason string     `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// New implements the agent's LLM interface.
func (c *OpenAICompat) New(ctx context.Context, params anthropic.MessageNewParams, _ ...option.RequestOption) (*anthropic.Message, error) {
	body, schemas, err := c.toOpenAI(params)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("openai-compat: %s: %s", res.Status, truncate(string(raw), 500))
	}
	var out oaiResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("openai-compat: decode response: %w", err)
	}
	return c.fromOpenAI(out, params.Model, schemas)
}

// toolSchema is the part of a tool's input schema used to coerce arguments.
type toolSchema struct {
	Properties map[string]struct {
		Type string `json:"type"`
	} `json:"properties"`
	Required []string `json:"required"`
}

func (c *OpenAICompat) toOpenAI(params anthropic.MessageNewParams) ([]byte, map[string]toolSchema, error) {
	b, err := json.Marshal(params)
	if err != nil {
		return nil, nil, err
	}
	var in antRequest
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, nil, fmt.Errorf("openai-compat: decode params: %w", err)
	}

	out := oaiRequest{Model: in.Model}
	var sys []string
	for _, s := range in.System {
		sys = append(sys, s.Text)
	}
	if len(sys) > 0 {
		out.Messages = append(out.Messages, oaiMessage{Role: "system", Content: ptr(strings.Join(sys, "\n\n"))})
	}

	for _, m := range in.Messages {
		var texts []string
		switch m.Role {
		case "assistant":
			msg := oaiMessage{Role: "assistant"}
			for _, blk := range m.Content {
				switch blk.Type {
				case "text":
					texts = append(texts, blk.Text)
				case "tool_use":
					tc := oaiToolCall{ID: blk.ID, Type: "function", ExtraContent: c.extraFor(blk.ID)}
					tc.Function.Name = blk.Name
					args, _ := json.Marshal(string(blk.Input))
					tc.Function.Arguments = args
					msg.ToolCalls = append(msg.ToolCalls, tc)
				} // thinking blocks are Anthropic-only; skip
			}
			if len(texts) > 0 {
				msg.Content = ptr(strings.Join(texts, "\n\n"))
			}
			out.Messages = append(out.Messages, msg)
		default: // user: tool results become "tool" messages, text a user message after them
			for _, blk := range m.Content {
				switch blk.Type {
				case "text":
					texts = append(texts, blk.Text)
				case "tool_result":
					text := toolResultText(blk.Content)
					if blk.IsError {
						text = "ERROR: " + text
					}
					out.Messages = append(out.Messages, oaiMessage{Role: "tool", ToolCallID: blk.ToolUseID, Content: ptr(text)})
				}
			}
			if len(texts) > 0 {
				out.Messages = append(out.Messages, oaiMessage{Role: "user", Content: ptr(strings.Join(texts, "\n\n"))})
			}
		}
	}

	schemas := map[string]toolSchema{}
	for _, t := range in.Tools {
		var ts toolSchema
		_ = json.Unmarshal(t.InputSchema, &ts)
		schemas[t.Name] = ts

		var tool oaiTool
		tool.Type = "function"
		tool.Function.Name = t.Name
		tool.Function.Description = t.Description
		tool.Function.Parameters = t.InputSchema
		out.Tools = append(out.Tools, tool)
	}
	body, err := json.Marshal(out)
	return body, schemas, err
}

func (c *OpenAICompat) keepExtra(id string, extra json.RawMessage) {
	if len(extra) == 0 || string(extra) == "null" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.extra == nil {
		c.extra = map[string]json.RawMessage{}
	}
	c.extra[id] = extra
}

func (c *OpenAICompat) extraFor(id string) json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.extra[id]
}

func toolResultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &blocks)
	var parts []string
	for _, b := range blocks {
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n")
}

// fromOpenAI builds the Anthropic response JSON and lets the SDK decode it,
// the same way the tests build scripted replies.
func (c *OpenAICompat) fromOpenAI(r oaiResponse, model string, schemas map[string]toolSchema) (*anthropic.Message, error) {
	if len(r.Choices) == 0 {
		return nil, fmt.Errorf("openai-compat: response has no choices")
	}
	choice := r.Choices[0]
	content := []antBlock{}
	if choice.Message.Content != nil && strings.TrimSpace(*choice.Message.Content) != "" {
		content = append(content, antBlock{Type: "text", Text: *choice.Message.Content})
	}
	for _, tc := range choice.Message.ToolCalls {
		id := tc.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", c.nextID.Add(1)) // unique: extra is keyed by it
		}
		c.keepExtra(id, tc.ExtraContent)
		content = append(content, antBlock{Type: "tool_use", ID: id, Name: tc.Function.Name, Input: coerceArgs(tc.Function.Name, toolArgs(tc.Function.Arguments), schemas[tc.Function.Name])})
	}

	// Some servers report "stop" even when they called tools, so go by the
	// content rather than finish_reason.
	stop := anthropic.StopReasonEndTurn
	switch {
	case len(choice.Message.ToolCalls) > 0:
		stop = anthropic.StopReasonToolUse
	case choice.FinishReason == "length":
		stop = anthropic.StopReasonMaxTokens
	}

	id := r.ID
	if id == "" {
		id = "msg_openai"
	}
	b, err := json.Marshal(map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": model,
		"content": content, "stop_reason": stop,
		"usage": map[string]int{"input_tokens": r.Usage.PromptTokens, "output_tokens": r.Usage.CompletionTokens},
	})
	if err != nil {
		return nil, err
	}
	var m anthropic.Message
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("openai-compat: build message: %w", err)
	}
	return &m, nil
}

// toolArgs returns the tool input as a JSON object. Arguments are normally a
// JSON-encoded string; invalid JSON is passed on under a field the strict
// tool decoder rejects, so the model sees an error and can retry.
func toolArgs(raw json.RawMessage) json.RawMessage {
	args := []byte(raw)
	var s string
	if json.Unmarshal(raw, &s) == nil {
		args = []byte(s)
	}
	if strings.TrimSpace(string(args)) == "" {
		return json.RawMessage("{}")
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(args, &obj) != nil {
		b, _ := json.Marshal(map[string]string{"invalid_arguments": string(args)})
		return b
	}
	return args
}

// coerceArgs fixes the type slips small models make, using the tool's own
// schema: "2" for an integer, "false" for a boolean, "null" or null for an
// optional field. Only declared properties are touched; unknown fields
// (price, city, cart_id) pass through for the strict decoder to reject, and
// anything it can't convert is left for that decoder to report.
func coerceArgs(tool string, args json.RawMessage, s toolSchema) json.RawMessage {
	dec := json.NewDecoder(strings.NewReader(string(args)))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || len(s.Properties) == 0 {
		return args
	}
	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
	}
	var fixed []string
	for key, v := range m {
		prop, ok := s.Properties[key]
		if !ok {
			continue
		}
		if str, isStr := v.(string); v == nil || isStr && isNullish(str) {
			if !required[key] {
				delete(m, key)
				fixed = append(fixed, key)
			}
			continue
		}
		if nv, ok := coerceValue(v, prop.Type); ok {
			m[key] = nv
			fixed = append(fixed, key)
		}
	}
	if len(fixed) == 0 {
		return args
	}
	b, err := json.Marshal(m)
	if err != nil {
		return args
	}
	slog.Info("openai-compat: coerced tool arguments", "tool", tool, "fields", fixed)
	return b
}

func isNullish(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "null", "none", "nil", "undefined":
		return true
	}
	return false
}

// coerceValue converts v to the schema type; ok is false when v is already
// fine or can't be converted.
func coerceValue(v any, typ string) (any, bool) {
	str, isStr := v.(string)
	str = strings.TrimSpace(str)
	switch typ {
	case "integer":
		if isStr {
			if n, err := strconv.ParseInt(str, 10, 64); err == nil {
				return n, true
			}
			if f, err := strconv.ParseFloat(str, 64); err == nil && f == float64(int64(f)) {
				return int64(f), true
			}
		}
		if n, isNum := v.(json.Number); isNum {
			if _, err := n.Int64(); err != nil {
				if f, err := n.Float64(); err == nil && f == float64(int64(f)) {
					return int64(f), true // 2.0
				}
			}
		}
	case "number":
		if isStr {
			if f, err := strconv.ParseFloat(str, 64); err == nil {
				return f, true
			}
		}
	case "boolean":
		if isStr {
			if b, err := strconv.ParseBool(strings.ToLower(str)); err == nil {
				return b, true
			}
		}
	}
	return nil, false
}

func ptr(s string) *string { return &s }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
