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
- **QQ 原生能力**：Agent 可搜索并调用 NapCat 原生接口，覆盖消息、好友、群管、文件、资料、相册和空间；按具体接口校验身份与审批，详见[QQ 接口说明](docs/qq-agent.md)
- **菜单与群娱**：无需模型即可签到、骰子、转账、银行、排行、种田和钓鱼；六级社区权限、分群配置、私聊远程管理，详见[社区功能说明](docs/community.md)
- **记忆 🧠**：向量检索的长短期记忆，越聊越懂你
- **心情 🌤**：会根据氛围主动搭话，心情低落时会主动安慰；极差时会对辱骂者自主禁言/改昵称（限时冷却，且绝不误伤主人/管理员）
- **人设 🎭**：有性格有语气，还能自我学习进化
- **识图 👀 / 画图 🎨 / 语音 🎙**：qwen 系列 + DashScope，生成图片、转述图片、朗读声音
- **Web 🕸**：联网搜索、爬取网页、Playwright 浏览器自动化
- **定时任务 ⏰**：告诉它「提醒我……」，到点自动发
- **调用 opencode ⚡**：AI 直接 `opencode_task` 让编程智能体帮你写代码、跑测试、排查问题
- **插件 🔌 / Skill 📚**：`.so` 插件 + Claude Code 风格技能扩展
- **文件 📁**：自动保存收到的图片文件，可检索上传
- **调度/审批**：Agent 高危操作(跑命令、调 opencode、群管理工具)走人工审批或白名单，提示词注入无法绕过；管理员直接下达固定群管命令经权限校验后执行

## 🚀 快速开始

前置：NapCat 已运行，OneBot 正向 WS 端口 `3001`。

