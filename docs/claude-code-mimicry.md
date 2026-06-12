# Claude Code 兼容与伪装策略文档

本文档用于追踪 Claude Code 兼容策略、当前已实现的伪装能力、验证方式和后续改进方向。

## 术语

- Claude Code：Anthropic 官方命令行编码工具。真实 Claude Code 请求会带一组稳定的客户端特征。
- 伪装：把非 Claude Code 客户端的请求改写成更接近真实 Claude Code 的请求形态。
- 指纹：服务端可观察到的一组特征，包括 `User-Agent`、`X-Stainless-*` 请求头、`anthropic-beta`、system prompt、body 字段结构等。
- Stainless：Anthropic SDK 使用的请求头命名族，例如 `X-Stainless-Lang`、`X-Stainless-Runtime`、`X-Stainless-Package-Version`。
- cloak：本项目中的请求伪装模式，用于给非 Claude Code 客户端补齐 Claude Code 风格的 system prompt、metadata 和敏感词处理。
- CCH signing：Claude Code Header/body 相关签名能力。OAuth token 默认启用最终 body 签名；API key 仍需显式配置 `experimental-cch-signing`。

## 当前默认指纹基线

当前代码默认值位于 `internal/runtime/executor/helps/claude_device_profile.go`：

```text
User-Agent: claude-cli/2.1.170 (external, sdk-cli)
X-Stainless-Package-Version: 0.94.0
X-Stainless-Runtime-Version: v24.3.0
X-Stainless-Os: Windows
X-Stainless-Arch: x64
```

验证来源：

- `2.1.170` 来自 2026-06-10 本机 `claude --version` 与 Claude Code `claude-fable-5` 本地假上游抓包校准。抓包使用空 `CLAUDE_CONFIG_DIR` 和测试 API key，只观测请求形态，不使用真实 OAuth token。
- `0.94.0`、`v24.3.0` 经历史一手二进制比对确认：2.1.154 与 2.1.161 二进制内嵌的 `X-Stainless-Package-Version` 常量**均为 `0.94.0`**，runtime 均为 `v24.3.0`；2026-06-10 本机 2.1.170 二进制字符串与 Fable 抓包仍确认 `X-Stainless-Package-Version: 0.94.0`、`X-Stainless-Runtime-Version: v24.3.0`。
- 2026-06-10 本机 Mac 抓包中的 `X-Stainless-Os` 为 `MacOS`，但当前仓库默认仍保留既有生产 baseline `Windows/x64`，避免无生产抓包证据时改变账号 device profile 的平台指纹。
- Fable 抓包确认主请求形态：`/v1/messages?beta=true`、`model: claude-fable-5`、`max_tokens: 64000`、`stream: true`、`thinking.type: adaptive`。默认无 `--effort` 时 `output_config.effort: high`；显式 `--effort xhigh/max` 会分别发送 `xhigh/max`。
- ⚠️ **关键陷阱**：npm 上独立包 `@anthropic-ai/sdk` 已升到 `0.100.1`，但官方 Claude Code 把 SDK **vendored(内联)进二进制**，真实 CLI 上报的 `X-Stainless-Package-Version` 仍是 `0.94.0`。**不要**把它追到 npm 的最新版——那会制造一个真实 CLI 永不发出的假指纹。runtime `v24.3.0` 同理(Bun 编译内嵌 node-compat 版本),保持不变。
- `2.1.154`(历史基线)来自 2026-05-29 本机 `claude --version` 和真实 Opus 4.8 OAuth 请求抓包；2026-06-03 升级到 2.1.161；2026-06-10 跟随 Claude Code 2.1.170/Fable 5 升级 UA。

这些是当前仓库默认基线，不等于“永远正确的最新版真实 Claude Code 指纹”。完整 HTTPS 抓包仍然是最强校准方式；如后续 Claude Code 更新导致 package/runtime、beta 或 system prompt 有差异，应按抓包结果更新。**升级 UA 版本号时务必只改版本号，不要连带改 package/runtime version,除非有新的一手抓包证据。**

## 已实现策略

### 1. Device profile 稳定化

代码入口：

