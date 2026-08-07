---
name: files
description: 文件与图片管理, 查看已保存文件、上传文件到群、获取群文件列表
---

当用户涉及文件/图片管理时使用本技能。

## 能力
- 收到的图片和文件会自动保存到 bot 本地, 不需要额外操作
- /files 查看已保存文件列表
- /sendfile <文件名> 将已保存文件发送到当前群/私聊

## AI 工具
- list_files: 列出 bot 已保存的文件
- upload_file_to_group: 将已保存文件上传到指定群
- get_group_files: 获取群根目录文件列表

## 使用场景
- 用户说"把刚才的图片发到XX群" → 用 list_files 找到文件, 用 upload_file_to_group 上传
- 用户说"保存了哪些文件" → list_files
- 用户说"XX群有什么文件" → get_group_files
