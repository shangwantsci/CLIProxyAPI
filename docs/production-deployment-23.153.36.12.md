# CPA Claude Proxy 生产部署记录：154.29.158.193

本文档固定新生产服务器上的部署位置、隔离边界、更新流程和排障入口。文档名保留旧 IP 仅为兼容既有引用；当前生产主机是 `154.29.158.193`。文档不记录任何 SSH 密码、管理密码、API Key、Claude token 或代理密码。

## 服务器现状

- 主机：`154.29.158.193`
- SSH：`root@154.29.158.193:56260`
- 系统：Ubuntu，Docker 已安装
- NewAPI 对外入口：`https://api.openstaryu.com`
- CPA 管理入口：`https://admin.openstaryu.com/management.html`
- CPA 本机入口：`http://127.0.0.1:8318/management.html`
- CPA 本机健康检查：`http://127.0.0.1:8318/healthz`
- CPA 的 `8318` 端口不再公网开放；不要再把 `http://154.29.158.193:8318` 作为客户或管理入口。

部署前已确认存在的生产项目：

- `/opt/new-api-production`
- `/opt/sub2api-production`
- Coolify 相关容器
- `new-api` 监听 `127.0.0.1:13000`
- `sub2api` 监听 `127.0.0.1:18080`
- `80/443` 由 Coolify Traefik 对公网提供 HTTP/HTTPS
- `8000/8080/6001/6002` 已收口为 `127.0.0.1` 本机监听

CPA 使用独立目录、独立容器和独立运行产物；为支持域名反代，CPA 容器同时加入 `cpa-claude-net` 和 Coolify 的 `coolify` 网络。NewAPI 中 CPA 号池渠道的 `base_url` 应为 `http://cpa-claude-proxy:8318`，避免通过公网 IP 回绕。

## CPA 固定部署布局

- 项目根目录：`/opt/cpa-claude-proxy`
- 后端运行目录：`/opt/cpa-claude-proxy/runtime`
- 后端二进制：`/opt/cpa-claude-proxy/runtime/CLIProxyAPI`
- 配置文件：`/opt/cpa-claude-proxy/config.yaml`
- 账号与代理数据：`/opt/cpa-claude-proxy/auths`
- 前端静态文件：`/opt/cpa-claude-proxy/static/management.html`
- Compose 文件：`/opt/cpa-claude-proxy/docker-compose.yml`
- 部署提交记录：`/opt/cpa-claude-proxy/DEPLOYED_COMMITS`
- 账号数据备份：`/opt/cpa-claude-proxy-backups/auths-*.tgz`

Docker 固定信息：

- 容器名：`cpa-claude-proxy`
- Docker 网络：`cpa-claude-net`、`coolify`
- 镜像：`debian:12-slim`
- 端口映射：`127.0.0.1:8318->8318/tcp`
- 容器环境：
  - `MANAGEMENT_STATIC_PATH=/CLIProxyAPI/static`
  - `SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt`
- 配置挂载必须是读写：`./config.yaml:/CLIProxyAPI/config.yaml`，不能带 `:ro`，否则管理面板保存 YAML 会返回 `write_failed`。

Traefik 动态路由：

- 配置文件：`/data/coolify/proxy/dynamic/openstaryu_routes.yaml`
- `api.openstaryu.com` -> `http://new-api-app:3000`
- `admin.openstaryu.com` -> `http://cpa-claude-proxy:8318`
- 两个域名均通过 Let's Encrypt 自动签发证书，并限制 ALPN 为 `http/1.1`。
- Traefik HTTP/3/QUIC 已关闭，不公开 `443/udp`。

## 当前部署版本

- 后端提交：`e774bd53`（Claude prompt too long 本地拦截、1M 上下文修正、缺失 text 字段修复）
- 前端提交：`f819c3e`（未变）
- 最近一次按本文档部署时间：`2026-06-13T03:53:03+00:00`
- 本次部署后端二进制 sha256：`324cb5b5a489e0c458b73978c9c9eaf3e5bf8eefd36878573adff58db033eda9`
- 本次部署后端压缩包 sha256：`5fe498c16d60c8aea1bd55dfc8ddc34ae654645391647c5e562e9caf82f8ed18`
- 本次部署前端 management.html sha256：未变，沿用 `c251642a1e153348df79a706c8b647c5e8975fd9ed9f2de1adb6697a7c986dfe`
- 部署前运行版本 backend=`6987d860`，frontend=`f819c3e`
- 部署前后端二进制备份：`/opt/cpa-claude-proxy-backups/CLIProxyAPI-before-deploy-20260613-035300.bak`（可回滚到 `6987d860`）
- 部署前账号备份（exclude logs）：`/opt/cpa-claude-proxy-backups/auths-20260613-035300.tgz`
- 部署后 `auths` 目录下 1192 个文件（含 `proxy_pool.json`；账号增删由晓宇手动管理）

