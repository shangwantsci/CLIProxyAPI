# CPA 二开项目文件地图

本文档用于避免改错仓库、改错页面、改错产物。开始任何改动前，先确认问题属于后端、前端、部署流程还是 Claude Code 伪装链路。

## 项目边界

- 后端仓库：`F:\claude反代\CLIProxyAPI`
- 前端仓库：`F:\claude反代\Cli-Proxy-API-Management-Center`
- 生产后端二进制：`/opt/cpa-claude-proxy/runtime/CLIProxyAPI`
- 生产前端单文件：`/opt/cpa-claude-proxy/static/management.html`
- 生产部署文档：`docs/production-deployment-23.153.36.12.md`

不要把前端源码问题改到后端仓库，也不要把后端 API 问题只改前端展示。

## 后端入口

### 管理 API 与账号池

- `internal/api/handlers/management/auth_files.go`
  - Claude 账号列表、健康字段、状态标签、启用/停用、字段保存、删除、重认证。
  - 常见字段：`health_status`、`status_reason`、`route_state`、`recoverability`、`last_error`。
- `internal/api/handlers/management/claude_probe_jobs.go`
  - Claude 一键探测任务。
  - 处理 profile/usage 探测、探测结果分类、任务统计。
- `internal/api/handlers/management/api_tools.go`
  - 管理面板“API 调用/探测”能力。
  - 会记录 Claude OAuth profile/usage 的健康状态。
- `internal/api/handlers/management/*_test.go`
  - 管理 API 回归测试。修改账号状态分类时必须补这里的测试。

### Claude 正常请求与路由健康

- `internal/runtime/executor/claude_executor.go`
  - Claude 请求执行器。
  - 处理请求转换、header 注入、伪装、上游错误、限流与健康事件。
- `sdk/cliproxy/auth/conductor.go`
  - 多账号路由、账号状态、错误隔离、冷却、永久禁用逻辑。
  - 正常用户请求中发现账号不可用时，最终会影响这里的状态。
- `sdk/cliproxy/auth/selector.go`
  - 账号选择逻辑。
- `sdk/cliproxy/auth/*_test.go`
  - 路由与账号状态测试。修改正常请求错误分类时必须补这里的测试。

### Claude Code 伪装相关

- `internal/runtime/executor/helps/claude_device_profile.go`
  - Claude Code UA 与 Stainless header 基线。
  - 真实 Claude Code 指纹学习与缓存。
- `internal/runtime/executor/helps/claude_system_prompt.go`
  - Claude Code system prompt 静态块。
- `internal/runtime/executor/helps/cloak_utils.go`
  - 是否启用 cloak 的判断。
- `internal/runtime/executor/claude_mimicry_audit.go`
  - Claude Code 伪装审计与 guard。
- `internal/runtime/executor/claude_mimicry_audit_test.go`
  - 伪装审计回归测试。

### Claude token usage 与缓存计费

- `internal/runtime/executor/helps/billable_usage.go`
  - `claude-billable-usage` 的输入 token projection。
  - 高风险规则：如果上游返回 Claude cache breakdown，不得清零或隐藏 `cache_creation_input_tokens`、`cache_read_input_tokens`、`cached_tokens`、`cache_creation.ephemeral_*`。
- `internal/runtime/executor/helps/usage_helpers.go`
  - Claude usage 解析、流式 usage 合并、usage detail total 归一化。
- `internal/translator/claude/openai/chat-completions/claude_openai_response.go`
  - Claude -> OpenAI Chat Completions 的 usage 映射。
- `internal/translator/claude/openai/responses/claude_openai-responses_response.go`
  - Claude -> OpenAI Responses 的 usage 映射。
- 相关测试：
  - `internal/runtime/executor/helps/billable_usage_test.go`
  - `internal/translator/claude/openai/chat-completions/claude_openai_response_test.go`
  - `internal/translator/claude/openai/responses/claude_openai-responses_response_test.go`

### 认证与账号文件

- `internal/auth/claude/anthropic_auth.go`
  - Claude OAuth、Claude AI browser/cookie 相关认证流程。
- `sdk/auth/filestore.go`
  - 账号文件读取、写入、metadata 到 runtime auth 的映射。
- `internal/watcher/synthesizer/file.go`
  - 文件账号热加载为运行时账号。

## 前端入口

前端仓库的维护地图已经存在：

```text
F:\claude反代\Cli-Proxy-API-Management-Center\docs\claude-account-pool-maintenance.md
```

修改前端账号池 UI 前必须先读这份文档。

### 生产账号池主页面

- `src/pages/DashboardPage.tsx`
  - 生产首页账号池真实实现。
  - 账号列表、筛选、批量操作、详情抽屉、设置弹窗、一键检测、导入账号都在这里。
- `src/pages/DashboardPage.module.scss`
  - 生产首页账号池样式。
- `src/services/api/authFiles.ts`
  - 账号池相关管理 API 封装。
- `src/services/api/claudeSessionImport.ts`
  - 批量抓取 sessionKey 并导入 Claude 账号。

### 容易改错的位置

- `src/pages/AuthFilesPage.tsx`
  - 原版 CPA 通用授权文件页。
  - 当前生产 `/auth-files/*` 已重定向到 `/`，Claude 专用账号池新交互通常不要只改这里。
- `dist/index.html`
  - 构建产物，不是源码。不要手改。

## 部署产物对应关系

- 后端源码改动后，构建：

```powershell
Set-Location F:\claude反代\CLIProxyAPI
$env:GOOS='linux'
$env:GOARCH='amd64'
$env:CGO_ENABLED='0'
F:\GO语言\bin\go.exe build -o ..\.codex_tmp\prod-deploy\CLIProxyAPI-linux-amd64 .\cmd\server
```

- 前端源码改动后，构建：

```powershell
Set-Location F:\claude反代\Cli-Proxy-API-Management-Center
npm run build
```

- 前端生产上传的不是整个 `dist`，而是：

```text
dist/index.html -> /opt/cpa-claude-proxy/static/management.html
```

## 修改前检查清单

1. 先确认问题发生在哪个入口：
   - 管理页 UI：前端仓库。
   - 管理 API 返回字段或账号状态：后端 `internal/api/handlers/management`。
   - 正常用户请求触发账号状态变化：后端 `sdk/cliproxy/auth` 和 Claude executor。
   - Claude Code 伪装：后端 `internal/runtime/executor` 和 `internal/runtime/executor/helps`。
2. 搜索用户能看到的文案，确认真实渲染文件。
3. 搜索 API 字段名，确认字段由前端计算还是后端返回。
4. 修改状态分类时，至少覆盖：
   - 列表展示。
   - 一键探测。
   - 管理 API 调用。
   - 正常用户请求。
5. 前端改动后运行：

```bash
npm run type-check
npm run lint
npm run build
```

6. 后端改动后运行相关包测试，并至少确认编译：

```powershell
F:\GO语言\bin\go.exe test -count=1 ./internal/api/handlers/management ./sdk/cliproxy/auth
F:\GO语言\bin\go.exe build -o .codex_tmp\cli-proxy-api-test.exe ./cmd/server
```

7. 部署前读 `docs/production-deployment-23.153.36.12.md`。
