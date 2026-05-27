# Codex 窗口交接文档

本文档用于在不同 Codex 窗口之间快速接手 CPA 二开项目。新窗口开始工作时，先读本文档，再按任务类型读相关专题文档。

## 项目目标

这是基于 CLIProxyAPI 的 CPA 二开项目，当前重点是：

- Claude Code / Claude OAuth 多账号池。
- Claude Code 伪装与指纹一致性。
- 管理面板中的账号健康、批量探测、代理池和策略设置。
- 生产服务器 `23.153.36.12:8318` 的稳定部署更新。

## 仓库与路径

- 后端仓库：`F:\claude反代\CLIProxyAPI`
- 前端仓库：`F:\claude反代\Cli-Proxy-API-Management-Center`
- 当前后端分支：`xiaoyu/claude-oauth-cookie-mimicry`
- 当前前端分支：`xiaoyu/claude-oauth-cookie-mimicry`
- 后端远端：`https://github.com/shangwantsci/CLIProxyAPI.git`

生产服务器只运行构建产物，不在服务器上保存 GitHub 凭据，不在服务器上临时改代码。

## 当前部署状态

- 生产入口：`http://23.153.36.12:8318/management.html`
- 健康检查：`http://23.153.36.12:8318/healthz`
- SSH：`root@23.153.36.12:41629`
- 后端部署提交：`1bdb365c`
- 前端部署提交：`00956ca`
- 最近一次账号备份：`/opt/cpa-claude-proxy-backups/auths-20260527-160019.tgz`

不要把 SSH 密码、管理密码、API Key、Claude token 或代理密码写进任何文档或提交。

服务器上以这个文件为部署事实来源：

```bash
cat /opt/cpa-claude-proxy/DEPLOYED_COMMITS
```

## 必读文档

- 部署更新：`docs/production-deployment-23.153.36.12.md`
- 文件地图：`docs/project-file-map.md`
- Claude Code 伪装：`docs/claude-code-mimicry.md`
- 前端账号池维护：`F:\claude反代\Cli-Proxy-API-Management-Center\docs\claude-account-pool-maintenance.md`

## 最近关键后端改动

- `0582f97d fix: distinguish Claude auth health states`
  - 把 Claude 账号健康状态拆成更细的原因，不再统一显示为已停用。
- `cd7a9c88 fix: classify Claude OAuth organization blocks`
  - 把 `OAuth authentication is currently not allowed for this organization.` 识别为 `organization_disabled`。
  - 覆盖管理 API 调用、一键探测和正常用户请求。
- `fe5fb943 fix: prioritize permanent Claude health`
  - 对历史上已经写成 `StatusError + Unavailable` 的组织禁用账号，列表也优先显示永久禁用。
- `1bdb365c Improve Claude Code mimicry and billable usage`
  - 对齐本机 Claude Code `2.1.152` 指纹、system prompt、headers 和 CCH 形态。
  - 注意：该提交中的 token usage projection 曾把 Claude cache breakdown 清零，导致 cctest 显示无缓存和计费倍率异常。后续修复的硬规则是：只在上游没有 cache breakdown 时才估算 billable input；一旦上游返回 cache create/read/cached 字段，Claude 原生响应、OpenAI 兼容响应和 usage queue 都必须保留 Anthropic cache 语义。

## 常用命令

后端相关测试：

```powershell
Set-Location F:\claude反代\CLIProxyAPI
F:\GO语言\bin\go.exe test -count=1 ./internal/api/handlers/management ./sdk/cliproxy/auth
```

后端编译检查：

```powershell
Set-Location F:\claude反代\CLIProxyAPI
F:\GO语言\bin\go.exe build -o .codex_tmp\cli-proxy-api-test.exe ./cmd/server
```

生产后端构建：

```powershell
Set-Location F:\claude反代\CLIProxyAPI
$env:GOOS='linux'
$env:GOARCH='amd64'
$env:CGO_ENABLED='0'
F:\GO语言\bin\go.exe build -o ..\.codex_tmp\prod-deploy\CLIProxyAPI-linux-amd64 .\cmd\server
```

前端检查：

```powershell
Set-Location F:\claude反代\Cli-Proxy-API-Management-Center
npm run type-check
npm run lint
npm run build
```

## 已知坑

- 后端仓库 `git status` 可能显示大量 `M`，但 `git diff --name-only` 可能只有 LF/CRLF 行尾警告，没有真实内容差异。不要随手 revert。
- 前端源码不在后端仓库。生产管理面板主要页面是前端仓库的 `src/pages/DashboardPage.tsx`。
- `src/pages/AuthFilesPage.tsx` 是原版通用授权文件页，生产 Claude 账号池通常不是这里。
- 前端上线产物是 `dist/index.html`，服务器上文件名是 `management.html`。
- Windows 中文路径可能导致上传脚本路径乱码，部署上传前建议复制到 `C:\tmp\cpa-prod-deploy`。
- 覆盖后端二进制后必须 `docker compose restart cpa-claude-proxy`，只 `docker compose up -d` 不一定生效。
- `go test ./...` 可能存在与当前任务无关的既有失败。若使用局部测试作为验证，必须在最终说明中明确测试范围。
- Claude token usage 是高风险区。不要把 `cache_creation_input_tokens`、`cache_read_input_tokens`、`cached_tokens` 或 OpenAI 兼容层的 `cached_tokens`/`cached_creation_tokens` 清零作为“扣除代理注入 token”的手段；这些字段是 cctest 和真实计费审计判断缓存命中的依据。

## 接手检查清单

1. 读本文件。
2. 根据任务类型读专题文档：
   - 部署：读生产部署文档。
   - 改文件或找入口：读项目文件地图。
   - 改前端账号池：读前端账号池维护地图。
   - 改 Claude Code 伪装：读 Claude Code 伪装文档。
3. 执行 `git status -sb` 和 `git log --oneline -5`，确认当前分支与最近提交。
4. 如果涉及生产，先确认服务器 `DEPLOYED_COMMITS`，并先备份账号数据。
5. 如果涉及账号状态分类，至少覆盖列表、一键探测、管理 API 调用、正常用户请求四条路径。
6. 如果涉及 Claude token usage、cache_control、system prompt 或 OpenAI/Responses 翻译层，先读 `docs/claude-code-mimicry.md` 的 `5.2 thinking/signature 与 token usage`，并跑其中列出的 cache breakdown 回归测试。
7. 提交前只暂存本次真实改动文件，不把行尾噪声一起提交。