### 本轮变更说明（e774bd53 / f819c3e）

仅后端部署。本轮合并两次 Claude 客户端请求兼容修复：一类是超上下文 `prompt is too long`，另一类是缺失 `text` 字段导致的 `Field required`。

1. 更新 Claude 静态模型能力：`claude-sonnet-4-6` 的 `context_length` 从 `200000` 修正为 `1000000`，和官方当前 1M 上下文能力一致。
2. Claude 非流式、流式生成请求在发往上游前增加本地 prompt token 上限检查；明显超过模型上下文窗口时返回本地 400，不再打 Anthropic 官方，也不污染账号健康。
3. `context-1m-2025-08-07` 不再作为 1M 的唯一判断，只作为兼容旧模型能力的提升信号；Sonnet 4.6 即使不带 beta 也按 1M 处理。
4. 新增本地错误码 `claude_prompt_too_long` 并纳入本地 request guard；上游纯文本 `prompt is too long: ... tokens > ... maximum` 400 也归类为客户端请求格式问题，不写账号 `LastError`、不标记账号不可用。
5. Claude 出站前清理非法 text block：`{"type":"text"}`、`{"type":"text","text":null}`、空字符串和纯空白 text 会被移除；如果 content 数组清空则补 `{"type":"text","text":"."}`。
6. text block 修复覆盖 `messages[*].content[*]`、`system[*]` 以及 `tool_result.content[*]` 等嵌套 content 数组；上游兜底返回的 `... .text: Field required` 400 也归类为客户端请求格式问题。
7. 本地验证：`git diff --check`、`go test ./sdk/cliproxy/auth -count=1`、`go test ./internal/registry -skip TestCodexFreeModelsExcludeGPT55 -count=1`、`go test ./internal/runtime/executor -skip 'TestEnsureAccessToken_WarmTokenLoadsCreditsHint|TestUpdateAntigravityCreditsBalance_LoadCodeAssistUserAgent' -count=1`、`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o ... ./cmd/server` 均通过。`TestCodexFreeModelsExcludeGPT55` 仍为既存 Codex free tier 断言失败，和本轮 Claude 变更无关。
8. 部署后验证：容器 `cpa-claude-proxy` 为 Up，运行二进制 sha256 与本地一致，`8318` 仍只监听 `127.0.0.1`，本机 `healthz 200`、`management 200`，公网 `https://api.openstaryu.com/` 与 `https://admin.openstaryu.com/management.html` 均为 200。

### 上一轮变更说明（6987d860 / f819c3e）

仅后端部署。本轮处理 Anthropic 已下架 `claude-fable-5` 后仍有客户缓存/手写旧模型名继续请求的问题：

1. 从嵌入式 Claude 模型目录移除 `claude-fable-5`，并在静态/远程/配置模型注册时统一过滤 `claude-fable-5` 与 `claude-fable-5-*`，避免号池 `/v1/models` 继续暴露已下架模型。
2. Claude 非流式、流式、count tokens 三条 executor 入口在发往上游前本地拦截 `claude-fable-5` / `claude-fable-5-*`，返回 404 和 `Claude Fable 5 is not available. Please use Opus 4.8.`，不再打 Anthropic 官方。
3. 新增本地错误码 `claude_model_unavailable` 并纳入本地请求 guard 分类；调度器收到该错误会直接返回客户端，不写账号 `LastError`、不标记账号不可用、不触发模型冷却。
4. 旧的 Fable 专属 thinking 能力测试迁移到动态 Mythos 测试，保留 always-adaptive thinking 逻辑覆盖，但不再要求静态目录保留 Fable 元数据。
5. 本地验证：`git diff --check`、`go test ./internal/registry -run 'TestClaudeStaticModelsExcludeFable5|TestClaudeModelsFilterFable5FromStaleRemoteCatalog' -count=1`、`go test ./internal/thinking ./sdk/cliproxy ./sdk/cliproxy/auth -count=1`、`go test ./internal/runtime/executor -skip 'TestEnsureAccessToken_WarmTokenLoadsCreditsHint|TestUpdateAntigravityCreditsBalance_LoadCodeAssistUserAgent' -count=1`、`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o ... ./cmd/server` 均通过。`go test ./internal/registry -run TestCodexFreeModelsExcludeGPT55 -count=1` 仍有既存 Codex free tier 断言失败，和本轮 Claude/Fable 变更无关。
6. 部署后验证：容器 `cpa-claude-proxy` 为 Up，运行二进制 sha256 与本地一致，`8318` 仍只监听 `127.0.0.1`，本机 `healthz 200`、`management 200`，公网 `https://api.openstaryu.com/` 与 `https://admin.openstaryu.com/management.html` 均为 200。

### 上一轮变更说明（a827ab9d / f819c3e）

仅后端部署。本轮修复客户常见的两类 Claude 上游 400：

