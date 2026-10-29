# 存储

## 基础模式：管理员登记 PVC

`dataSpaces.enabled: false` 是基础默认。该模式不依赖 TOS、FSX 或 IDC：由集群管理员提供租户 namespace 中已 Bound 的 PVC，在平台“管理 → 存储”登记为 `pvc` 资产。PVC 的底层可以由已有 CSI 提供；平台不负责部署存储系统。

| 资产类型 | 容器路径 | 权限 |
| --- | --- | --- |
| dataset | `/mnt/data/dataset` | 只读 |
| checkpoint | `/mnt/data/checkpoint` | 只读 |
| output | `/mnt/data/output` | 可写 |

训练代码优先读取 `PLATFORM_DATASET_PATH`、`PLATFORM_OUTPUT_PATH` 和可选的 `PLATFORM_CHECKPOINT_PATH`，避免固定集群存储地址。

1. 在平台创建团队，核对实际租户 namespace（通常为 `tenant-<团队ID>`）。
2. 管理员在该 namespace 创建并验证 PVC，准备数据与可写输出目录。
3. 使用团队管理员或超级管理员在存储面板登记 PVC 名称与资产用途。
4. 在 Web 新建训练任务中选择固定 Git commit、已登记镜像和存储资产。
5. 验证输入可读、输出可写，以及新任务的 PVC 挂载与用户权限。

跨节点多 Worker 共同访问的数据通常需要支持 ReadWriteMany 的存储；ReadWriteOnce 卷不能假定可同时挂载到多个节点。文件所有者和权限必须匹配训练镜像用户。若选择相对路径，目录需要预先存在并符合资产授权范围。

PVC 资产不提供网页目录浏览，`browseEnabled` 必须为 false，`rootPrefix` 必须为空。当前 CLI 参数和命令预览尚未提供完整 PVC 资产提交支持，基础模式使用 Web 表单；不要复制逻辑数据空间的 CLI 示例当作 PVC 参数。

登记共享资产不等于跨 namespace 共享同一个 PVC。PVC 是 namespace 资源，涉及多个租户时由管理员分别提供适当的 claim 与授权，不能依赖同名自动创建。

## 可选模式：TOS / FSX 数据空间

对象存储集成目前具体针对 TOS 与 FSX CSI，不是通用 S3 实现。部署者需要提供自己的桶、区域、endpoint、CSI 配置和受限凭据来源，再显式启用 dataSpaces。

该模式可提供逻辑个人、团队、公共空间，以及相应的浏览、上传、工作区持久化和源码快照路径。常见逻辑挂载为 `/workspace`、`/mnt/storage/me`、`/mnt/storage/team` 和 `/mnt/storage/public`。个人 storage home 使用稳定用户标识，不能把团队迁移等同于个人文件迁移。

CLI `--working-dir`、原生 Ray working-dir 上传以及工作区快照依赖源码归档与对象存储链路，不能在未配置对象存储的基础模式中假定可用。调试镜像存在也不代表工作区持久存储已经就绪。

平台不会再安装 IDC 数据同步 Worker、功能仓同步任务或自动挂载固定网络目录。已有数据库中的旧 provider 值仅为迁移兼容保留；新安装应使用 `pvc` 或明确配置的可选 TOS 模式。

## 数据保护

存储资产只是访问登记，删除任务不能作为删除 PVC 数据的授权。共享输入只读，输出目录应按用户/任务隔离。管理员负责底层存储备份、生命周期、容量与恢复验证。数据库备份不包含训练数据；两者需要分别规划。

实现：[资产合同](../backend/domain/storage_asset.go)、[资产 API](../backend/api/storage_assets.go)、[迁移](../backend/db/migrations/)。
