# Mac 本地开发与接手指南

本文档用于在新 Mac 上继续 CPA / CLIProxyAPI 二开项目。目标是让新机器通过 GitHub、公开文档和可复现命令接手项目，而不是从旧 Windows 机器整盘复制。

本文档不记录任何 SSH 密码、管理密码、API Key、Claude token、账号 JSON、refresh token、session key 或代理密码。

## 目录布局

建议在 Mac 上使用纯 ASCII 路径，避免后续构建、上传或脚本处理中文路径时出问题：

```bash
mkdir -p ~/Code/cpa
cd ~/Code/cpa

git clone -b xiaoyu/claude-oauth-cookie-mimicry https://github.com/shangwantsci/CLIProxyAPI.git
git clone -b xiaoyu/claude-oauth-cookie-mimicry https://github.com/shangwantsci/Cli-Proxy-API-Management-Center.git
```

建议目录：

```text
~/Code/cpa/CLIProxyAPI
~/Code/cpa/Cli-Proxy-API-Management-Center
```

旧 Windows 路径只作为历史参考：

```text
F:\claude反代\CLIProxyAPI
F:\claude反代\Cli-Proxy-API-Management-Center
```

## 必备工具

建议先安装 Homebrew，然后安装基础工具：

```bash
brew install git ripgrep node
```

后端 `go.mod` 当前要求 Go `1.26.0`。如果 Homebrew 已提供 Go 1.26 或更新版本，可以：

```bash
brew install go
go version
```

如果 Homebrew 版本落后，使用官方安装包或版本管理器安装 Go 1.26.x，并确认：

```bash
go version
```

## 首次接手必读

在 Mac 上新开 Claude 或 Codex 窗口时，先读：

```text
CLIProxyAPI/AGENTS.md
CLIProxyAPI/docs/codex-handoff.md
CLIProxyAPI/docs/project-file-map.md
CLIProxyAPI/docs/production-deployment-23.153.36.12.md
CLIProxyAPI/docs/claude-code-mimicry.md
CLIProxyAPI/docs/agent-start-prompt.md
Cli-Proxy-API-Management-Center/docs/claude-account-pool-maintenance.md
```

如果任务涉及生产，以服务器文件为最终事实源：

```bash
ssh -p 41629 root@23.153.36.248 'cat /opt/cpa-claude-proxy/DEPLOYED_COMMITS'
```

不要把 SSH 密码写进文档、提交、脚本或聊天最终回复。建议在 Mac 上配置 SSH key 后再部署：

```bash
ssh-keygen -t ed25519 -C "xiaoyu-cpa-mac"
```

把公钥加入服务器 `root` 用户的 `~/.ssh/authorized_keys`。私钥留在 Mac 本机或密码管理器中。

## 后端常用命令

进入后端仓库：

```bash
cd ~/Code/cpa/CLIProxyAPI
```

查看状态：

```bash
git status -sb
git log --oneline -5
```

运行局部测试：

```bash
go test -count=1 ./internal/api/handlers/management ./sdk/cliproxy/auth
```

Claude Code 伪装、cache_control 或 token usage 相关改动必须先读 `docs/claude-code-mimicry.md`，并按 `CLIProxyAPI/AGENTS.md` 中列出的 cache breakdown 回归测试执行。

编译检查：

```bash
go build -o .codex_tmp/cli-proxy-api-test ./cmd/server
rm -f .codex_tmp/cli-proxy-api-test
```

Linux 生产二进制构建：

```bash
mkdir -p ../.codex_tmp/prod-deploy
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o ../.codex_tmp/prod-deploy/CLIProxyAPI-linux-amd64 ./cmd/server
shasum -a 256 ../.codex_tmp/prod-deploy/CLIProxyAPI-linux-amd64
```

## 前端常用命令

进入前端仓库：

```bash
cd ~/Code/cpa/Cli-Proxy-API-Management-Center
```

安装依赖：

```bash
npm install
```

检查与构建：

```bash
npm run type-check
npm run lint
npm run build
rg -n -i "claude|anthropic" dist
```

`npm run build` 会执行 `scripts/sanitize-management-html.mjs`。`rg` 对 `dist` 必须无输出，才能把前端产物上传到生产。

生产前端产物对应关系：

```text
dist/index.html -> /opt/cpa-claude-proxy/static/management.html
```

## 生产部署提醒

部署前读 `CLIProxyAPI/docs/production-deployment-23.153.36.12.md`。当前生产主机虽然文档名保留旧 IP，但实际服务器是：

```text
root@23.153.36.248:41629
```

标准原则：

- 不在服务器上 `git pull`。
- 不在服务器上临时改代码。
- 服务器只接收本地构建好的后端二进制和前端 `management.html`。
- 覆盖后端二进制前备份 `/opt/cpa-claude-proxy/auths`。
- 覆盖后端二进制后必须重启 `cpa-claude-proxy` 容器。
- 写入 `/opt/cpa-claude-proxy/DEPLOYED_COMMITS` 时使用实际部署的后端/前端提交号。

## 不要迁移的内容

从旧 Windows 机器迁移时，不要复制这些内容到 Mac 仓库：

- `node_modules`
- `dist`
- Go build cache
- `.codex_tmp`
- 生产账号数据
- SSH 密码、API key、token、refresh token、session key、代理密码
- 旧工作区里的无关 `M` 或行尾噪声

代码和公开文档通过 GitHub 同步；生产账号数据留在服务器并按部署文档备份；秘密信息放在密码管理器或 SSH key 中。