- `internal/runtime/executor/helps/claude_device_profile.go`
- `internal/runtime/executor/claude_executor.go`
- `sdk/cliproxy/auth/persist_policy.go`

行为：

- 如果客户端请求头中出现真实 `claude-cli/x.y.z` UA，会提取一组 device profile。
- device profile 包含 UA、Stainless package version、runtime version、OS、arch。
- 缓存 TTL 为 7 天。
- 可以持久化到账号 metadata：
  - `claude_device_profile`
  - `claude_device_profile_updated_at`
- 解析到的客户端版本必须不低于当前 baseline，低版本或缺字段会回退到 baseline。
- device profile 学习是“可信自适应”，不是只按 UA 版本号无条件升级：
  - UA 中包含 `undefined`、`local` 或明显异常主版本号的候选会被拒绝。
  - 候选 CLI 版本高于 baseline，但 `X-Stainless-Package-Version` 低于 baseline 时会被拒绝，避免 `claude-cli/2.2.126` 搭配旧 package 的不自洽组合污染账号 metadata。
  - 已写入账号 metadata 的历史污染 profile 也会在读取时重新校验，不可信时回退到当前 baseline。
- OS/arch 会被 pin 到当前 baseline，避免跨平台组合漂移。

### 2. 上游请求头重写

代码入口：

- `internal/runtime/executor/claude_executor.go`
- `internal/runtime/executor/helps/claude_device_profile.go`

当前会先删除这些敏感/客户端来源头，再由服务端统一设置：

```text
User-Agent
X-Stainless-Package-Version
X-Stainless-Runtime-Version
X-Stainless-Os
X-Stainless-Arch
X-Stainless-Retry-Count
X-Stainless-Runtime
X-Stainless-Lang
X-Stainless-Timeout
X-App
Anthropic-Version
Anthropic-Beta
Anthropic-Dangerous-Direct-Browser-Access
X-Claude-Code-Session-Id
Authorization
x-api-key
```

当前固定设置：

```text
X-App: cli
X-Stainless-Retry-Count: 0
X-Stainless-Runtime: node
X-Stainless-Lang: js
X-Stainless-Timeout: 600
X-Claude-Code-Session-Id: 按 token 缓存的 session id
Anthropic-Dangerous-Direct-Browser-Access: true
Accept: application/json
Accept-Encoding: gzip, deflate, br, zstd
```

OAuth token 使用 `Authorization: Bearer ...`。Anthropic base URL + API key 模式使用 `x-api-key`。`Anthropic-Dangerous-Direct-Browser-Access: true` 对 Claude Code 伪装请求统一设置；`x-client-request-id` 不再主动注入，因为本机 `2.1.152` 默认请求没有该头。

### 3. 第三方代理特征头清理

代码入口：`internal/runtime/executor/claude_executor.go`

当前会清理常见中转/网关前缀：

```text
x-openclaw-
x-hermes-
acp-
x-claude-relay-
x-litellm-
helicone-
x-portkey-
cf-aig-
x-kong-
x-bt-
x-cpa-
x-cliproxy-
x-sub2api-
x-newapi-
x-oneapi-
x-openrouter-
x-lobe-
x-cherry-
x-fastapi-
x-chatnio-
x-aigateway-
x-llm-
```

新增代理生态时，应优先补这里，并补测试确认不会把 CPA/NewAPI/OpenRouter 等特征透传给 Anthropic。

### 4. `anthropic-beta` 过滤与补齐

代码入口：`internal/runtime/executor/claude_executor.go`

普通 Claude Code 默认 beta tokens：

```text
claude-code-20250219
interleaved-thinking-2025-05-14
effort-2025-11-24
```

`claude-fable-5` / `claude-mythos-5` 这类 always-adaptive 模型使用模型特定默认 beta 顺序：

```text
claude-code-20250219
interleaved-thinking-2025-05-14
thinking-token-count-2026-05-13
context-management-2025-06-27
prompt-caching-scope-2026-01-05
mid-conversation-system-2026-04-07
advisor-tool-2026-03-01
effort-2025-11-24
fallback-credit-2026-06-01
```