1. 出站前修复 assistant `tool_use` 历史消息：如果下一条消息不是 user，则立即插入 user `tool_result`；如果下一条 user 缺少部分 `tool_result`，则补齐缺失项；如果 text 排在 `tool_result` 前面，则重排为 `tool_result` 在前、原 text 在后。
2. 合成的缺失 `tool_result` 使用 `is_error: true` 和 `Tool result unavailable.`，避免伪造成工具成功执行，同时满足 Anthropic 对工具结果邻接和排序的格式要求。
3. 采样参数归一化：`claude-fable-5`、`claude-mythos-5`、`claude-mythos-preview`、`claude-opus-4-8`、`claude-opus-4-7` 出站前删除 `temperature/top_p/top_k`；其他模型同时带 `temperature` 和 `top_p` 时删除 `top_p`。
4. active thinking 请求继续把 `temperature` 归一化为 `1`，并同步删除 `top_p`，避免 thinking 限制和采样参数互斥同时触发 400。
5. 新增 `tool_use ids were found without tool_result blocks immediately after` 与 ``temperature` and `top_p` cannot both be specified` 两类纯文本 400 到客户端请求形态分类，不污染账号健康、不触发全池账号惩罚。
6. 本地验证：`go test ./internal/runtime/executor -run 'Claude|RepairClaude|NormalizeClaude' -count=1`、`go test ./sdk/cliproxy/auth -count=1`、`go test ./internal/runtime/executor -skip 'TestEnsureAccessToken_WarmTokenLoadsCreditsHint|TestUpdateAntigravityCreditsBalance_LoadCodeAssistUserAgent' -count=1`、`git diff --check`、`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o ... ./cmd/server` 均通过。未跳过的 `go test ./internal/runtime/executor` 仍有两个既存 Antigravity credit mock URL 测试失败，和本轮 Claude 变更无关。
7. 部署后验证：容器 `cpa-claude-proxy` 为 Up，运行二进制 sha256 与本地一致，`8318` 仍只监听 `127.0.0.1`，本机 `healthz 200`、`management 200`，公网 `https://api.openstaryu.com/` 与 `https://admin.openstaryu.com/management.html` 均为 200。

### 上一轮变更说明（10c06c12 / f819c3e）

仅后端部署。本轮继续针对客户高并发首字慢和 NewAPI `client_gone/context canceled` 激增做号池侧止血：

1. 本地混合 provider 路径的账号选择从“先 pick 后 reserve”改为在同一把 manager 锁内完成候选过滤、选择器挑选和 runtime slot 预占，避免多个并发请求同时选中同一满号后再失败重选。
2. 非 streaming、streaming、count tokens 三条混合执行链路都使用已预占账号；Home 控制面模式仍保留原外层 reserve 行为。
3. 已预占账号准备执行模型时忽略当前请求刚写入的 runtime RPM 计数，避免请求把自己误判为已 RPM 满；模型禁用/冷却等状态仍按原逻辑过滤。
4. OpenAI Chat Completions 与 Completions 流式入口在等待上游首个 payload 前也会按 `streaming.keepalive-seconds` 刷 SSE `: keep-alive` 注释，避免首包前连接完全静默。
5. 流式 keepalive 默认从关闭改为 15 秒；`keepalive-seconds: 0` 使用默认值，`< 0` 才关闭。
6. 本地验证：`go test ./sdk/cliproxy/auth -count=1 -timeout 30s`、`go test ./sdk/api/handlers ./sdk/api/handlers/openai -run 'TestStreamingKeepAliveIntervalDefaultsToFifteenSeconds|TestChatCompletionsStreamingEmitsKeepAliveBeforeFirstPayload|TestForwardResponsesStreamSeparatesDataOnlySSEChunks|TestForwardResponsesStreamRepairsEmptyCompletedOutputFromDoneItems' -count=1`、`go test ./internal/runtime/executor -run 'TestRepairClaudeRequestShape_AddsUserTurnAfter(FableAssistantTextPrefill|Opus48AssistantTextPrefill|AssistantPrefillForNewClaude46Models|ThinkingAssistantTextPrefill|NonTextAssistantPrefill)|TestRepairClaudeRequestShape_KeepsAssistantPrefillForLegacyClaudeModels' -count=1`、`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o ... ./cmd/server` 均通过。
7. 部署后验证：容器 `cpa-claude-proxy` 为 Up，运行二进制 sha256 与本地一致，`8318` 仍只监听 `127.0.0.1`，本机 `healthz 200`、`management 200`，公网 `https://api.openstaryu.com/` 与 `https://admin.openstaryu.com/management.html` 均为 200。

### 上一轮变更说明（e2eced84 / f819c3e）

前后端均部署。本轮重点解决客户高并发下首字时快时慢，以及 NewAPI 日志中仍存在的 Claude assistant prefill 400：

