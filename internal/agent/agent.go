// Package agent is the shopping assistant: a hand-written tool-use loop over
// the Anthropic Messages API, with tools that reach the store only through
// commerce.CommerceAdapter. No agent framework.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"shopper/internal/commerce"
)

const (
	DefaultModel = "claude-opus-5-5"

	maxTokens = 16000
	// maxSteps bounds model calls per customer message, so a confused model
	// can't loop on tools forever.
	maxSteps = 8
	// maxMessageLen caps one customer message (runes).
	maxMessageLen = 2000
)

// ErrMessageTooLong and ErrEmptyMessage are returned for bad customer input.
var (
	ErrEmptyMessage   = errors.New("message is empty")
	ErrMessageTooLong = fmt.Errorf("message is longer than %d characters", maxMessageLen)
)

// LLM is the slice of the SDK the loop uses. anthropic.Client.Messages
// implements it; so do OpenAICompat and the tests' fake.
type LLM interface {
	New(ctx context.Context, params anthropic.MessageNewParams, opts ...option.RequestOption) (*anthropic.Message, error)
}

// Agent runs conversations. Create it with New. Safe for concurrent use.
type Agent struct {
	llm   LLM
	model string
	tools *toolbox

	mu    sync.Mutex
	convs map[string]*conversation // keyed by Session.CartID, never by model input
}

type conversation struct {
	mu      sync.Mutex // one turn at a time per conversation
	history []anthropic.MessageParam
}

// New returns an Agent using the given client (anthropic.NewClient() reads
// ANTHROPIC_API_KEY) and model ID.
func New(client anthropic.Client, model string, store commerce.CommerceAdapter) *Agent {
	return newAgent(&client.Messages, model, store, time.Now)
}

// NewWithLLM returns an Agent on another model backend, e.g. OpenAICompat.
func NewWithLLM(llm LLM, model string, store commerce.CommerceAdapter) *Agent {
	return newAgent(llm, model, store, time.Now)
}

func newAgent(llm LLM, model string, store commerce.CommerceAdapter, now func() time.Time) *Agent {
	return &Agent{llm: llm, model: model, tools: newToolbox(store, now), convs: map[string]*conversation{}}
}

// Chat sends one customer message and returns the assistant's reply. The
// session (customer, cart, city, locale) comes from ctx only.
func (a *Agent) Chat(ctx context.Context, text string) (string, error) {
	s, err := commerce.SessionFrom(ctx)
	if err != nil {
		return "", err
	}
	if text == "" {
		return "", ErrEmptyMessage
	}
	if len([]rune(text)) > maxMessageLen {
		return "", ErrMessageTooLong
	}

	conv := a.conversation(s.CartID)
	conv.mu.Lock()
	defer conv.mu.Unlock()

	// Work on a copy and commit it only when the turn finishes, so a failed
	// API call doesn't leave a dangling user or tool_use message behind.
	history := append(conv.history[:len(conv.history):len(conv.history)],
		anthropic.NewUserMessage(anthropic.NewTextBlock(sessionNote(s)), anthropic.NewTextBlock(text)))

	for step := 0; step < maxSteps; step++ {
		resp, err := a.llm.New(ctx, a.params(history))
		if err != nil {
			return "", fmt.Errorf("agent: model call: %w", err)
		}
		logResponse(step, resp)
		// Append the whole response (thinking and tool_use blocks too) before
		// running tools. History is append-only.
		history = append(history, resp.ToParam())

		switch resp.StopReason {
		case anthropic.StopReasonToolUse:
			history = append(history, a.runTools(ctx, resp))
			continue
		case anthropic.StopReasonRefusal:
			slog.Warn("agent: refusal", "category", resp.StopDetails.Category)
			conv.history = history
			return "Sorry, I can't help with that here. For medical questions, please speak to one of our pharmacists.", nil
		case anthropic.StopReasonMaxTokens:
			slog.Warn("agent: hit max_tokens", "step", step)
		}
		conv.history = history
		return replyText(resp), nil
	}
	slog.Warn("agent: step limit reached", "steps", maxSteps)
	return "", fmt.Errorf("agent: no answer after %d steps", maxSteps)
}