`claude-opus-4-8` 会使用模型特定 beta 顺序：

```text
claude-code-20250219
interleaved-thinking-2025-05-14
mid-conversation-system-2026-04-07
effort-2025-11-24
```

当前允许但不默认注入的 beta tokens：

```text
oauth-2025-04-20
prompt-caching-scope-2026-01-05
context-management-2025-06-27
context-1m-2025-08-07
extended-cache-ttl-2025-04-11
fine-grained-tool-streaming-2025-05-14
structured-outputs-2025-12-15
fast-mode-2026-02-01
redact-thinking-2026-02-12
thinking-token-count-2026-05-13
task-budgets-2026-03-13
cache-diagnosis-2026-04-07
server-side-fallback-2026-06-01
fallback-credit-2026-06-01
mid-conversation-system-2026-04-07
advisor-tool-2026-03-01
```

当前会丢弃：

```text
（无）
```

> `context-1m-2025-08-07`（1M 上下文）历史上曾被强制丢弃以收窄请求形态、降低订阅号被识别为非官方客户端的风险。2026-06-08（commit `fa83c466`）按业务需求改为放开：从丢弃表移除并加入上述允许白名单，使客户端显式请求 1M 时能透传上游、且不被 mimicry guard 判为 unexpected beta 拦截。它**不在默认注入列表**。2026-06-11 生产反馈显示 OAuth/Claude Code 认证方式会被上游拒绝并返回 `This authentication style is incompatible with the long context beta header.`，因此当前实现对 OAuth token 自动过滤该 beta；仅 API key 等兼容认证方式可透传客户端显式请求的 1M beta。

维护原则：

- 只允许在白名单中的 beta。
- 对客户端传入 beta 做过滤和去重。
- 强制补齐 Claude Code 默认 beta。
- 客户端/请求体显式传入的已知 beta 可以保留；不再把历史观察到的所有 beta 都默认注入，避免请求形态过宽。
- 请求体包含 `context_management` 时会自动补 `context-management-2025-06-27`，避免客户端忘记带 beta 时被上游直接拒绝。
- `server-side-fallback-2026-06-01` 只允许客户端显式请求时透传，不默认注入；2026-06-10 Claude Code 2.1.170 Fable 抓包即使设置 `--fallback-model claude-opus-4-8`，主请求 body 仍为 `fallbacks: null`，默认 beta 也只包含 `fallback-credit-2026-06-01`。
- 每次更新 beta 列表必须说明来源，最好来自真实 Claude Code 抓包。

### 5. Claude Code system prompt 静态块

代码入口：

- `internal/runtime/executor/helps/claude_system_prompt.go`
- `internal/runtime/executor/claude_executor.go`

当前 system prompt 形态按 2026-05-29 本机 Claude Code `v2.1.154` Opus 4.8 OAuth 抓包校准：

- 身份块：`You are a Claude agent, built on Anthropic's Claude Agent SDK.`
- runtime context 块：以 `CWD:`、`Date:`、`gitStatus:` 开头，匹配官方 `2.1.154` bare/OAuth 请求形态。
- 身份块和 runtime context 块都会带 `cache_control: {"type":"ephemeral"}`。
- CPA OAuth/订阅桥接路径仍在最前方保留 `x-anthropic-billing-header`，并对最终 body 做 CCH signing。
- 真实官方 `ANTHROPIC_AUTH_TOKEN` bearer-token 抓包没有 `x-anthropic-billing-header`；mimicry audit 将其记录为可识别的官方 bearer-token 形态，但 guard 在需要签名 CCH 的代理路径仍会要求 `Signed=true`。

相关策略：

- 对需要 Claude Code system blocks 的模型注入静态块。
- 非 strict cloak 模式下，会把用户 system prompt 保留并前置/转移到请求中。
- strict cloak 模式下，会更强地清理用户 system prompt，只保留 Claude Code 风格 prompt。
- 当前 Agent SDK 身份块和 Harness 块已自带 `cache_control`；通用 cache 注入逻辑不会再重复给 system 额外补点。
- 当非 strict cloak 把原始 system text 转移到首条 user message 的 `<system-reminder>` 时，如果原始最后一个被转发的 system text block 带合法 `cache_control: {"type":"ephemeral"}`，该断点会随转移后的 reminder block 保留；OAuth cloaking 也会保留这类显式 cache-marked system 文本，避免客户长前缀被 sanitize 后无法创建 prompt cache。非法 `cache_control.type` 不会被转发，避免把客户端异常参数放大成上游错误。

