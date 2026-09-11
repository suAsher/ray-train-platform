# GPU 配额与 MLflow 接口实施计划

**Goal:** 在 Portal 恢复日常配额可见性，提供精确 Run 对比和最小权限的外部实验读写。

**Architecture:** 保持 Go 平台 DB 的身份与资源授权权威；MLflow 专用客户端按指定 Run 验证归属；Portal 仅消费受控 API。没有数据库迁移或训练运行时变化。

**Tech Stack:** Go/Gin、MLflow 3.14 REST、Vue 3/Element Plus、Node 合同测试；全部测试/编译在构建机。

## 1. Portal 配额与实验

- [ ] 在 `scripts/check-raytrain-experience-contract.mjs` 先覆盖 quota 字段、未知值、过期请求、精确指标键、缺失值、2–4 Run 选择与 Run 不匹配。
- [ ] 构建机 Node 22 容器执行该脚本，记录缺少能力的 RED。
- [ ] 实现 `src/views/rayTrain/myQuota.js` 及配额组件、`experimentCatalog.js` 纯函数和实验中心搜索/比较组件，保持原有票据契约。
- [ ] `AccountSecurity/index.vue` 添加令牌用途选择，`tokenPurpose.js` 返回全新 scope 数组：training 保持原范围、mlflow-read 仅 jobs:read、mlflow-write 为 jobs:read + mlflow:write；未知用途拒绝。先执行 token-purpose 合同 RED，再实现。

## 2. 后端指定 Run 接口

- [ ] `backend/api/mlflow_integration_test.go` 覆盖 owner/team/scope、未知 Run、错误 JSON、保留标签、审计失败与限流。
- [ ] `backend/observability/mlflow_integration_test.go` 使用 httptest 上游校验指定 Run、provenance、实验与用户归属、指标历史和写入请求体；构建机记录 RED。
- [ ] 实现新文件和 routes 注册；只对新写接口使用 mlflow:write，旧令牌默认范围不变。将 `JobExperiment` 用作新读接口响应，具体实现与测试一起审阅。
- [ ] 构建机完整 `go test -timeout=20m ./...`，单独检查新增接口测试覆盖率。独立审查拒绝分支、审计和兼容性。

## 3. 文档与验证

- [ ] 更新 `docs/MLFLOW_USER_GUIDE.md` 与新增对接说明，区分候选功能、线上状态和后续生命周期。
- [ ] Portal candidate archive 审查已跟踪 env；仅保留非敏感构建输入。在构建机执行 `docker build --pull -f docker/Dockerfile.lint .`、新增两个合同和 Vite build。
- [ ] 有可用浏览器与认证时验证列表配额、搜索/比较、单条 Run 原生深链和使用说明；记录不可达环境，不用 mock 冒充生产验收。
- [ ] 用户明确授权后才推送/部署：复核远端、backend 四端同步、只构建 backend、最小 Helm dry-run diff、运行中任务 UID/重启数对比；Portal 按 dev CI/CD 验证。未授权则保留本地候选。
