# AGENTS.md

Go 1.26+ proxy server providing OpenAI/Gemini/Claude/Codex compatible APIs with OAuth and round-robin load balancing.

## Repository
- GitHub: https://github.com/router-for-me/CLIProxyAPI

## Project Memory Docs

This fork has project-operation docs that must be treated as working memory for future Codex sessions. These fork-specific operation docs are intentionally written in Chinese for the project owner; keep their language unless the owner asks otherwise. Read the relevant document before changing code, deploying, or answering project-structure questions:

- `docs/codex-handoff.md` — first-stop handoff document for a new Codex window. It records the fork goal, local repositories, current production state, recent important commits, known pitfalls, and takeover checklist.
- `docs/project-file-map.md` — file map for the backend/frontend split. Read this before locating files or deciding whether a task belongs in backend or frontend. This is the guardrail against editing the wrong repository or the wrong frontend page.
- `docs/production-deployment-23.153.36.12.md` — standard production deployment and rollback flow for `23.153.36.12:8318`. Read this before any server update. Never write SSH passwords, management passwords, API keys, Claude tokens, or proxy passwords into docs or commits.
- `docs/claude-code-mimicry.md` — current Claude Code compatibility and fingerprint strategy. Read this before touching Claude headers, device profile, system prompt, cloak behavior, beta tokens, CCH signing, or mimicry audit/guard logic.
- `F:\claude反代\Cli-Proxy-API-Management-Center\docs\claude-account-pool-maintenance.md` — frontend account-pool maintenance map. Read this before changing the production management UI, especially account pool, probe jobs, quota display, detail drawers, settings modals, or bulk actions.

Keep these docs current when behavior changes. If deployed commits, server paths, frontend routes, Claude Code fingerprint defaults, or account-health semantics change, update the relevant doc in the same work. If a doc conflicts with live code or server state, verify the source of truth, fix the doc, and call out the correction.

## Token Usage Guardrail

Claude token accounting is a production-sensitive area. Before changing Claude Code mimicry, system prompt injection, `cache_control`, CCH signing, usage parsing, billable usage projection, or OpenAI/Responses Claude translators, read `docs/claude-code-mimicry.md` section `5.2 thinking/signature 与 token usage`.

Never zero, remove, or hide Anthropic cache breakdown fields in user-visible usage or the usage queue when upstream returned them. These fields include `cache_creation_input_tokens`, `cache_read_input_tokens`, `cached_tokens`, `cache_creation.ephemeral_5m_input_tokens`, `cache_creation.ephemeral_1h_input_tokens`, OpenAI-compatible `prompt_tokens_details.cached_tokens`, and `cached_creation_tokens`. Billable projection may rewrite input tokens only when upstream did not return any cache breakdown.

OpenAI-compatible request translators must preserve user-provided Anthropic `cache_control` objects on text content parts when converting to Claude. Do not collapse a Responses single text part into a plain string if it carries `cache_control`; doing so prevents Anthropic prompt cache creation/read usage from appearing.

Required regression tests for Claude token usage/cache changes:

```bash
go test ./internal/runtime/executor/helps -run "TestRewriteClaudeUsageForBillablePreservesClaudeCacheBreakdown|TestRewriteClaudeStreamUsageForBillablePreservesClaudeCacheBreakdown|TestRewriteClaudeStreamUsageForBillablePreservesCacheWithoutInventingInput|TestRewriteClaudeStreamUsageForBillableMessageStartUsage|TestClaudeBillableUsageDetailPreservesClaudeCacheBreakdown"
go test ./internal/translator/claude/openai/chat-completions -run "TestConvertOpenAIRequestToClaude_PreservesTextCacheControl"
go test ./internal/translator/claude/openai/chat-completions -run "TestConvertClaudeResponseToOpenAINonStream_PreservesClaudeCacheBreakdownWhenBillableInputEnabled"
go test ./internal/translator/claude/openai/responses -run "TestConvertOpenAIResponsesRequestToClaude_PreservesInputTextCacheControl"
go test ./internal/translator/claude/openai/responses -run "TestConvertClaudeResponseToOpenAIResponsesNonStream_PreservesClaudeCacheBreakdownWhenBillableInputEnabled|TestConvertClaudeResponseToOpenAIResponsesStream_PreservesClaudeCacheBreakdownWhenBillableInputEnabled"
```