### 5.1 请求形态修复优先

代码入口：`internal/runtime/executor/claude_executor.go`

当前策略：

- 对外接客户端优先修复可安全修复的请求，而不是直接拦截。
- `thinking.type` 为 `enabled`、`adaptive` 或 `auto` 时，若当前模型不声明支持 `output_config.effort=xhigh`，会把 `xhigh` 降级为 `high`。这是按 2026-05-29 生产日志中上游明确拒绝 Opus 旧路径 `xhigh`，且真实 Opus 4.8 OAuth 抓包使用 `high` 校准。
- `claude-fable-5` 是 always-adaptive 模型：默认请求补 `thinking.type=adaptive` 与 `output_config.effort=high`，但显式 `low/high/xhigh/max` 都应保留。`(none)` 或显式 `thinking.type=disabled` 不会向 Fable 发送 `disabled`，因为该模型不支持关闭 thinking。
- `thinking.budget_tokens` 超出已知模型 `thinking.min/max` 时会 clamp 到模型范围内；如请求同时设置 `max_tokens`，会尽量保持 `budget_tokens < max_tokens`。
- `messages[*].content` 中空字符串 text block 会删除；如果该消息只剩空 text 或 `content` 本身是空字符串，会改为单空格占位，避免上游返回 `messages: text content blocks must be non-empty`；如果非空 text block 带 `cache_control`，该字段必须保留。
- `messages[*].content[*].tool_use.id` 与对应 `tool_result.tool_use_id` 会在送上游前规范化为 Claude 允许的 `^[a-zA-Z0-9_-]+$` 形态；客户端传入点号、冒号、斜杠、空格或非 ASCII 字符时，号池会用原始 ID 的稳定短哈希生成合法 ID，并保持同一请求内 tool_use/tool_result 配对，避免上游 400。
- `messages[*].role="system"` 会在送上游前提升为顶层 `system` 文本块并从 `messages` 删除；如果请求只剩 system 指令，会补一个空 user fallback，避免新模型拒绝 `system` role。
- 新式 adaptive-only / always-adaptive 模型（当前覆盖 `claude-fable-5`、`claude-mythos-5`、`claude-mythos-preview`、`claude-opus-4-8`、`claude-opus-4-7`）不透传 `temperature`，避免上游返回 ``temperature` is deprecated for this model`。旧模型保持客户端 `temperature` 原值；带 active thinking 的旧模型仍按既有规则把 `temperature` 归一到 `1`。
- 不支持 assistant message prefill 的模型（当前覆盖 `claude-fable-5`、`claude-mythos-5`、`claude-mythos-preview`、`claude-opus-4-8`、`claude-opus-4-7`、`claude-opus-4-6`、`claude-sonnet-4-6`），以及任何显式 `thinking.type=enabled/adaptive/auto` 的请求，最终都不能以 `role:"assistant"` 收尾。若最终消息是 assistant，号池会追加一个空 user turn，让最终请求以 user 收尾；不再限定 assistant content 必须是 text-only，包含 `thinking`、`redacted_thinking`、`tool_use` 或混合 content 的 assistant 收尾也会被修复。
- `cache_control` TTL 归一化（`normalizeCacheControlTTL`）：Anthropic 要求按 `tools → system → messages` 的求值顺序，1h TTL 块不能排在 5m 块之后。号池注入的 Claude Code 身份/runtime context 块带默认（5m）`cache_control` 且排在最前，会与客户端显式的 `ttl="1h"` 块冲突。**策略（2026-06-08 commit `fa83c466` 起）**：只要请求中存在任意客户端显式 1h 块（仅客户端会写 `ttl="1h"`，号池注入块不写 ttl），就把**所有** ephemeral 块统一升级为 1h，保住客户端的 1h 缓存意图，同时让顺序天然合法。请求中没有 1h 块时字节级原样返回，默认（5m）行为不变。注意：升级只改 `cache_control.ttl`，不动 system block 的 `text`，因此不影响 mimicry guard 的 system 块哈希审计（该审计只哈希 `text`）。此前的旧策略是反向的——遇到 5m 块就把后续 1h 块降级成 5m，会静默丢掉客户端请求的 1h 缓存，已废弃。
- `context_management.edits` 中的 `clear_thinking_20251015` 只有在最终请求没有 `enabled/adaptive/auto` thinking 时才会删除，避免被上游以“clear_thinking 需要 thinking”为由拒绝。
- 前置修复和最终修复分层执行：`ApplyThinking` 前只修会导致本地 thinking 校验失败的字段；`ApplyThinking` 和 payload config 完成后再根据最终 body 处理 `context_management`，避免误删模型后缀稍后启用 thinking 的合法请求。

