# MLflow 集成接入与受控产物实施计划

**目标：** 完成已批准的独立身份 → 外部实验 → 产物上传 → 平台下载路径，并保持旧训练与个人数据兼容。

**架构：** 独立机器凭据和资源 grant；MLflow 原服务外加授权适配器；产物以数据库状态与不可变 TOS 分片管理。Portal 只使用受控 API。

**技术：** Go / Gin / GORM / PostgreSQL / TOS、Vue / Element Plus、构建机 Docker 测试。

- [x] 核对 release skill、历史快照、四端源码、Portal dev、线上版本和副本健康。
- [x] 完成身份授权接缝独立审阅，确定 wrapper 方案，记录设计及影响。
- [ ] 身份模块：新增 `backend/integrations/`、auth 集成 guard、repositories/api 集成模块和迁移 47；先写 scope、路由与授权测试，构建机记录 RED 后实现。
- [ ] 产物模块：新增 `backend/trackingartifacts/`、repository、TOS adapter 和迁移 48；先写摘要、并发、预算与恢复测试，构建机记录 RED 后实现。
- [ ] 根代理接线：tracking actor 加机器标识、授权 wrapper、签名游标、artifact HTTP handler、主程序与 capabilities；每次身份授权失败不得调用上游或对象存储。
- [ ] Portal：集成接入子页、令牌一次性显示、grant 管理、外部 Run 产物展示及下载；Node 合同 RED 后实现，构建机完整 lint/build/Playwright 验证。
- [ ] 在构建机精确候选运行全部 Go 测试、真实 PostgreSQL 新装/重复/升级/并发测试、真实 MLflow/TOS 适配联调；独立安全审阅并修复发现。
- [ ] 更新 OpenAPI、外部接口交付单、使用说明及验收脚本，明确上传容量、时限、幂等、权限、重试和原生共享入口边界。
- [ ] 再核对远端，候选验证通过才同步后端四端与 Portal dev，禁止覆盖他人提交。
- [ ] 备份、隔离恢复验证、最小 Helm diff、backend-only 发布，验证 Pod imageID 与活跃训练逐项状态。Portal 核对 CI 和真实页面。
- [ ] 生产专用实验验证集成 A/B 隔离、上传下载摘要相同、撤销后失效，结束后撤销测试凭据，记录版本/证据/未完成项。

本机只编辑、审阅和 diff 检查；编译、格式化、单元/集成/E2E、lint 与构建全部在构建机。设计见[对应规格](../specs/2026-09-12-mlflow-integrations-artifacts-design.md)。