1. Claude 默认 `max_sessions` 从 5 提高到 10；管理面板单账号/批量策略默认值和占位提示同步为 10，后端字段保存后通过 `authManager.Update` 热加载到调度器，无需重启。
2. 调度选账号前会过滤“新会话已满”的账号；已有真实会话仍可继续复用同一账号，无会话 ID 请求仍不占用会话槽，避免新请求先选到满账号再失败重试导致首字抖动。
3. 新增运行态清会话接口 `POST /auth-files/runtime-sessions/clear`，支持清全部或指定账号的本地会话占用；只释放内存中的会话槽，不删除账号、不改认证文件。
4. 管理面板生产首页账号池新增“清会话”和“清选中会话”入口；旧通用授权页也保留同类入口。
5. `assistant prefill` 修复范围扩大：`claude-sonnet-4-6`、`claude-opus-4-6` 加入不支持 prefill 的模型名单；命中模型或 active thinking 请求只要最终以 `role:"assistant"` 收尾，就追加空 user turn，不再限定 assistant content 必须全是 text block。`thinking`、`redacted_thinking`、`tool_use`、混合 content 都会被修复。
6. 本地验证：`go test ./sdk/cliproxy/auth ./internal/api/handlers/management ./internal/api -count=1`、`go test ./internal/runtime/executor -run 'Claude|TestRepairClaudeRequestShape' -count=1`、`go build -o /private/tmp/cpa-cli-proxy-api-test ./cmd/server`、前端 `npm run type-check`、`npm run lint`、`npm run build` 均通过；`rg -n -i "claude|anthropic" dist` 无输出。
7. 部署后验证：容器 `cpa-claude-proxy` 为 Up，`8318` 仍只监听 `127.0.0.1`，本机 `healthz 200`、`management 200`，公网 `https://api.openstaryu.com/` 与 `https://admin.openstaryu.com/management.html` 均为 200。

### 上一轮变更说明（63739474）

仅后端部署。本轮基于 NewAPI 使用日志中最新高频 Claude 上游 400 做出站兼容与错误分类修复：

1. `claude-sonnet-4-6` 收到 `output_config.effort=max` 时降级为 `high`，避免 `level "max" not supported`。
2. `claude-haiku-4-5-20251001` 收到 `output_config.effort` 时转成 `thinking.budget_tokens`，不再向 budget-only 模型透传 effort 参数。
3. 空 text block、空字符串 content、纯空白 text 会改成非空白占位 `"."`，避免 `text content blocks must contain non-whitespace text`。
4. Claude 出站前会把 `developer` role 提升为顶层 `system`，把 OpenAI 风格 `tool` role 转成 `user` + `tool_result`。
5. OpenAI 风格 `tools[].type=function` 会转成 Claude custom tool schema；仅 custom/function 工具补 `input_schema.type`，不改 Claude 内置工具。
6. `claude-fable-5` 的强制 `tool_choice`（`any` / `tool` / `function`）会降级为 `auto`，避免 Fable + always-adaptive thinking 下被上游拒绝。
7. 新增这些 400 到客户端请求形态分类：`top_p deprecated`、non-whitespace text、effort parameter、tool_choice、unexpected role、function tool tag；不污染账号健康、不触发全池账号惩罚。

### 上一轮变更说明（eb5ad9fe，已被 63739474 取代）

仅后端部署。本轮修复 Claude 上游 `messages: text content blocks must be non-empty`：

1. `messages[*].content` 中空字符串 text block 会删除；如果该消息只剩空 text，改为单空格占位。
2. `messages[*].content` 本身是空字符串时，改为单空格占位。
3. system-only fallback 与 assistant-prefill fallback 的空 user turn 也改为单空格占位，避免号池自己生成上游不接受的空 text。
4. `text content blocks must be non-empty` 这类 400 归类为客户端请求形态问题，不污染账号健康、不触发全池账号惩罚。

### 上一轮变更说明（ab437822，已被 eb5ad9fe 取代）

仅后端部署。本轮修复 Claude 新式模型请求形态兼容问题：

1. `messages[*].role="system"` 送上游前提升为顶层 `system` 文本块，并从 `messages` 删除；system-only 请求会补空 user fallback，避免上游返回 `role 'system' is not supported on this model`。
2. 新式 adaptive-only / always-adaptive 模型（当前覆盖 `claude-fable-5`、`claude-mythos-5`、`claude-mythos-preview`、`claude-opus-4-8`、`claude-opus-4-7`）的 text-only assistant prefill 会追加空 user turn；任何显式 `thinking.type=enabled/adaptive/auto` 的请求也按同样规则处理，避免 `conversation must end with a user message`。
3. 上述新式模型出站前删除 `temperature`，避免上游返回 ``temperature` is deprecated for this model`；旧模型保持客户端 `temperature` 原值，带 active thinking 的旧模型仍按既有规则归一到 `1`。
4. `role system`、`assistant prefill`、`temperature deprecated` 这类 400 归类为客户端请求形态问题，不污染账号健康、不触发全池账号惩罚。