硬性约束：

- 这里不得改 OAuth / refresh_token 路径。
- 这里不得改响应 usage 重写逻辑；任何涉及 `cache_control` 的请求修复都必须保留上游 cache breakdown 的透传测试。

### 5.2 `cc_version` build 指纹

代码入口：`internal/runtime/executor/claude_executor.go`

行为：

- `cc_version=<version>.<build>` 的三位 build 指纹使用固定 salt `59cf53e54c78`。
- 指纹输入取第一条 user 消息里的第一个 text 内容，而不是 system prompt。
- 这是 2026-05-27 从本机 Claude Code `v2.1.152` 二进制和请求形态中观察到的行为；测试 `TestCheckSystemInstructionsWithMode_BillingFingerprintUsesFirstUserText` 覆盖该规则。

### 5.3 thinking/signature 与 token usage

代码入口：

- `internal/runtime/executor/helps/billable_usage.go`
- `internal/runtime/executor/helps/usage_helpers.go`
- `internal/translator/claude/openai/*`

当前策略：

- Claude 原生流式响应中的 `thinking_delta`、`signature_delta` 不做结构改写；usage 重写只处理 `usage` 或 `message.usage` 节点。
- token 统计读取 `message_start.message.usage` 和后续 `message_delta.usage`，再合并 input/output/cache 字段。
- fallback total 会按 `input + output + reasoning + cache_creation + cache_read` 归一化。
- `claude-billable-usage` 默认开启，但它不能破坏 Anthropic cache 语义：
  - 如果上游 usage 中存在 `cache_creation_input_tokens`、`cache_read_input_tokens`、`cached_tokens` 或 `cache_creation.ephemeral_*`，说明 Anthropic 已经返回 cache breakdown。此时必须保留上游的 uncached `input_tokens` 和 cache create/read 字段，不能为了“扣除代理注入 token”把它们清零或改成完整上下文输入。
  - Claude 原生响应、OpenAI Chat Completions 兼容响应、OpenAI Responses 兼容响应、usage queue 都遵守同一规则。
  - OpenAI 兼容层在保留 Claude cache breakdown 时使用 Anthropic 语义：`prompt_tokens` / Responses `input_tokens` 代表上游 uncached input；`cached_tokens`、`cached_creation_tokens`、`claude_cache_creation_5_m_tokens`、`claude_cache_creation_1_h_tokens` 单独暴露；并写入 `usage_semantic: "anthropic"`、`usage_source: "claude"`。
  - 只有在上游没有任何 cache breakdown 时，才允许把 `input_tokens` / `prompt_tokens` / Responses `input_tokens` 改为按原始下游请求估算的输入 token，用于避免把 Claude Code wrapper/system/billing blocks 算给用户。
