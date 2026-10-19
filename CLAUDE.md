# Shopper: AI shopping agent demo

Side project: an AI shopping agent for a fictional pharmacy-style store
(Saudi market). Built on my own time to learn agent building and demo it to
my manager. **No client code or data. Fictional catalogue and brands only.**

Budget: 25–40 hours over 4–6 weeks, alongside a full-time job. Prefer small,
working increments over big rewrites.

## Stack

- Go, standard library only so far (`net/http` ServeMux with Go 1.22 patterns).
  chi is acceptable if routing gets complicated.
- Agent loop written by hand with an official LLM Go SDK. **No agent framework.**
  Written against Anthropic types; `agent.OpenAICompat` translates to any
  OpenAI-compatible API (free tiers / local Ollama) until there's API credit.
- Chat UI: one HTML page with a little JS or htmx, served by the Go server.
- Later: a `magento` adapter talking to a Magento store over REST.

## Architecture

```
chat UI → internal/api → internal/agent → tools → commerce.CommerceAdapter
                                                    ├── mock    (now)
                                                    └── magento (later)
```

- The agent and API reach store data **only** through `commerce.CommerceAdapter`.
  Nothing outside an adapter touches a database.
- `internal/commerce` depends on nothing else in the repo.

## Layout

```
cmd/server/          HTTP server (go run ./cmd/server)
cmd/gendata/         regenerates data/products.json (deterministic)
internal/commerce/   types.go (domain), adapter.go (interface, errors, Session)
internal/commerce/mock/     in-memory adapter over data/products.json
internal/commerce/magento/  (not started)
internal/api/        HTTP handlers and response views
internal/agent/      agent loop (Anthropic Go SDK, claude-opus-5-5) and 5 tools;
                     openai.go adapts the loop to OpenAI-compatible APIs
data/products.json   150 fictional products
```

## Rules that must hold

- **Session from context only.** `commerce.WithSession` / `SessionFrom`. HTTP
  middleware injects it. Tool arguments from the model never include customer
  ID, cart ID or city overrides.
- **Prices computed in the backend.** `Product.EffectivePrice(now)` is the only
  price rule. Carts store SKU + qty and re-price on every read. Never accept a
  price from a caller or the model. Re-price at checkout.
- **Money is integer halalas** (`commerce.Money`, 1 SAR = 100). Convert floats
  only at boundaries (`commerce.SAR(x)`, `.Riyals()`).
- **Prescription guardrail in the adapter**, not just the prompt:
  `AddToCart` returns `ErrPrescriptionRequired` for Rx products.
- **Tools return allowed fields only.** Never marshal `commerce.Product` to the
  model. Build a view struct per tool. Exact stock counts stay internal.
- Disabled products behave as `ErrNotFound`.
- Adapters return the sentinel errors in `adapter.go` (wrapping is fine).
  `internal/api` maps them to HTTP status codes.

## Domain notes

- Product mirrors Magento: sku, name, price, special_price (+ from/to dates,
  to-date inclusive), status (1 enabled, 2 disabled), custom attributes
  flattened (requires_prescription, active_ingredient, age_min, category).
- Arabic fields: name_ar, description_ar. `?lang=ar` on the API.
- Stock per city: riyadh, jeddah, dammam (maps to Magento MSI sources later).
- Search: AND across terms, prefix matching, Arabic normalisation (hamza
  forms, taa marbuta, alef maqsura, diacritics, Arabic-Indic digits).
  Weights: name 4, sku/brand/ingredient 3, category 2, description 1.
- Fixed data edge cases used by tests: PAIN-001 active sale, VITA-001 expired
  sale, SKIN-003 future sale; COLD-005, VITA-013, SKIN-010 disabled; RX-* are
  prescription-only.

## MVP scope

In: ~150-product catalogue, search, product details, availability, cart, an
agent with 4–5 tools, simple chat UI, prescription guardrail.

Out (mention as next steps in the demo): payments, accounts, personalised
pricing, ACP/UCP agentic commerce protocols.

## Status

