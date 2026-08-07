---
name: group-admin
description: 群管理员能力, 禁言/踢人/全体禁言/发布公告/设置群名片/群名/管理员等
---

当用户需要执行群管理操作时使用本技能。

## 命令 (仅群管理员或主人在群内可用)

- /ban <QQ> [分钟] — 禁言成员(默认10分钟)
- /unban <QQ> — 解除禁言
- /mute — 开启全体禁言
- /unmute — 解除全体禁言
- /kick <QQ> — 移出成员
- /notice <内容> — 发布群公告
- /setcard <QQ> <名片> — 设置群名片
- /setname <群名> — 修改群名
- /setadmin <QQ> [off] — 设置/取消群管理员
- /memberlist — 查看群成员列表

## AI 工具

- group_ban / group_whole_ban / group_kick — 禁言/全体禁言/踢人 (极危, 仅主人可批准)
- group_notice / group_set_card / group_set_name — 公告/名片/群名 (高风险)
- group_member_list — 群成员列表 (低风险)

## 权限说明
- 只有群管理员或主人才可执行群管理命令
- AI 调用高危群管理工具时需主人审批