### 上一轮变更说明（fe34c944，已被 ab437822 取代）

仅后端部署。本轮修复客户端历史消息里非法 Claude 工具调用 ID 导致的上游 400：

1. `messages[*].content[*].tool_use.id` 送上游前会统一规范化为 Claude 允许的 `^[a-zA-Z0-9_-]+$` 形态。
2. 对应 `tool_result.tool_use_id` 使用同一映射同步改写，保持同一请求内工具调用和工具结果配对。
3. 合法 ID 原样保留；非法 ID 使用原始 ID 的稳定短哈希生成 `toolu_<hash>`，避免暴露客户端特殊字符形态。
4. 该类错误仍属于客户端请求形态问题，不会标记账号永久不可用，也不参与全池账号污染。

### 上一轮变更说明（4cde9eaa，逻辑改动 834a9200，已被 fe34c944 取代）

仅后端部署。本轮重点是 Claude 上游错误分类，不做全局 400 重试：

1. `Identity verification is required to continue.` 归类为 `identity_verification_required`，按永久/半永久账号不可用处理，账号移出轮转并触发换号重试。
2. 批量导入探测更严格：`identity_verification_required` 会拒绝导入；未匹配到明确永久账号错误的 Claude 上游 HTTP 400 也会以 `probe_bad_request` 拒绝导入，避免坏账号进池。
3. OAuth/Claude Code 账号请求会在上游前剥离 `context-1m-2025-08-07`，避免 `This authentication style is incompatible with the long context beta header.`；API Key 形态仍允许显式 1M beta。
4. `500/529 Overloaded` 按上游临时过载处理，允许尝试下一个账号，但不把当前账号标记为永久不可用、不挂模型、不写冷却。
5. 客户端请求形态错误仍按请求错误返回，例如 malformed payload、thinking 参数非法、prompt too long 等，不扫全池重试，避免误伤正常账号。

### 上一轮变更说明（fa83c466，已被 4cde9eaa 取代）

仅后端、3 文件（1 改 + 2 测试）。修复客户反馈的两个问题：

1. **1h 缓存被降级成 5min**：号池伪装注入的 Claude Code system 块（身份块 / runtime context 块）带默认 5m `cache_control` 且排在客户端块之前。Anthropic 要求按 `tools→system→messages` 顺序「1h 块不能排在 5m 块之后」，原 `normalizeCacheControlTTL` 为满足该约束**把客户端显式的 `ttl="1h"` 剥掉、静默降级成 5m**。改为：只要请求含任意客户端显式 1h 块（仅客户端写 `ttl="1h"`，注入块不写 ttl），就把**所有** ephemeral 块统一升级为 1h，保住客户端 1h 意图、顺序天然合法；无 1h 块时字节级原样返回，默认 5m 行为不变。升级只改 `cache_control.ttl` 不动 `text`，不影响 guard 的 system 块哈希审计。
2. **1M 上下文被禁用**：`context-1m-2025-08-07` 原在 `claudeDroppedBetaTokens` 被强制丢弃且不在 guard 白名单——透传也会被自家 guard 判 unexpected beta 拦截。改为从丢弃表移除 + 加入 `claudeAllowedBetaTokens` optional 白名单（**不**加入默认注入列表，仅客户端显式请求时透传）。**风险**：走此路径的请求（含 OAuth 订阅号）会暴露真实 Claude Code 不发送的 1M 指纹，存在风控可能，是已知并被接受的取舍。

改动文件：`internal/runtime/executor/claude_executor.go`、`internal/runtime/executor/claude_executor_test.go`、`internal/runtime/executor/claude_mimicry_audit_test.go`。`auth` 包、`helps` 包（含 AGENTS.md 强制 cache breakdown 5 回归）、`executor` 包（除 2 个预存 antigravity loadCodeAssist mock URL 失败外）全过；`go build ./...` 通过。

**部署后生产实测**：① 1h 缓存——同最初复现请求,`ephemeral_1h_input_tokens` 从 0 → 1976、`ephemeral_5m` 从 3835 → 0,客户端 1h 完整保留;② 1M——带 `context-1m-2025-08-07` 的请求正常返回 200(修复前会被丢弃),360K-450K token 大输入正常处理。注:该上游路径默认上下文窗口已 >360K,未能用「不带 beta 被拒/带 beta 通过」建立 200K→1M 的精确边界对照。

### 上一轮部署版本（已被 fa83c466 取代）