- [x] `types.go`, `adapter.go`
- [x] `data/products.json` via `cmd/gendata`
- [x] mock adapter (catalogue + cart) with tests
- [x] `GET /products/search`, `GET /products/{sku}`, `GET /products/{sku}/availability`
- [x] session middleware + cart endpoints (`GET/PATCH /session`, `/cart`, `/cart/items[/{sku}]`)
- [x] agent loop + tools (search, product details, availability, add to cart, view cart), `POST /chat`
- [x] `-provider` flag: OpenAI-compatible backend (gemini, groq, openrouter, ollama)
- [ ] live test of the agent with a real API key (free provider first, Anthropic later)
- [ ] chat UI
- [ ] magento adapter

## Commands

```bash
go test ./...
go run ./cmd/server            # http://localhost:8080 (-addr :8090, -model ...)
go run ./cmd/gendata           # rewrite data/products.json
curl 'localhost:8080/products/search?q=paracetamol&city=riyadh'
curl 'localhost:8080/products/search?q=فيتامين&lang=ar'
```

Search params: `q, category, brand, city, in_stock, otc_only, max_price (SAR),
page, page_size (max 50), lang`.

Cart (needs a cookie jar; the server issues an HttpOnly `shopper_session`
cookie on first use and keeps cart ID and city server-side):

```bash
curl -b jar -c jar localhost:8080/cart
curl -b jar -c jar -X POST localhost:8080/cart/items -d '{"sku":"PAIN-001","qty":2}'
curl -b jar -c jar -X PATCH localhost:8080/cart/items/PAIN-001 -d '{"qty":0}'   # 0 removes
curl -b jar -c jar -X DELETE localhost:8080/cart/items/PAIN-001
curl -b jar -c jar -X PATCH localhost:8080/session -d '{"city":"jeddah"}'
```

Bodies are strict JSON: unknown fields (`price`, `cart_id`) are a 400.
Sessions and carts are in memory and reset on restart. Known gap: changing
city does not re-check stock for lines already in the cart (do it at checkout).

Chat (needs `ANTHROPIC_API_KEY`; without it `/chat` returns 502 and the log
says which credential is missing):

```bash
curl -b jar -c jar -X POST localhost:8080/chat -d '{"message":"do you have paracetamol?"}'
curl -b jar -c jar -X DELETE localhost:8080/chat   # new conversation, cart kept
```

API keys go in `.env` (git-ignored; copy `.env.example`). The server loads
it on start (`-env path` to change); shell variables win. Never commit keys.
`LLM_PROVIDER`, `LLM_MODEL`, `LLM_BASE_URL` in `.env` pick the backend so a
plain `go run ./cmd/server` uses it. A `-provider` naming a different backend
ignores `LLM_MODEL`/`LLM_BASE_URL`; `-model/-base-url` override individually.

Free/other model backends (Anthropic is the default; switch back by dropping
`-provider`). `-model` is required for these; `-base-url` overrides the preset:

```bash
GEMINI_API_KEY=... go run ./cmd/server -provider gemini -model gemini-2.5-flash
go run ./cmd/server -provider ollama -model qwen3:8b      # local, no key
# also: groq (GROQ_API_KEY), openrouter (OPENROUTER_API_KEY),
#       openai + -base-url (OPENAI_API_KEY)
```

The adapter drops Anthropic-only features (prompt caching, thinking, effort).
It also coerces argument type slips from small models using the tool schema
(`"2"` → 2, `"false"` → false, `"null"` optional fields dropped); unknown
fields still reach the strict decoder and are rejected. llama3.2:3b (tested
2026-10-19) is too weak: invents fields like `delivery_city`, ignores tool
errors and makes up products. Gemini 3's thought signatures
(`extra_content` on tool calls) are kept in memory by call ID and echoed back.
Smaller models are weaker at tool use and Arabic; the guardrails still hold
because they live in the adapter and tool decoding, not the model.

Agent notes: conversations are in memory, keyed by the session's cart ID.
History is append-only (the delivery city and language ride along on each
customer message, not in the system prompt), which keeps the prompt cache and
Opus 5.5's thinking blocks valid. Max 8 model calls per message. Tool inputs
are decoded strictly, so `city`, `cart_id` or `price` from the model is an
error the model sees. Tests use a fake model (`internal/agent/agent_test.go`).