- OpenAI Chat Completions / Responses 请求侧如果带 `cache_control: {"type":"ephemeral"}`，转换成 Claude Messages 时必须保留该字段。Responses 单个 `input_text` 如果带 `cache_control`，不能折叠成普通字符串 `content`，必须保持 content part 数组；否则 Anthropic 上游收不到缓存断点，后续 usage 只会显示普通 input tokens，`cache_creation` / `cache_read` 都会是 0。
- 2026-05-27 已对齐 sub2api 最新更新中的 `message_start` usage 读取思路；这里的“对齐 sub2api”只指 token usage/计费语义，不指 Claude Code 伪装策略。sub2api 的长上下文美元价格倍率修复不直接适用于本项目，因为本项目当前记录 token 明细，不在该路径内做本地美元计价。

事故复盘与硬性约束：

- 2026-05-27 的 `1bdb365c` 曾错误地把 cache breakdown 清零，导致 cctest 显示 `缓存创建=0`、`缓存读取=0`、命中率 `0%`、实际消耗倍率异常升高。这是错误实现，后续不得重复。
- 2026-05-28 发现流式非 usage chunk 被反复触发原始请求 token 估算，导致生产 CPU 异常升高。后续任何流式 usage 改写必须先判断当前 chunk 是否存在 `usage` 或 `message.usage`，普通 `content_block_delta` / `signature_delta` 不得进入 token 估算热路径。
- 2026-05-28 发现 OpenAI 兼容请求路径会丢失用户传入的 text content `cache_control`；Anthropic 原生 `/v1/messages` 缓存正常，但 `/v1/chat/completions` 和 `/v1/responses` 转 Claude 后没有缓存断点，导致客户重复请求看不到 cache create/read。后续排查缓存问题时必须同时验证请求侧 `cache_control` 是否穿过翻译层，而不能只看响应 usage 重写。
- 修改 token usage、Claude Code system prompt、cache_control、CCH signing 或 OpenAI/Responses 翻译层时，必须跑以下回归测试，确认 cache breakdown 没有被抹掉：
  - `go test ./internal/runtime/executor/helps -run "TestRewriteClaudeUsageForBillablePreservesClaudeCacheBreakdown|TestRewriteClaudeStreamUsageForBillablePreservesClaudeCacheBreakdown|TestRewriteClaudeStreamUsageForBillablePreservesCacheWithoutInventingInput|TestRewriteClaudeStreamUsageForBillableMessageStartUsage|TestClaudeBillableUsageDetailPreservesClaudeCacheBreakdown|TestRewriteClaudeStreamUsageForBillableSkipsTokenEstimateForNonUsageChunks"`
  - `go test ./internal/translator/claude/openai/chat-completions -run "TestConvertOpenAIRequestToClaude_PreservesTextCacheControl"`
  - `go test ./internal/translator/claude/openai/chat-completions -run "TestConvertClaudeResponseToOpenAINonStream_PreservesClaudeCacheBreakdownWhenBillableInputEnabled"`
  - `go test ./internal/translator/claude/openai/responses -run "TestConvertOpenAIResponsesRequestToClaude_PreservesInputTextCacheControl"`
  - `go test ./internal/translator/claude/openai/responses -run "TestConvertClaudeResponseToOpenAIResponsesNonStream_PreservesClaudeCacheBreakdownWhenBillableInputEnabled|TestConvertClaudeResponseToOpenAIResponsesStream_PreservesClaudeCacheBreakdownWhenBillableInputEnabled"`

### 6. cloak 模式

代码入口：

- `internal/runtime/executor/helps/cloak_utils.go`
- `internal/runtime/executor/claude_executor.go`
- `internal/config/config.go`
- `internal/api/handlers/management/auth_files.go`

配置位置：

- 账号 metadata/attributes：
  - `cloak_mode`
  - `cloak_strict_mode`
  - `cloak_sensitive_words`
  - `cloak_cache_user_id`
- 配置文件 Claude key：
  - `claude[].cloak.mode`
  - `claude[].cloak.strict-mode`
  - `claude[].cloak.sensitive-words`
  - `claude[].cloak.cache-user-id`

模式：

- `always`：总是伪装。
- `never`：不伪装。
- `auto`：只有当客户端同时具备 Claude Code UA 和合法 `metadata.user_id` 时才不伪装。

`metadata.user_id` 支持旧格式 `user_[64hex]_account_[id]_session_[uuid]`，也支持包含 `device_id` 和 `session_id` 的 JSON。