```bash
git clone <repo> canmao
cd canmao

# 配置
cp config.example.yaml config.yaml  # 修改 onebot.ws_url / token / self_id 与 ai.* 模型
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
| `community` | 菜单 / 娱乐 / 六级权限 / 分群 / 群管与私聊管理 |

群 AI 与 bot/群娱开关独立，只能由配置中的主人在目标群 `/ai on`、`/ai off`。
先在私聊用 `/provider`、`/model` 配好模型；`/ai settings` 查看本群设置，
`/ai mention on|off` 和 `/ai probability <0到1>` 设置本群触发方式。
普通管理员、社区超管与 QQ 群主均不能修改群 AI 设置，详见[社区功能说明](docs/community.md)。

支持识图的模型默认通过 `ai.image_mode: native` 直接接收原图，包括当前消息和引用消息中的图片，无需额外配置识图模型。旧 `memory.vision_model` 在原图模式下不参与请求。

使用纯文本对话模型时，可显式设置 `ai.image_mode: caption`，并在 `memory.vision_model` 填写当前提供商可用的识图模型，将图片先转述为文字。转述失败会提示错误，不会将图片悄悄丢弃。会话记录保存图片数量和对话文本，原图只在当前回合（含工具调用后的续答）传给模型。

历史消息时间通过独立的系统索引传给模型。模型回复会清除旧版内部时间标记，保留正常日期、正文与 @；同样适用于旧回复缓存和历史上下文。兼容接口返回的分段文本会合并为正文，没有正文或工具调用的响应会明确报错。

## 审批、会话与文件范围

安全审核仅负责 Agent 的工具操作。管理员直接下达固定群管命令、私聊管理命令及管理员配置的关键词规则，经权限校验后执行，无需二次批准；通过自然语言让 Agent 操作时仍按工具风险审核，发起人的管理员身份不会自动跳过审批。

Agent 工具执行前会校验 JSON Schema、真实消息身份和资源归属。低风险工具直接执行；中风险操作由发起人或主人确认；高风险操作只能由 `bot.master_id` 指定的主人或 `bot.admin_ids` 中的管理员批准；极危操作仅主人可批准。`run_command`、`opencode_task` 和群管理工具在执行入口具有高风险下限，`write_file` 只允许主人发起并按极危操作审批。心情自主惩罚仍只针对真实消息记录的辱骂者，并保留主人和管理员保护。

每个审批请求绑定机器人发出的真实消息。引用对应的申请消息，并回复：

- `同意`：只批准引用的这次操作。
- `拒绝`：拒绝引用的这次操作；发起人也能取消自己的请求。
- `以后都允许`：将引用工具的免审批权限授予发起人，范围限于当前群及该用户，或该用户的私聊。

无需输入编号。未引用、引用其他消息、过期或重复引用都不会批准操作；多个申请由引用消息分别定位。群内审批必须在原群完成；其他用户私聊发起的高风险审批会转发给主人，主人引用自己收到的申请确认。真实回复人的身份仍须满足风险等级，永久授权记录批准人和风险等级，批准人失去权限后不再生效。没有消息回执的适配器无法发起审批。

长期记忆按真实发言者和当前群/私聊隔离，即使群会话为 merged 也不会丢失事实来源。仅从当前用户明确表达的姓名、居住地、偏好和学习目标自动提取，不保存普通闲聊、引用内容或 AI 猜测。同键事实更新、重复事实去重；`remember`、`list_memories`、`forget_memory` 可通过自然语言请求保存、核对或遗忘。没有 embedding 配置或服务暂不可用时使用本地中文检索，配置可用时融合向量相似度、关键词与更新时间，并限制候选和注入长度。详见[记忆说明](docs/memory.md)。

会话切换、删除和定时任务取消都检查当前群及用户归属。`session_mode: merged` 只在本群内共享；同一会话的对话和管理命令依次执行，审批与 `ask_user` 回答先行处理，可唤醒等待中的任务。`/clear` 同时清除消息窗口和摘要。压缩以所选模型的窗口预算为准，计入工具定义，并原子替换摘要与历史窗口。

普通用户只能读取、列出或发送本会话的文件：私聊使用 `data/files/private_<用户ID>/`，群聊使用 `data/files/group_<群ID>/`。相对文件名以该目录为基准，路径穿越及越界符号链接会被拒绝。主人保留服务器文件访问能力。收到的文件、NapCat 本地缓存副本和生成的图片、语音使用相同的范围与索引，同名文件另存，不覆盖已有文件。

Agent 会在会话数据库中保存 Run/Step 执行记录，包含工具参数、审批等待、开始执行、结果及最终状态。后续模型请求失败时仍保留已发生的操作记录；同一 OneBot 消息和同一轮中的工具调用 ID 会去重。进程重启后，未完成任务标记为 `interrupted`，不会自动重放结果不确定的操作；重试前应核对记录。不同消息提出的相似操作仍可能是新的任务，不保证外部系统的业务幂等性。

升级已有部署时，会话数据库自动补齐版本字段和执行记录表。旧版免审批记录缺少可信批准人，升级后需重新授权；旧版平铺在 `data/files/` 下且没有来源记录的文件保留，仅主人可见。需要恢复普通用户访问时，由主人将文件移入对应会话目录并重启以重新扫描。升级前建议备份数据目录。

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
go vet ./...
go build -o Canmao .
```

`services/shell` 依赖 `/bin/bash`，本机没有该可执行文件时跳过命令执行测试；其余测试使用临时数据库、本地 HTTP 或管道模拟，不要求真实 QQ、模型服务或 opencode。部署到 Linux 后仍需验证实际 NapCat、模型提供商及 ACP 连接。

## 🛠 开发约定（踏坑）

- **API Key 一律走 `/provider` + config 动态管理，绝不要写死进代码。**
- opencode 需用**绝对路径**二进制，且 systemd 已注入 node PATH。
- 高危工具(`run_command`、`opencode_task`)自动进审批或 allow 白名单。
- 长回复(>200 字)自动合并转发，避免霸屏。

## 🔧 服务器环境

在中国大陆等外网受限的网络中，可配置 HTTP 代理（如 mihomo `127.0.0.1:7890`）；Go 依赖走 goproxy.cn 等国内镜像可加速。

---

Made with 🐱 by 罐头喵.