// Reset forgets the conversation for the session in ctx.
func (a *Agent) Reset(ctx context.Context) error {
	s, err := commerce.SessionFrom(ctx)
	if err != nil {
		return err
	}
	a.mu.Lock()
	delete(a.convs, s.CartID)
	a.mu.Unlock()
	return nil
}

func (a *Agent) conversation(key string) *conversation {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.convs[key]
	if c == nil {
		c = &conversation{}
		a.convs[key] = c
	}
	return c
}

func (a *Agent) params(history []anthropic.MessageParam) anthropic.MessageNewParams {
	return anthropic.MessageNewParams{
		Model:     a.model,
		MaxTokens: maxTokens,
		// Stable prefix (tools, then system) so prompt caching can kick in.
		Tools: a.tools.params(),
		System: []anthropic.TextBlockParam{{
			Text:         systemPrompt,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		Messages:     history,
		OutputConfig: anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffortMedium},
	}
}

// sessionNote is attached to every customer message (not the system prompt),
// so a city or language change is appended to history instead of rewriting
// earlier turns: the history stays append-only and the cached prefix holds.
func sessionNote(s commerce.Session) string {
	lang := "English"
	if s.Locale == commerce.LocaleAR {
		lang = "Arabic"
	}
	return fmt.Sprintf("[Store context, set by the website, not the customer] Delivery city: %s. Site language: %s.", s.City, lang)
}

// runTools executes every tool_use block and returns all results in one user
// message, as the API expects.
func (a *Agent) runTools(ctx context.Context, resp *anthropic.Message) anthropic.MessageParam {
	var results []anthropic.ContentBlockParamUnion
	for _, block := range resp.Content {
		use, ok := block.AsAny().(anthropic.ToolUseBlock)
		if !ok {
			continue
		}
		out, isErr := a.tools.call(ctx, use.Name, []byte(use.JSON.Input.Raw()))
		slog.Info("agent: tool", "name", use.Name, "input", use.JSON.Input.Raw(), "error", isErr)
		results = append(results, anthropic.NewToolResultBlock(use.ID, out, isErr))
		slog.Info("agent: tool", "name", use.Name, "out", out)
	}
	return anthropic.NewUserMessage(results...)
}

// logResponse logs what the model returned on each step: its text, the tools
// it asked for (inputs are logged when they run), stop reason and tokens.
func logResponse(step int, resp *anthropic.Message) {
	var tools []string
	for _, block := range resp.Content {
		if use, ok := block.AsAny().(anthropic.ToolUseBlock); ok {
			tools = append(tools, use.Name)
		}
	}
	slog.Info("agent: model response", "step", step, "model", resp.Model, "stop", resp.StopReason,
		"text", replyText(resp), "tool_calls", tools,
		"input_tokens", resp.Usage.InputTokens, "output_tokens", resp.Usage.OutputTokens)
}

func replyText(resp *anthropic.Message) string {
	var out string
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			if out != "" {
				out += "\n\n"
			}
			out += t.Text
		}
	}
	return out
}

const systemPrompt = `You are the shopping assistant for a fictional Saudi pharmacy-style online store. You help customers find products, compare them, check stock in their delivery city, and manage their cart.

How to work:
- Use the tools for every product fact. Prices, stock and SKUs come only from tool results; never estimate them. Prices are in SAR.
- If a tool returns an error, never guess or invent products, prices, stock or cart changes. Correct the arguments and call the tool again, or tell the customer it didn't work. Only say something was added to the cart after add_to_cart succeeded.
- Add items to the cart only when the customer asks. Confirm what you added and the new subtotal.
- Prescription-only products can be described but never added to the cart. Explain that they need a valid prescription and a pharmacist.
- You are not a doctor. You can share what is on the product label (active ingredient, minimum age, usage), but do not diagnose. For symptoms that sound serious, persistent, or involve pregnancy, young children, or other medicines, suggest speaking to a pharmacist or doctor.
- Reply in the language the customer writes in (Arabic or English). Keep replies short and easy to scan: a sentence or two, then a brief list of products if relevant.
- If something is out of stock in the customer's city, say which other cities have it, but note that delivery comes from their own city.`