## Commands
```bash
gofmt -w . # Format (required after Go changes)
go build -o cli-proxy-api ./cmd/server # Build
go run ./cmd/server # Run dev server
go test ./... # Run all tests
go test -v -run TestName ./path/to/pkg # Run single test
go build -o test-output ./cmd/server && rm test-output # Verify compile (REQUIRED after changes)
```
- Common flags: `--config <path>`, `--tui`, `--standalone`, `--local-model`, `--no-browser`, `--oauth-callback-port <port>`

## Config
- Default config: `config.yaml` (template: `config.example.yaml`)
- `.env` is auto-loaded from the working directory
- Auth material defaults under `auths/`
- Storage backends: file-based default; optional Postgres/git/object store (`PGSTORE_*`, `GITSTORE_*`, `OBJECTSTORE_*`)

## Architecture
- `cmd/server/` — Server entrypoint
- `internal/api/` — Gin HTTP API (routes, middleware, modules)
- `internal/api/modules/amp/` — Amp integration (Amp-style routes + reverse proxy)
- `internal/thinking/` — Main thinking/reasoning pipeline. `ApplyThinking()` (apply.go) parses suffixes (`suffix.go`, suffix overrides body), normalizes config to canonical `ThinkingConfig` (`types.go`), normalizes and validates centrally (`validate.go`/`convert.go`), then applies provider-specific output via `ProviderApplier`. Do not break this "canonical representation → per-provider translation" architecture.
- `internal/runtime/executor/` — Per-provider runtime executors (incl. Codex WebSocket)
- `internal/translator/` — Provider protocol translators (and shared `common`)
- `internal/registry/` — Model registry + remote updater (`StartModelsUpdater`); `--local-model` disables remote updates
- `internal/store/` — Storage implementations and secret resolution
- `internal/managementasset/` — Config snapshots and management assets
- `internal/cache/` — Request signature caching
- `internal/watcher/` — Config hot-reload and watchers
- `internal/wsrelay/` — WebSocket relay sessions
- `internal/usage/` — Usage and token accounting
- `internal/tui/` — Bubbletea terminal UI (`--tui`, `--standalone`)
- `sdk/cliproxy/` — Embeddable SDK entry (service/builder/watchers/pipeline)
- `test/` — Cross-module integration tests

## Code Conventions
- Keep changes small and simple (KISS)
- Comments in English only
- If editing code that already contains non-English comments, translate them to English (don’t add new non-English comments)
- For user-visible strings, keep the existing language used in that file/area
- New Markdown docs should be in English unless the file is explicitly language-specific (e.g. `README_CN.md`)
- As a rule, do not make standalone changes to `internal/translator/`. You may modify it only as part of broader changes elsewhere.
- If a task requires changing only `internal/translator/`, run `gh repo view --json viewerPermission -q .viewerPermission` to confirm you have `WRITE`, `MAINTAIN`, or `ADMIN`. If you do, you may proceed; otherwise, file a GitHub issue including the goal, rationale, and the intended implementation code, then stop further work.
- `internal/runtime/executor/` should contain executors and their unit tests only. Place any helper/supporting files under `internal/runtime/executor/helps/`.
- Follow `gofmt`; keep imports goimports-style; wrap errors with context where helpful
- Do not use `log.Fatal`/`log.Fatalf` (terminates the process); prefer returning errors and logging via logrus
- Shadowed variables: use method suffix (`errStart := server.Start()`)
- Wrap defer errors: `defer func() { if err := f.Close(); err != nil { log.Errorf(...) } }()`
- Use logrus structured logging; avoid leaking secrets/tokens in logs
- Avoid panics in HTTP handlers; prefer logged errors and meaningful HTTP status codes
- Timeouts are allowed only during credential acquisition; after an upstream connection is established, do not set timeouts for any subsequent network behavior. Intentional exceptions that must remain allowed are the Codex websocket liveness deadlines in `internal/runtime/executor/codex_websockets_executor.go`, the wsrelay session deadlines in `internal/wsrelay/session.go`, the management APICall timeout in `internal/api/handlers/management/api_tools.go`, and the `cmd/fetch_antigravity_models` utility timeouts
