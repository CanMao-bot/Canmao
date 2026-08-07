# 罐头喵 (Canmao) 🐱

> 一个住在 QQ 里的罐头型智能助手喵～装得下工具、会记性、有心情、会长记性，
> 还能喊 opencode 帮你写代码！

基于 **Go** 的 QQ 机器人智能框架，通过 **OneBot(WebSocket)** 连接 **NapCat**，
内建完整的 AI Agent 能力，开箱即用。

> 注：早期叫 `gobot`，模块名/构建产物仍沿用 `gobot`，显示名是「罐头喵」。
>
> 🌐 **官网与文档**：<https://canmao-bot.github.io/Canmao-site/>

---

## ✨ 特性

- **AI Agent**：支持多模型、多提供商动态切换，工具调用自动循环
- **记忆 🧠**：向量检索的长短期记忆，越聊越懂你
- **心情 🌤**：会根据氛围主动搭话，不再是一台冷冰冰的机器
- **人设 🎭**：有性格有语气，还能自我学习进化
- **识图 👀 / 画图 🎨 / 语音 🎙**：qwen 系列 + DashScope，生成图片、转述图片、朗读声音
- **Web 🕸**：联网搜索、爬取网页、Playwright 浏览器自动化
- **定时任务 ⏰**：告诉它「提醒我……」，到点自动发
- **调用 opencode ⚡**：AI 直接 `opencode_task` 让编程智能体帮你写代码、跑测试、排查问题
- **插件 🔌 / Skill 📚**：`.so` 插件 + Claude Code 风格技能扩展
- **文件 📁**：自动保存收到的图片文件，可检索上传
- **调度/审批**：高危操作(跑命令、调 opencode)自动走人工审批或白名单

## 🚀 快速开始

前置：NapCat 已运行，OneBot 正向 WS 端口 `3001`。

```bash
git clone <repo> canmao
cd canmao

# 配置
cp config.yaml config.yaml  # 修改 onebot.ws_url / token / self_id 与 ai.* 模型
# 国内构建
export GOPROXY=https://goproxy.cn,direct
go build -o canmao .
./canmao
```

以 systemd 运行（推荐，路径请按实际修改）：

```ini
[Service]
Type=simple
User=root
WorkingDirectory=/path/to/Canmao
Environment=PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
ExecStart=/path/to/Canmao/Canmao
Restart=on-failure
```

```bash
# 使用示例配置
cp config.example.yaml config.yaml
# 编辑 config.yaml 填入你的 QQ 号 / token / API Key
go build -o Canmao .
systemctl daemon-reload && systemctl start gobot
```

## ⚙️ 配置

核心配置在 `config.yaml`（详见文件内注释）：

| 节 | 作用 |
|----|------|
| `bot` | 显示名 / 主人 QQ / 合并转发阈值 |
| `onebot` | WS 地址 / token / self_id |
| `ai` | 初始默认模型提供商（也可运行时用 `/provider` 动态换） |
| `memory` / `mood` / `persona` | 记忆 / 心情 / 人设开关 |
| `web` | 搜索 / 爬取 / 浏览器（代理指向 mihomo） |
| `media` | 图像生成 / TTS |
| `acp` | opencode 调用（二进制需绝对路径） |
| `sched` / `mcp` / `skills` / `plugin` | 调度 / MCP / 技能 / 插件 |

## 📁 目录结构

```
adapter/   OneBot WebSocket 客户端
core/      Event 分发、Sender、合并转发、配置
services/  ai / acp / dashscope / file / mcp / memory /
           plugin / sched / shell / skill / web
store/     SQLite 持久化(perm/session/memory/mood/…)
examples/  .so 插件示例(echo/groupadmin)
skills/    Claude Code 风格技能
config.yaml  主配置
```

## 🧪 测试

```bash
go test ./...
```

## 🛠 开发约定（踏坑）

- **API Key 一律走 `/provider` + config 动态管理，绝不要写死进代码。**
- opencode 需用**绝对路径**二进制，且 systemd 已注入 node PATH。
- 高危工具(`run_command`、`opencode_task`)自动进审批或 allow 白名单。
- 长回复(>200 字)自动合并转发，避免霸屏。

## 🔧 服务器环境

在中国大陆等外网受限的网络中，可配置 HTTP 代理（如 mihomo `127.0.0.1:7890`）；Go 依赖走 goproxy.cn 等国内镜像可加速。

---

Made with 🐱 by 罐头喵.