- 后端提交：`71b68992`（请求体过大 413 不再污染账号健康：`checkClaudeUpstreamBodySize` 改返回带 `LocalRequestTooLargeErrorCode` 的本地守卫错误，conductor 短路豁免、不写入账号 `LastError/Status`，客户端仍收到 413）
- 前端提交：`c0ef735`（未变）
- 部署时间：`2026-06-06T04:12:09+00:00`，binary sha256 `f07396238658f40fc2af74b6d8d6eac05cdcc40f388eec6bdfe7b6cb0bcfe9bf`
- 回滚备份：`/opt/cpa-claude-proxy-backups/CLIProxyAPI-before-deploy-20260606-000917.bak`（回到 `644d9ea0`）

### 本轮新增配置项

- `claude-max-request-bytes`（int，默认 `31457280` = 30 MiB）：发往 Claude 上游前的请求体字节上限，超限本地返回 413 而不转发上游（避免超大请求反复打上游污染账号行为画像）。`<= 0`（如 `-1`）表示不限制；未配置（零值）按默认 30 MiB。
- `claude-max-concurrent-requests`（int，默认 `0` = 不限制）：CPA 同时在途的 Claude 上游请求数上限（服务器带宽/连接保护，**非**账号保护——账号保护由 per-account RPM 负责）。流式请求占用槽位直到流读完。达到上限时新请求立即返回 429（`Retryable=false`，不换账号重试、不排队）。`<= 0` 不限制。前端策略页可配置，保存后后端按新值热更新生效；无需重启或重新部署二进制。

> 注：本轮交叉编译未注入 git commit 的 ldflags，容器日志 `Version: dev, Commit: none` 属正常；权威版本以 `DEPLOYED_COMMITS` 为准。
> 本轮三个功能：① 全局并发上限（带宽保护阀，默认 0 待命）；② 伪装诊断三项（UA 基线 2.1.154→2.1.161、前端兜底常量修正、审计排除故意未伪装请求且 guard 仍生效）；③ `account_session_invalid` 403（session_key 失效）归入永久失效自动禁用，不再误标 payment_required/请求异常。伪装基线经一手二进制核实未落后，`X-Stainless-Package-Version` 保持 0.94.0（vendored，勿追 npm 0.100.1）。

- 最近一次有效完整账号备份(92 个)：`/opt/cpa-claude-proxy-backups/auths-20260531-061110.tgz`
- 最近一次网络入口收口：`2026-05-28T11:32:01+00:00`
- 最近一次网络入口收口备份：`/root/openstaryu-hardening-20260528-113201`
- 初始迁移来源：旧服务器 `38.76.196.12:/opt/cpa-claude-proxy`

服务器上可用下面命令查看实际部署提交：

```bash
cat /opt/cpa-claude-proxy/DEPLOYED_COMMITS
```

如果本文档记录与服务器上的 `DEPLOYED_COMMITS` 不一致，以服务器文件为准，并在下一次文档维护时同步更新本文档。

## 标准更新流程

后续更新不要在服务器上临时改代码。标准流程是本地构建、上传产物、记录提交、Compose 重启。

### 0. 判断本次更新范围

- 只改后端：只构建和上传 `CLIProxyAPI-linux-amd64`，保留服务器现有 `static/management.html`。
- 只改前端：只构建和上传 `dist/index.html`，保留服务器现有后端二进制。
- 前后端都改：两个产物都构建上传。

不要在服务器上 `git pull`、临时改代码或现场编译。服务器只运行本地构建后上传的产物。

### 1. 本地确认提交

```powershell
git -C F:\claude反代\CLIProxyAPI rev-parse --short HEAD
git -C F:\claude反代\Cli-Proxy-API-Management-Center rev-parse --short HEAD
```

确认需要部署的改动已经提交并推送。部署记录中的 `backend` 和 `frontend` 必须写入实际部署的提交号。

### 2. 本地构建 Linux 后端二进制

```powershell
Set-Location F:\claude反代\CLIProxyAPI
$env:GOOS='linux'
$env:GOARCH='amd64'
$env:CGO_ENABLED='0'
F:\GO语言\bin\go.exe build -o ..\.codex_tmp\prod-deploy\CLIProxyAPI-linux-amd64 .\cmd\server
```

构建后记录文件哈希，便于和服务器上的运行产物核对：

```powershell
Get-FileHash -Algorithm SHA256 F:\claude反代\.codex_tmp\prod-deploy\CLIProxyAPI-linux-amd64
```

上传前先压缩后端二进制，避免直接传输大文件时因网络抖动产生残缺文件：

```powershell
tar -czf F:\claude反代\.codex_tmp\prod-deploy\CLIProxyAPI-linux-amd64.tgz -C F:\claude反代\.codex_tmp\prod-deploy CLIProxyAPI-linux-amd64
Get-FileHash -Algorithm SHA256 F:\claude反代\.codex_tmp\prod-deploy\CLIProxyAPI-linux-amd64.tgz
```

### 3. 本地构建前端

```powershell
Set-Location F:\claude反代\Cli-Proxy-API-Management-Center
npm run build
rg -n -i "claude|anthropic" dist
```