### 7. mimicry audit 与 guard

代码入口：

- `internal/runtime/executor/claude_mimicry_audit.go`
- `internal/runtime/executor/claude_executor.go`

能力：

- 审计 system blocks、CCH、beta、thinking、tools、headers。
- 输出状态：
  - `aligned`
  - `warning`
  - `failed`
  - `waiting`
- guard 动作：
  - `allow`
  - `degrade`
  - `block`
- 会记录最近事件，用于排查哪类客户端触发了伪装风险。
- 上游 400 中可归因于客户端请求形态的问题不会污染账号健康统计，例如：
  - `level ... not supported`
  - `budget ... out of range`
  - `thinking not supported`
  - `unknown level`
- 账号健康语义仍保持：认证失败、组织禁用、quota、上游不可用等账号或上游状态类错误才进入账号健康/冷却路径。

## 维护流程

修改 Claude Code 伪装时，不要只改一处 header。必须按下面顺序检查：

1. 确认真实来源：
   - 最好来自真实 Claude Code HTTPS 抓包。
   - 如果来自 npm 版本或旧审查报告，必须标注“不完全可靠”。
2. 更新基线或策略：
   - device profile：`claude_device_profile.go`
   - header 注入：`claude_executor.go`
   - system prompt：`claude_system_prompt.go`
   - cloak 判断：`cloak_utils.go`
   - audit/guard：`claude_mimicry_audit.go`
3. 补测试：
   - `internal/runtime/executor/claude_mimicry_audit_test.go`
   - `internal/runtime/executor/claude_executor_test.go`
   - `internal/runtime/executor/helps/*_test.go`
4. 更新本文档：
   - 当前基线。
   - 已实现策略。
   - 待验证/待改进项。
   - 验证来源和日期。

## 推荐验证命令

后端相关测试：

```powershell
Set-Location F:\claude反代\CLIProxyAPI
F:\GO语言\bin\go.exe test -count=1 ./internal/runtime/executor ./internal/runtime/executor/helps
```

如果 `./internal/runtime/executor` 整包存在其他 provider 的既有失败，可先跑 Claude 定向回归：

```powershell
F:\GO语言\bin\go.exe test -count=1 ./internal/runtime/executor -run "TestRepairClaudeRequestShape|TestInferClaudeBetasFromBody|TestApplyClaudeHeaders|TestAuditClaudeMimicry|TestClaudeExecutor_Execute_Opus48MatchesClaudeCode214RequestShape"
```

如果改动同时影响账号状态或管理面板字段：

```powershell
F:\GO语言\bin\go.exe test -count=1 ./internal/api/handlers/management ./sdk/cliproxy/auth
```

编译检查：

```powershell
F:\GO语言\bin\go.exe build -o .codex_tmp\cli-proxy-api-test.exe ./cmd/server
```

## 待改进清单

1. 下次 Claude Code 升级后重新抓包校准默认 baseline。
   - 本轮已用 `2.1.152` 本机请求确认 UA、Stainless package version、runtime version、OS、arch 的组合。
2. 定期同步 Claude Code system prompt。
   - 当前静态块来自本机 `2.1.152` 抓包；后续 Claude Code 更新后仍应重新抓包确认。
3. 建立 beta token 更新记录。
   - 每次新增、删除 beta 都记录来源。
4. 明确 CCH signing 的启用条件和失败降级策略。
   - OAuth token 当前默认启用最终 body 签名。
   - API key 仍由 `experimental-cch-signing` 显式控制。
5. 对更多第三方客户端做伪装审计样本。
   - Python Anthropic SDK。
   - JS Anthropic SDK。
   - OpenAI 兼容客户端。
   - Claude Code 原生客户端。

## 资料来源

- 当前仓库代码。
- 根目录历史审查材料：
  - `F:\claude反代\CLIProxyAPI-ClaudeCode伪装泄露检查-2026-05-21.md`
  - `F:\claude反代\CLIProxyAPI审查报告-2026-05-21.md`

历史审查材料只能作为线索，不能直接当作当前实现状态。当前事实必须以代码和测试为准。
