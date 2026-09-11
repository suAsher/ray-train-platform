---
name: release
description: ray-train-platform 的开发、构建、四端同步、部署、验收与排障。兼容 Claude 入口，执行前读取项目的唯一权威 release skill。
---

# Release 兼容入口

本入口不再维护第二份流程。执行任务前，完整读取：

[权威 release skill](../../../.agents/skills/release/SKILL.md)

再按其路由读取 references 中与当前任务有关的文件。所有路径以该权威文件所在目录解析。

不要使用本文件历史版本中的本地编译测试、仅推 GitHub 或同时构建旧前端的命令；当前前后端已分仓，构建测试在构建机，Portal 走其独立 GitLab CI/CD。

若权威文件缺失，停止发布并说明缺失文件，不使用过期流程代替。