`npm run build` 会自动执行 `scripts/sanitize-management-html.mjs`。`rg` 对 `dist` 必须无输出，否则说明管理面板单文件仍有可被静态扫描命中的敏感 provider 明文，不要上传。

前端生产产物是：

```text
F:\claude反代\Cli-Proxy-API-Management-Center\dist\index.html
```

上传到服务器后应安装为：

```text
/opt/cpa-claude-proxy/static/management.html
```

### 4. 准备本机上传临时目录

Windows 本机路径包含中文时，部分 Python/PowerShell 上传脚本可能把路径转码为乱码。建议先复制到纯 ASCII 路径：

```powershell
$stage = 'C:\tmp\cpa-prod-deploy'
New-Item -ItemType Directory -Path $stage -Force | Out-Null
Copy-Item -LiteralPath 'F:\claude反代\.codex_tmp\prod-deploy\CLIProxyAPI-linux-amd64.tgz' -Destination (Join-Path $stage 'CLIProxyAPI-linux-amd64.tgz') -Force
Copy-Item -LiteralPath 'F:\claude反代\Cli-Proxy-API-Management-Center\dist\index.html' -Destination (Join-Path $stage 'management.html') -Force
```

如果本次只更新后端或只更新前端，只复制对应产物。

### 5. 上传到服务器临时目录

```bash
mkdir -p /tmp/cpa-claude-stage
```

上传文件：

- 后端压缩包：`/tmp/cpa-claude-stage/CLIProxyAPI-linux-amd64.tgz`
- 前端：`/tmp/cpa-claude-stage/management.html`

### 6. 在服务器上备份账号数据

```bash
mkdir -p /opt/cpa-claude-proxy-backups
tar -czf /opt/cpa-claude-proxy-backups/auths-$(date +%Y%m%d-%H%M%S).tgz -C /opt/cpa-claude-proxy auths
```

如果 `auths/logs` 正在高频写入或轮转，`tar` 可能因日志文件变化返回非 0。账号回滚关键数据是账号 JSON 与代理配置，可在这种情况下排除日志目录：

```bash
tar --exclude='auths/logs' -czf /opt/cpa-claude-proxy-backups/auths-$(date +%Y%m%d-%H%M%S).tgz -C /opt/cpa-claude-proxy auths
```

### 7. 覆盖运行产物并重启

前后端都更新时：

```bash
cd /tmp/cpa-claude-stage
rm -f CLIProxyAPI-linux-amd64
tar -xzf CLIProxyAPI-linux-amd64.tgz
chmod 0755 CLIProxyAPI-linux-amd64
sha256sum CLIProxyAPI-linux-amd64
install -m 0755 /tmp/cpa-claude-stage/CLIProxyAPI-linux-amd64 /opt/cpa-claude-proxy/runtime/CLIProxyAPI
install -m 0644 /tmp/cpa-claude-stage/management.html /opt/cpa-claude-proxy/static/management.html
cd /opt/cpa-claude-proxy
docker compose up -d
docker compose restart cpa-claude-proxy
```

`sha256sum` 必须与本地未压缩二进制的 SHA256 一致；不一致时停止部署，重新上传压缩包。

注意：只执行 `docker compose up -d` 可能不会重启已经运行的容器；覆盖二进制后必须显式重启或使用 `docker compose up -d --force-recreate`，否则新产物可能不会生效。

只更新后端时，不要覆盖 `management.html`：

```bash
cd /tmp/cpa-claude-stage
rm -f CLIProxyAPI-linux-amd64
tar -xzf CLIProxyAPI-linux-amd64.tgz
chmod 0755 CLIProxyAPI-linux-amd64
sha256sum CLIProxyAPI-linux-amd64
install -m 0755 /tmp/cpa-claude-stage/CLIProxyAPI-linux-amd64 /opt/cpa-claude-proxy/runtime/CLIProxyAPI
cd /opt/cpa-claude-proxy
docker compose up -d
docker compose restart cpa-claude-proxy
```

只更新前端时，不需要重启也能被静态文件读取；但为了让部署行为一致，仍建议重启一次：

```bash
install -m 0644 /tmp/cpa-claude-stage/management.html /opt/cpa-claude-proxy/static/management.html
cd /opt/cpa-claude-proxy
docker compose restart cpa-claude-proxy
```

### 8. 写入提交记录

```bash
cat > /opt/cpa-claude-proxy/DEPLOYED_COMMITS <<EOF
backend=<backend_commit>
frontend=<frontend_commit>
deployed_at=$(date -Is)
EOF
```

### 9. 清理临时目录

```bash
rm -rf /tmp/cpa-claude-stage
```

本机上传临时目录也应清理：

```powershell
Remove-Item -LiteralPath 'C:\tmp\cpa-prod-deploy' -Recurse -Force
```

## 标准验证流程

每次部署或排障后执行：

服务器本机验证：

