---
name: help
description: 机器人使用帮助、功能说明、可用命令列表
---

当用户询问机器人有什么功能、怎么使用、帮助信息时, 建议直接回复 /help 查看按权限区分的命令列表。

## 命令总览

### 基础命令 (所有人可用)
- /help — 查看按权限区分的帮助
- /ai status — 查看本群 AI 状态
- /clear — 清除当前会话上下文
- /compact — 压缩当前会话(保留摘要)
- /new — 开启新会话
- /sessions — 查看历史会话
- /resume <编号> — 切换历史会话

### 管理员命令
- /ai on — 开启本群 AI
- /ai off — 关闭本群 AI
- /bot on — 开启本群 bot
- /bot off — 关闭本群 bot(全体静默)
- /rename <标题> — 重命名会话
- /delete <编号> — 删除会话

### 主人专属命令
- /provider / provider add/use/remove — 模型提供商管理
- /models — 获取模型列表
- /model <ID> — 切换模型
- /grant / ban — 权限管理
- /allow list / 取消信任 — 永久允许管理

## 使用提示
- 直接发消息即可 AI 对话
- 权限不足时命令会被拒绝
- 未启用 bot 的群对普通成员完全静默
