# BEVFusion 公共数据根与交付验证计划

## 目标

让当前位于 `ray-train/tenants/local/datasets/public/` 的 BEVFusion 数据可由平台任务、调试环境和外部 `spk-rayjob` 提交一致地读取；后续迁移到标准 `ray-train/public/` 时只需更改部署配置，而不改训练代码。

## 数据契约

* 公共根只允许标准根 `ray-train/public/`，或当前租户隔离的临时根 `ray-train/tenants/<tenant>/datasets/public/`。
* Portal 浏览、RayJob 数据选择、调试环境 `/mnt/storage/public` 和任务输入 `/mnt/data/input` 必须解析到同一公共根。
* 工作负载继续使用租户根 PVC `data-tenant-*` 加受控 `subPath`；Pod 不接触 TOS AK/SK。
* 已存在的专用只读 public PV/PVC 不会被自动修改或删除。其迁移必须在无使用者时按运维步骤重建，避免隐式破坏任务。

## 实施与验证顺序

1. 添加受控公共根配置、领域校验和单元测试。
2. 将公共根注入数据目录浏览、工作区挂载规划、训练任务挂载规划和共享 PV 申请。
3. 通过 Helm 将当前环境设为临时根；保留 values 默认标准根。
4. 验证 FSX 租户根 PVC 在两台 GPU 节点均可挂载，再验证只读 public 与个人 runs 写入。
5. 使用实际 BEVFusion 代码分别通过 Portal 快照和 `spk-rayjob` 在 2 x 8 卡运行；检查 16 rank、数据读取、日志和 checkpoint。
6. 更新用户手册、BEVFusion 改造说明、外部提交说明和后续标准根迁移步骤。

## 不变量

* 不自动删除现有 RayJob、用户文件或非本平台资源。
* 不在代码、镜像或文档中写入 TOS、Git 或平台登录密钥。
* 只有实际任务已完成并写出 checkpoint 后，才标记为可供用户验收。
