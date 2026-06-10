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

- 后端提交：`fa83c466`（保住客户端 1h 缓存 TTL + 放开 1M 上下文：`normalizeCacheControlTTL` 改为「有任意客户端显式 1h 块则把所有 ephemeral 块统一升级为 1h」，不再把客户的 1h 静默降级成 5m；`context-1m-2025-08-07` 从丢弃表移除并加入 beta 允许白名单，客户端显式请求时透传上游且不被 mimicry guard 拦截）
- 前端提交：`c0ef735`（未变；本轮仅后端部署）
- 最近一次按本文档部署时间：`2026-06-08T04:38:25+00:00`
- 本次部署后端二进制 sha256：`67461f9370c3c0176f6358e3ae59161db53ebf7c5f28a96c41c63d856f97d6ac`
- 部署前运行版本 backend=`71b68992`，binary sha256 `f07396238658f40fc2af74b6d8d6eac05cdcc40f388eec6bdfe7b6cb0bcfe9bf`
- 部署前后端二进制备份：`/opt/cpa-claude-proxy-backups/CLIProxyAPI-before-deploy-20260608-043614.bak`（可回滚到 `71b68992`）
- 部署前账号备份（exclude logs）：`/opt/cpa-claude-proxy-backups/auths-20260608-043614.tgz`
- 部署后账号数：1195 个 json（账号增删由晓宇手动管理）

### 本轮变更说明（fa83c466）

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
Copy-Item -LiteralPath 'F:\claude反代\.codex_tmp\prod-deploy\CLIProxyAPI-linux-amd64' -Destination (Join-Path $stage 'CLIProxyAPI-linux-amd64') -Force
Copy-Item -LiteralPath 'F:\claude反代\Cli-Proxy-API-Management-Center\dist\index.html' -Destination (Join-Path $stage 'management.html') -Force
```

如果本次只更新后端或只更新前端，只复制对应产物。

### 5. 上传到服务器临时目录

```bash
mkdir -p /tmp/cpa-claude-stage
```

上传文件：

- 后端：`/tmp/cpa-claude-stage/CLIProxyAPI`
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
install -m 0755 /tmp/cpa-claude-stage/CLIProxyAPI /opt/cpa-claude-proxy/runtime/CLIProxyAPI
install -m 0644 /tmp/cpa-claude-stage/management.html /opt/cpa-claude-proxy/static/management.html
cd /opt/cpa-claude-proxy
docker compose up -d
docker compose restart cpa-claude-proxy
```

注意：只执行 `docker compose up -d` 可能不会重启已经运行的容器；覆盖二进制后必须显式重启或使用 `docker compose up -d --force-recreate`，否则新产物可能不会生效。

只更新后端时，不要覆盖 `management.html`：

```bash
install -m 0755 /tmp/cpa-claude-stage/CLIProxyAPI /opt/cpa-claude-proxy/runtime/CLIProxyAPI
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