```bash
docker ps --filter name=cpa-claude-proxy --format 'table {{.Names}}\t{{.Image}}\t{{.Ports}}\t{{.Status}}'
ss -lntp | grep ':8318'
curl -sS -o /dev/null -w 'healthz %{http_code}\n' --max-time 10 http://127.0.0.1:8318/healthz
curl -sS -o /dev/null -w 'management %{http_code}\n' --max-time 10 http://127.0.0.1:8318/management.html
find /opt/cpa-claude-proxy/auths -maxdepth 1 -type f -printf '%f\n' | sort
```

本机公网验证：

```powershell
curl.exe -sS -o NUL -w "api %{http_code}\n" --max-time 20 https://api.openstaryu.com/
curl.exe -sS -o NUL -w "management %{http_code}\n" --max-time 20 https://admin.openstaryu.com/management.html
```

预期：

- `cpa-claude-proxy` 容器为 `Up`
- `8318` 端口只由 `docker-proxy` 在 `127.0.0.1` 监听
- `healthz 200`
- `management 200`
- `api.openstaryu.com` 和 `admin.openstaryu.com` HTTPS 均为 `200`
- `auths` 目录存在 Claude 账号 JSON 和 `proxy_pool.json`

端口收口验证：

```bash
ss -lntup
ufw status verbose
docker ps --format 'table {{.Names}}\t{{.Ports}}\t{{.Status}}'
```

预期公网监听只保留：

- `80/tcp`
- `443/tcp`
- `56260/tcp`

预期本机监听包括：

- `127.0.0.1:8318` CPA
- `127.0.0.1:13000` NewAPI
- `127.0.0.1:18080` sub2api
- `127.0.0.1:8000` Coolify UI
- `127.0.0.1:8080` Traefik dashboard
- `127.0.0.1:6001-6002` Coolify realtime

## 常用排障命令

查看容器日志：

```bash
docker logs --tail 200 cpa-claude-proxy
```

确认前端静态文件挂载：

```bash
docker exec cpa-claude-proxy /bin/sh -c 'env | grep MANAGEMENT_STATIC_PATH; ls -la /CLIProxyAPI/static'
```

查看非敏感关键配置：

```bash
grep -nE '^(host|port|auth-dir|remote-management|routing):|^[[:space:]]+(allow-remote|disable-control-panel|disable-auto-update-panel|panel-github-repository|session-affinity|session-affinity-ttl|strategy):' /opt/cpa-claude-proxy/config.yaml
```

查看代理池数量：

```bash
python3 - <<'PY'
import json
from pathlib import Path
path = Path('/opt/cpa-claude-proxy/auths/proxy_pool.json')
data = json.loads(path.read_text()) if path.exists() else {}
proxies = data.get('proxies') or []
print(f'proxy_count={len(proxies)} enabled={sum(1 for p in proxies if p.get("enabled", True))}')
PY
```

确认没有影响其他生产项目：

```bash
docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Ports}}\t{{.Status}}'
```

## 回滚原则

优先回滚运行产物，不删除账号数据。

如果账号数据被误改：

```bash
cd /opt/cpa-claude-proxy
tar -czf /opt/cpa-claude-proxy-backups/auths-before-rollback-$(date +%Y%m%d-%H%M%S).tgz auths
rm -rf auths
mkdir -p auths
tar -xzf /opt/cpa-claude-proxy-backups/<backup-file>.tgz -C /opt/cpa-claude-proxy
docker compose restart cpa-claude-proxy
```

如果容器无法启动：

```bash
cd /opt/cpa-claude-proxy
docker compose logs --tail 200
docker compose up -d
```

## 注意事项

- 不要把 GitHub 私有仓库凭据放在服务器上；服务器只运行构建产物。
- 不要把 SSH 密码、管理密码、API Key、Claude token 写进本文档。
- 常规代码更新时只操作 `/opt/cpa-claude-proxy` 和 `/opt/cpa-claude-proxy-backups`。
- 不要把 CPA 重新改回公网 `0.0.0.0:8318`。
- 不要把 NewAPI 的 CPA 号池渠道改回公网 IP `:8318`；应保持 `http://cpa-claude-proxy:8318`。
- 不要把 `/CLIProxyAPI/config.yaml` 改成只读挂载；管理 API 需要写回配置文件并热更新运行时配置。
- 不要改 `/opt/new-api-production`、`/opt/sub2api-production`、Coolify 目录或相关容器，除非任务明确涉及域名反代、端口收口或对应服务本身。
- 账号、代理、管理密钥和客户端 API Key 由旧服务器配置/数据迁移而来，后续应通过管理面板维护。
- 前端源码仓库是 `F:\claude反代\Cli-Proxy-API-Management-Center`，不是后端仓库内的 `static` 目录。
- 后端源码仓库是 `F:\claude反代\CLIProxyAPI`，生产服务器不要保存 GitHub 凭据。
