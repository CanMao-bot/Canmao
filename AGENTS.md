# 罐头喵 (Canmao) — QQ bot 智能框架

基于 Go 的 QQ 机器人框架(原名 gobot，模块名仍为 `gobot`)，通过 OneBot(WebSocket) 连接 NapCat，内建 AI Agent(工具调用)、记忆、心情、人设、Web/浏览器、图像生成、TTS、调度、插件、Skill 与 opencode 调用(ACP) 等能力。

## 构建与运行

- Go 1.25.5；模块名 `gobot`；main 入口 `main.go`。
- 构建：`export GOPROXY=https://goproxy.cn,direct && go build -o Canmao .`
- 测试：`go test ./...`
- 运行：systemd 服务 `gobot.service`。改代码后需 `go build` 再 `systemctl restart gobot`。
- systemd 服务路径 `/etc/systemd/system/gobot.service`。已注入 node PATH 供 opencode 使用。
- 依赖：`gorilla/websocket`、`mark3labs/mcp-go`、`yaml.v3`、`modernc.org/sqlite`(纯 Go，无需 CGO)。

## 目录结构

- `adapter/` — OneBot WebSocket 客户端（正向 WS、文件/群管理 API、合并转发）。
- `core/` — 核心：`bot.go`(Event 分发、Sender、Reply 合并转发)、`config.go`(全部配置)、`event.go`。
- `services/` — 各能力服务（main.go 中逐块装配）：
  - `ai/` — AI Agent 核心(service.go runAgent 循环)、`client.go`(LLM)、`models.go`(多 provider 注册表)、工具、`approval.go`(审批)、`ask.go`(ask_user)、`mood.go`、`persona.go`、`context.go`(上下文压缩)、`vision.go`(识图)、`help.go`、`commands.go`、`acp_tool.go`(opencode_task)。
  - `acp/` — ACP 客户端，spawn `opencode acp` 子进程走 stdio JSON-RPC。
  - `dashscope/` — 图像生成(qwen-image-2.0) + TTS(qwen3-tts-flash)。
  - `file/` — 文件保存/上传 + read_file/write_file/list_directory 工具。
  - `groupreq/` — 入群申请管理（bot 是群管理时群内发申请卡片，`/同意 N` `/拒绝 N [理由]` `/入群申请` 审批）。
  - `mcp/` — MCP 客户端（stdio 传输，mcp-go）。
  - `memory/` — 向量记忆（embedding 检索）。
  - `plugin/` + `pluginapi/` — .so 插件加载。示例在 `examples/`。
  - `sched/` — 定时任务。
  - `shell/` — `run_command` 系统命令工具。
  - `skill/` + `skills/` — Claude Code 风格 SKILL.md 技能。
  - `web/` — 搜索(DuckDuckGo)/爬取(readability)/浏览器(Playwright `scripts/browser.py`)。
- `store/` — SQLite 持久化：`perm`、`session`、`memory`、`mood`、`persona`、`provider`、`group`、`sched`、`allow`。
- `config.yaml` — 主配置；`data/` — 运行时数据。

## 配置要点（config.example.yaml）

- `bot.master_id` 最高权限；`onebot.ws_url` / `token` / `self_id` 连 NapCat WS。本地部署请复制为 `config.yaml` 并填入真实值，**config.yaml 不提交**。
- `ai.*` 为初始默认 provider；运行期可用 `/provider` 命令动态增删/切换（存 `data/providers.json`）。
- `memory`、`mood`、`persona`、`web`、`sched`、`media`、`acp`、`mcp`、`skills`、`plugin` 均为对应功能开关。
- `web.proxy` 指向 HTTP/SOCKS5 代理（外网受限环境访问国外网站需要，个人环境自行配置）。

## 关键约定与踏坑

- **API Key 都可通过 `/provider` 与 config 增删改**：千万别写死在代码里。所有外部模型(LLM/embedding/图像/TTS)走 OpenAI 兼容 `/compatible-mode/v1` 接口。
- **工具注册**：AI 工具用 `NewTool(name, desc, params, required, handler)` 创建，`AddTool()` 注入；`RiskHigh` 工具(如 `run_command`、`opencode_task`)需人工审批或进 allow 白名单。
- **opencode 必须用绝对路径**：systemd root 环境无 nvm PATH；`acp.binary` 指向 `opencode.exe` 完整路径，且服务已注入 node PATH。
- **新增服务**：在 `main.go` 装配 + `core/config.go` 加配置节 + 相应 `services/*` 包。改完务必 `go build` 验证。
- **合并转发**：回复超 `long_message_threshold`(200) 自动合并转发。
- **Session 模式**：`session_mode: merged` 群内全群共享上下文；`separate` 按用户。
- 测试文件与源码同目录（`*.test.go`）。

## 开源提交/推送规范（重要）

本项目用于**开源**，任何改动都必须是干净的、可公开的：

1. **绝不 commit/push 敏感信息**：API Key、token、QQ 号、密码、代理地址一律不进仓库，也不进 import、硬编码、字符串、注释或 agent 记忆。用环境变量、运行时配置或占位符（`YOUR_*`）。
2. **config.yaml 始终不提交**：只提交 `config.example.yaml`（占位符版）。本地配置用 `.gitignore` 忽略。
3. **不提交运行时数据**：`data/`（数据库/记忆/会话/文件）不进仓库。
4. push 前自查：`git diff --check`、扫描 `git grep -niE 'sk-|token|api_key|master_id|self_id|password|secret'`（去除合法占位符后必须为空）。
5. 本文件、README 中的路径一律用通用占位（`/path/to/Canmao`），不用本机绝对路径。
6. 若发现历史提交已含密钥：用 `git filter-repo`/`git rebase` 清除并轮换密钥。

## 服务器（运行环境）

> 本文件为开源副本，服务器细节如需保留请放入未提交的个人笔记（如 `.local.NOTES`），勿写入仓库。
- Go 模块走 goproxy.cn；如外网受限可用代理（个人环境自行配置）。