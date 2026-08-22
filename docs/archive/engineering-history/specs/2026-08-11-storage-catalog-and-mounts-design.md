# 受控数据目录与训练挂载设计

日期：2026-08-11
状态：已批准实施
范围：TOS 数据目录选择、RayJob 数据挂载、IDC 只读目录登记

## 目标

让训练用户从 Portal 选择已授权的数据目录、Checkpoint 和产物根目录，而不是输入 `tos://` 或 `idc://`。用户能浏览自己被授权的 TOS 前缀，但不能枚举整个桶；训练和调试工作负载只看到被选中的目录，且不获得 TOS 长期 AK/SK。

## 事实与边界

- TOS 目录是对象前缀，不是实际文件夹。TOS bucket policy 与 ListBucket prefix 条件负责真实权限边界；Portal 目录树只是该边界的受控展示。
- 当前 VKE 已安装 `fsx.csi.volcengine.com`。TOS 应使用 FSX CSI 的静态 PV/PVC 以 Pod 级方式挂载，不使用节点 `fstab` 或 `hostPath`。
- IDC 的三个 NFS 导出目录由运维创建静态 NFS PV/PVC，使用 `ReadOnlyMany` 和只读 `volumeMount`。用户节点上的 SSHFS 个人挂载不进入 V1 的 RayJob/Dev Workspace，因为它不能保证每个 Worker 一致可见，也不应把节点 SSH 身份带入 Pod。
- 本项目不会创建 TOS bucket policy、TOS CSI PV/PVC、NFS PV/PVC 或任何 Secret；这些由基础设施清单预置。平台只登记已存在的 PVC 并在任务中引用它们。

## 数据目录目录册

新增 `storage_assets` 表。每条资产代表一个已经由基础设施限制好的目录根：

```text
wellspiking-v1
  tenant: local
  kind: dataset
  provider: tos
  claim: tos-wellspiking-v1-ro
  root prefix: datasets/tenant-local/wellspiking-v1/
  access: read-only
```

V1 的可见范围是“本租户”或“资产所有用户”。目录根是实际安全边界：一个 asset 对应一个最小权限 PVC/TOS prefix。目录根内部可按对象前缀浏览和选择子目录，但用户不能从该根向外浏览。

资产类型固定为：

- `dataset`：只读；可选择资产根内的子目录。
- `checkpoint`：只读；可选择资产根内的子目录。
- `output`：可写；Portal 自动使用 `runs/<job-id>` 作为本任务输出目录，用户不输入路径。

IDC 资产可登记为同一模型的 `provider=idc`。IDC 没有由控制面提供的文件浏览器，V1 只允许选择已登记目录根；后续如需要子目录浏览，必须通过独立、受控的 IDC 文件目录服务实现，不能让后端读取任意 NFS 路径。

## 授权和 API

普通用户只能调用：

```text
GET /api/v1/storage-assets?kind=dataset|checkpoint|output
GET /api/v1/storage-assets/{id}/directories?path=<relative>&cursor=<opaque>
```

第二个接口只接受资产根内的相对路径，拒绝 `..`、反斜杠、绝对路径、URL、任意 bucket 或 prefix。TOS `ListObjectsType2` 使用 `Delimiter=/`，后端永远把请求前缀限制在 asset 的 `root_prefix` 下，并限制页面大小。

TenantAdmin 创建、删除本租户资产；SuperAdmin 可以创建共享资产。创建资产只接受 PVC 名和受控 TOS 根前缀，不接受 AK/SK。

## 任务契约和挂载

Portal 提交的 JobSpec 使用 `datasetStorage`、`checkpointStorage`、`outputStorage` 选择项：资产 ID 与相对目录。提交服务根据当前用户、租户、资产类型与读写模式解析这些选择，忽略客户端伪造的挂载字段，将解析结果持久化在 JobSpec。

Ray Head 与每个 Worker 的挂载路径固定为：

```text
/mnt/data/dataset
/mnt/data/checkpoint
/mnt/data/output
```

训练代码读取新的本地路径环境变量：

```text
PLATFORM_DATASET_PATH
PLATFORM_CHECKPOINT_PATH
PLATFORM_OUTPUT_PATH
```

Submitter Pod 不挂业务数据卷，只有 Source Materializer 仍在受控 init container 中按需使用 TOS 凭据拉取已批准代码源。主训练容器、Ray Head、Ray Worker 和 GPU 调试 Worker 不注入 `AWS_ACCESS_KEY_ID` 或 `AWS_SECRET_ACCESS_KEY`。

## UI

“训练数据接入”页改成数据资产目录册与目录浏览器。新建训练任务的第三步不出现 URI 文本框；三类选择器均显示资产名称、存储来源、只读/可写状态和面包屑。输出选择器只选一个可写输出资产，页面展示平台生成的运行目录预览。

目录浏览器返回的内容只来自已授权 asset；Bucket 名、完整根前缀、凭据、PV/Secret 名称均不返回浏览器。

## 基础设施交付物

运维为每个已登记 TOS asset 创建带最小 Prefix policy 的 FSX TOS PV/PVC，并让其在目标租户 namespace 可用。NFS 只读资产使用静态 NFS PV/PVC；NFS 服务端也必须只读导出。平台部署步骤验证 PVC `Bound`，而不是读取其 Secret。

建议的 TOS 根策略：

```text
datasets/<tenant>/<dataset>/    Get + List(prefix only)
models/<tenant>/<model>/        Get + List(prefix only)
outputs/<tenant>/<user>/        Get + List + Put + Delete(prefix only)
```

个人 SSHFS 路径保持用户自行使用，不作为分布式任务存储。若后续需要进入所有 Worker，需改为每用户的 RWX NFS/NAS PVC 或平台受控同步任务。

## 验收

1. Engineer 不能查看其他租户或其他用户私有 asset，也不能让 API 列举 asset 根外的对象前缀。
2. 训练表单没有可编辑的 TOS/IDC URI 字段；提交服务拒绝伪造或类型不符的 asset ID。
3. Ray Head/Worker 看到正确的挂载和本地路径环境变量，且主容器环境中不存在 AWS AK/SK。
4. 数据集和 Checkpoint 挂载只读；输出路径可写且每个任务独立。
5. TOS/IDC PVC 未配置或未绑定时，任务在提交前返回明确错误，不创建 RayJob。
