// Command server runs the shopper demo: product, cart and chat API. Chat UI
// later.
//
//	go run ./cmd/server                       # :8080, data/products.json
//	go run ./cmd/server -addr :9000 -data path/to/products.json
//	go run ./cmd/server -provider gemini -model gemini-2.5-flash   # free tier, GEMINI_API_KEY
//	go run ./cmd/server -provider ollama -model qwen3:8b           # local, no key
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"shopper/internal/agent"
	"shopper/internal/api"
	"shopper/internal/commerce/mock"
)

func main() {
	addr := flag.String("addr", ":8090", "listen address")
	dataPath := flag.String("data", "data/products.json", "catalogue for the mock adapter")
	provider := flag.String("provider", "anthropic", "model backend: anthropic, gemini, groq, openrouter, ollama, openai (any OpenAI-compatible API, with -base-url)")
	model := flag.String("model", "", "model ID (default "+agent.DefaultModel+" for anthropic; required otherwise)")
	baseURL := flag.String("base-url", "", "override the provider's OpenAI-compatible base URL")
	envFile := flag.String("env", ".env", "KEY=value file with API keys; real environment variables win")
	flag.Parse()

	// Log key names only, never values.
	if keys, err := loadEnvFile(*envFile); err != nil {
		slog.Error("env file", "err", err)
		os.Exit(2)
	} else if len(keys) > 0 {
		slog.Info("env file", "path", *envFile, "set", keys)
	}
	// Flags win; otherwise LLM_PROVIDER, LLM_MODEL and LLM_BASE_URL (from the
	// shell or .env) choose the backend, so switching models is a .env edit.
	// A -provider naming a different backend ignores LLM_MODEL and
	// LLM_BASE_URL, which belong to the .env one.
	envDefault(provider, "provider", "LLM_PROVIDER")
	if *provider == os.Getenv("LLM_PROVIDER") {
		envDefault(model, "model", "LLM_MODEL")
		envDefault(baseURL, "base-url", "LLM_BASE_URL")
	}

	store, err := mock.LoadFile(*dataPath)
	if err != nil {
		slog.Error("load catalogue", "err", err)
		os.Exit(1)
	}
	slog.Info("backend", "provider", *provider)
	chatter, err := newAgent(*provider, *model, *baseURL, store)
	if err != nil {
		slog.Error("agent", "err", err)
		os.Exit(2)
	}
	handler := api.New(store)
	handler.SetChatter(chatter)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           logRequests(handler),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      3 * time.Minute, // several model calls; local models are slow
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		slog.Info("listening", "addr", *addr, "catalogue", *dataPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	slog.Info("stopped")
}

// envDefault sets *v from the environment variable unless the flag was given.
func envDefault(v *string, flagName, env string) {
	if val := os.Getenv(env); !flagGiven(flagName) && val != "" {
		*v = val
	}
}

func flagGiven(name string) bool {
	given := false
	flag.Visit(func(f *flag.Flag) { given = given || f.Name == name })
	return given
}

// openAIProviders are the OpenAI-compatible presets: base URL and the env
// var holding the key ("" = no key needed).
var openAIProviders = map[string]struct{ baseURL, keyEnv string }{
	"gemini":     {"https://generativelanguage.googleapis.com/v1beta/openai", "GEMINI_API_KEY"},
	"groq":       {"https://api.groq.com/openai/v1", "GROQ_API_KEY"},
	"openrouter": {"https://openrouter.ai/api/v1", "OPENROUTER_API_KEY"},
	"ollama":     {"http://localhost:11434/v1", ""},
	"openai":     {"", "OPENAI_API_KEY"},
}

func newAgent(provider, model, baseURL string, store *mock.Adapter) (*agent.Agent, error) {
	if provider == "anthropic" {
		if model == "" {
			model = agent.DefaultModel
		}
		slog.Info("agent", "provider", provider, "model", model)
		// The client reads ANTHROPIC_API_KEY (or another configured
		// credential) on first use; without one, /chat returns 502 and logs why.
		return agent.New(anthropic.NewClient(), model, store), nil
	}
	p, ok := openAIProviders[provider]
	if !ok {
		return nil, fmt.Errorf("unknown -provider %q", provider)
	}
	if model == "" {
		return nil, fmt.Errorf("-model is required with -provider %s", provider)
	}
	if baseURL == "" {
		baseURL = p.baseURL
	}
	if baseURL == "" {
		return nil, fmt.Errorf("-base-url is required with -provider %s", provider)
	}
	var key string
	if p.keyEnv != "" {
		if key = os.Getenv(p.keyEnv); key == "" {
			slog.Warn("agent: API key not set; /chat will fail", "env", p.keyEnv)
		}
	}
	slog.Info("agent", "provider", provider, "model", model, "base_url", baseURL)
	return agent.NewWithLLM(agent.NewOpenAICompat(baseURL, key), model, store), nil
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery,
			"status", rec.status, "dur", time.Since(start).Round(time.Microsecond))
	})
}
