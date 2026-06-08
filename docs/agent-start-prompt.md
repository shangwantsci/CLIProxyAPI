# Claude / Codex 接手提示词

把下面提示词复制到 Mac 上的新 Claude 或 Codex 窗口。它用于让新窗口先读正确文档、建立项目边界，并避免泄露密钥或误改生产敏感路径。

```text
请使用简体中文回复，并称呼我为“晓宇”。

你现在接手 CPA / CLIProxyAPI 二开项目。我会同时使用 Claude 和 Codex。请先阅读这些文档，再回答项目结构、代码修改或部署问题：

- CLIProxyAPI/AGENTS.md
- CLIProxyAPI/docs/codex-handoff.md
- CLIProxyAPI/docs/project-file-map.md
- CLIProxyAPI/docs/production-deployment-23.153.36.12.md
- CLIProxyAPI/docs/claude-code-mimicry.md
- CLIProxyAPI/docs/local-development-macos.md
- Cli-Proxy-API-Management-Center/docs/claude-account-pool-maintenance.md

如果任务涉及前端账号池、导入账号、代理池、策略设置、额度显示、账号详情抽屉或批量操作，优先检查前端仓库：

- Cli-Proxy-API-Management-Center/src/pages/DashboardPage.tsx
- Cli-Proxy-API-Management-Center/src/pages/DashboardPage.module.scss
- Cli-Proxy-API-Management-Center/src/services/api/authFiles.ts
- Cli-Proxy-API-Management-Center/src/services/api/claudeSessionImport.ts

如果任务涉及管理 API、账号健康、探测任务、认证导入、Claude 正常请求路由、token usage、cache_control 或 Claude Code 伪装，优先检查后端仓库：

- CLIProxyAPI/internal/api/handlers/management
- CLIProxyAPI/internal/auth/claude
- CLIProxyAPI/internal/runtime/executor
- CLIProxyAPI/internal/runtime/executor/helps
- CLIProxyAPI/sdk/auth
- CLIProxyAPI/sdk/cliproxy/auth

当前生产事实源：

- 服务器：root@23.153.36.248:41629
- API 入口：https://api.openstaryu.com
- 管理入口：https://admin.openstaryu.com/management.html
- 生产部署文档：CLIProxyAPI/docs/production-deployment-23.153.36.12.md
- 生产实际部署提交以服务器 /opt/cpa-claude-proxy/DEPLOYED_COMMITS 为准

重要边界：

- 不要把 SSH 密码、管理密码、API key、Claude token、refresh token、session key、账号文件或代理密码写入文档、提交或最终回复。
- 不要在服务器上 git pull、临时改代码或现场编译；生产只运行本地构建后上传的产物。
- 前端生产 dist 不能出现 claude / anthropic 明文字样；部署前必须运行 npm run build，并确认 rg -n -i "claude|anthropic" dist 无输出。
- 后端仓库可能存在任务前已有无关 M 或行尾噪声；不要 revert，不要误提交。提交时只暂存本次真实改动。
- 不要破坏 Claude token/cache 计费透传，尤其 cache_creation_input_tokens、cache_read_input_tokens、cached_tokens、cached_creation_tokens。
- 修改 OpenAI/Responses 转 Claude、cache_control、usage、Claude Code mimicry、system prompt 或 CCH 签名相关逻辑前，先读 CLIProxyAPI/AGENTS.md 和 CLIProxyAPI/docs/claude-code-mimicry.md，并运行对应回归测试。
- 涉及生产部署前，必须先按部署文档备份 /opt/cpa-claude-proxy/auths。

接手后先执行：

1. git status -sb
2. git log --oneline -5
3. 根据任务类型阅读对应专题文档
4. 如果涉及生产，读取服务器 /opt/cpa-claude-proxy/DEPLOYED_COMMITS 校准实际部署版本
```
