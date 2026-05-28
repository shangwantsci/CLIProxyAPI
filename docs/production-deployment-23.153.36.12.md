# CPA Claude Proxy 生产部署记录：23.153.36.12

本文档固定新生产服务器上的部署位置、隔离边界、更新流程和排障入口。文档不记录任何 SSH 密码、管理密码、API Key、Claude token 或代理密码。

## 服务器现状

- 主机：`23.153.36.12`
- SSH：`root@23.153.36.12:41629`
- 系统：Ubuntu，Docker 已安装
- CPA 对外端口：`8318`
- CPA 访问入口：`http://23.153.36.12:8318/management.html`
- CPA 健康检查：`http://23.153.36.12:8318/healthz`

部署前已确认存在的生产项目：

- `/opt/new-api-production`
- `/opt/sub2api-production`
- Coolify 相关容器
- `new-api` 监听 `127.0.0.1:13000`
- `sub2api` 监听 `127.0.0.1:18080`
- `80/443/8000/8080/6001/6002` 已由 Coolify/Traefik 等服务使用

CPA 使用独立目录、独立容器、独立网络和独立端口，不修改上述项目。

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
- Docker 网络：`cpa-claude-net`
- 镜像：`debian:12-slim`
- 端口映射：`0.0.0.0:8318->8318/tcp`
- 容器环境：
  - `MANAGEMENT_STATIC_PATH=/CLIProxyAPI/static`
  - `SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt`

## 当前部署版本

- 后端提交：`7cf6544a`
- 前端提交：`00956ca`
- 最近一次按本文档部署时间：`2026-05-28T08:11:50+00:00`
- 最近一次账号备份：`/opt/cpa-claude-proxy-backups/auths-20260528-081142.tgz`
- 最近一次后端二进制备份：`/opt/cpa-claude-proxy-backups/CLIProxyAPI-before-7cf6544a-20260528-081142.bak`
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
```

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
curl.exe -sS -o NUL -w "healthz %{http_code}\n" --max-time 10 http://23.153.36.12:8318/healthz
curl.exe -sS -o NUL -w "management %{http_code}\n" --max-time 10 http://23.153.36.12:8318/management.html
```

预期：

- `cpa-claude-proxy` 容器为 `Up`
- `8318` 端口由 `docker-proxy` 监听
- `healthz 200`
- `management 200`
- `auths` 目录存在 Claude 账号 JSON 和 `proxy_pool.json`

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
- 更新时只操作 `/opt/cpa-claude-proxy` 和 `/opt/cpa-claude-proxy-backups`。
- 不要改 `/opt/new-api-production`、`/opt/sub2api-production`、Coolify 目录或相关容器。
- 账号、代理、管理密钥和客户端 API Key 由旧服务器配置/数据迁移而来，后续应通过管理面板维护。
- 前端源码仓库是 `F:\claude反代\Cli-Proxy-API-Management-Center`，不是后端仓库内的 `static` 目录。
- 后端源码仓库是 `F:\claude反代\CLIProxyAPI`，生产服务器不要保存 GitHub 凭据。
