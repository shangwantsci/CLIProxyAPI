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
User-Agent: claude-cli/2.1.152 (external, sdk-cli)
X-Stainless-Package-Version: 0.94.0
X-Stainless-Runtime-Version: v24.3.0
X-Stainless-Os: Windows
X-Stainless-Arch: x64
```

验证来源：

- `2.1.152` 来自 2026-05-27 本机 `claude --version`。
- `sdk-cli`、`0.94.0`、`v24.3.0`、`Windows/x64` 来自 2026-05-27 本机 Claude Code `2.1.152` 对本地 `ANTHROPIC_BASE_URL` 捕获的真实请求头。

这些是当前仓库默认基线，不等于“永远正确的最新版真实 Claude Code 指纹”。完整 HTTPS 抓包仍然是最强校准方式；如后续 Claude Code 更新导致 package/runtime、beta 或 system prompt 有差异，应按抓包结果更新。

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

当前默认 beta tokens：

```text
claude-code-20250219
interleaved-thinking-2025-05-14
effort-2025-11-24
```

当前允许但不默认注入的 beta tokens：

```text
oauth-2025-04-20
prompt-caching-scope-2026-01-05
context-management-2025-06-27
extended-cache-ttl-2025-04-11
fine-grained-tool-streaming-2025-05-14
structured-outputs-2025-12-15
fast-mode-2026-02-01
redact-thinking-2026-02-12
thinking-token-count-2026-05-13
task-budgets-2026-03-13
cache-diagnosis-2026-04-07
mid-conversation-system-2026-04-07
```

当前会丢弃：

```text
context-1m-2025-08-07
```

维护原则：

- 只允许在白名单中的 beta。
- 对客户端传入 beta 做过滤和去重。
- 强制补齐 Claude Code 默认 beta。
- 客户端/请求体显式传入的已知 beta 可以保留；不再把历史观察到的所有 beta 都默认注入，避免请求形态过宽。
- 每次更新 beta 列表必须说明来源，最好来自真实 Claude Code 抓包。

### 5. Claude Code system prompt 静态块

代码入口：

- `internal/runtime/executor/helps/claude_system_prompt.go`
- `internal/runtime/executor/claude_executor.go`

当前 system prompt 静态块按 2026-05-27 本机 Claude Code `v2.1.152` 捕获结果校准：

- 身份块：`You are a Claude agent, built on Anthropic's Claude Agent SDK.`
- 静态提示块：以 `You are an interactive agent that helps users according to your "Output Style" below...` 开头，并包含 `# Harness`。
- 身份块和静态提示块都会带 `cache_control: {"type":"ephemeral"}`。
- OAuth/订阅桥接路径仍在最前方保留 `x-anthropic-billing-header`，并对最终 body 做 CCH signing。

相关策略：

- 对需要 Claude Code system blocks 的模型注入静态块。
- 非 strict cloak 模式下，会把用户 system prompt 保留并前置/转移到请求中。
- strict cloak 模式下，会更强地清理用户 system prompt，只保留 Claude Code 风格 prompt。
- 当前 Agent SDK 身份块和 Harness 块已自带 `cache_control`；通用 cache 注入逻辑不会再重复给 system 额外补点。

### 5.1 `cc_version` build 指纹

代码入口：`internal/runtime/executor/claude_executor.go`

行为：

- `cc_version=<version>.<build>` 的三位 build 指纹使用固定 salt `59cf53e54c78`。
- 指纹输入取第一条 user 消息里的第一个 text 内容，而不是 system prompt。
- 这是 2026-05-27 从本机 Claude Code `v2.1.152` 二进制和请求形态中观察到的行为；测试 `TestCheckSystemInstructionsWithMode_BillingFingerprintUsesFirstUserText` 覆盖该规则。

### 5.2 thinking/signature 与 token usage

代码入口：

- `internal/runtime/executor/helps/billable_usage.go`
- `internal/runtime/executor/helps/usage_helpers.go`
- `internal/translator/claude/openai/*`

当前策略：

- Claude 原生流式响应中的 `thinking_delta`、`signature_delta` 不做结构改写；usage 重写只处理 `usage` 或 `message.usage` 节点。
- token 统计读取 `message_start.message.usage` 和后续 `message_delta.usage`，再合并 input/output/cache 字段。
- fallback total 会按 `input + output + reasoning + cache_creation + cache_read` 归一化。
- `claude-billable-usage` 默认开启时，返回给下游用户和 usage queue 的 usage 使用 customer-facing billable projection：
  - `input_tokens` / OpenAI `prompt_tokens` / Responses `input_tokens` 改为按原始下游请求估算的输入 token。
  - `cache_creation_input_tokens`、`cache_read_input_tokens`、`cached_tokens` 以及 OpenAI 兼容层的 `cached_tokens`/`cached_creation_tokens` 不再暴露为用户计费字段，避免把本项目注入的 Claude Code system prompt、billing header、ephemeral cache 成本算给用户。
  - usage queue 发布前同样应用该 projection；原始上游 usage 仍保留在 API response chunk / 请求日志链路中，供排障和账号健康分析使用。
- 2026-05-27 已对齐 sub2api 最新更新中的 `message_start` usage 读取思路；这里的“对齐 sub2api”只指 token usage/计费语义，不指 Claude Code 伪装策略。sub2api 的长上下文美元价格倍率修复不直接适用于本项目，因为本项目当前记录 token 明细，不在该路径内做本地美元计价。

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
