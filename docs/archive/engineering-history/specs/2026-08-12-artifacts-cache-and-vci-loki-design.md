# 训练产物、节点缓存与 VCI Loki 设计

日期：2026-08-12
状态：已批准实施
范围：任务产物页、GPU 节点缓存契约、Loki 生产部署

## 目标

训练用户不接触 TOS 地址或 AK/SK：选择已授权的数据空间后提交任务，训练程序通过固定本地路径读写数据；任务完成后，用户在任务详情页直接浏览、预览和下载自己的产物。日志由 Alloy 收集到 Loki，训练 Pod 删除后仍可查询。GPU 节点新增的多块数据盘只用作可丢弃的高速缓存，不成为数据或产物的唯一副本。

## 已验证的运行条件

- 集群现有一台 4090 ECS 节点，以及上海 A/B/C 三个 VCI 虚拟节点；VCI 上已有 CoreDNS、KubeRay 和 Kueue 控制组件。
- `ebs-ssd` 是当前唯一动态 StorageClass，实际最小可创建容量为 20Gi；在 VCI 节点上创建、挂载、删除并重建 StatefulSet 后，同一 EBS 卷的数据可以恢复。
- VCI 不读取实体节点的 containerd mirror 配置。直连 DaoCloud、华为公共镜像源超时；`harbor.wellspiking.ai/hub/...` 直连拉取成功。
- 新建 VCI EBS 卷没有按 `fsGroup` 修正根目录权限。一个只含 `CHOWN` capability 的 root 初始化容器可将 Loki 卷交给 UID/GID 10001，主容器继续以非 root 和只读根文件系统运行。
- 训练数据已通过 FSX 静态 TOS PV/PVC 挂载；它们继续是数据真相，训练工作负载不注入 TOS 凭据。

## 数据与产物体验

每个任务的输出仍由平台分配在所选输出数据空间内：`runs/<job-id>/`。用户在任务详情打开“训练产物”页，只能看到该任务输出目录内的对象：

```text
任务详情
  ├─ 日志
  ├─ 指标
  ├─ 训练产物
  │    ├─ 子目录浏览
  │    ├─ 小型文本 / JSON / 图片预览
  │    └─ 文件下载
  └─ Pod 拓扑
```

后端以当前登录用户的 tenant 查询任务，再从任务持久化的 `ResolvedStorage.Output` 计算对象前缀。浏览与下载 API 只接受相对路径，拒绝绝对路径、URL、反斜杠、NUL 和 `..`。API 响应不返回 bucket、TOS 根前缀、PVC 名称或凭据。

目录列举使用 TOS 的 delimiter 分页；文件下载由后端在授权后签发极短期 GET URL。前端只消费平台 API，浏览器不会拿到 AK/SK。预览有大小和 MIME 白名单限制；其它对象只提供下载。

## Loki：VCI 上的三副本单体部署

在当前日志规模下，使用 Loki Chart 18.7.5 的 `Monolithic` 模式、三个 `singleBinary` StatefulSet 副本，而不是已弃用的 Simple Scalable 模式。三副本均调度到 VCI，但每一个 StatefulSet Pod 独占一个 `ebs-ssd` RWO PVC：

```text
Alloy ──push──> loki-gateway (2 VCI replicas)
                   │
          ┌────────┼────────┐
          ▼        ▼        ▼
      loki-0    loki-1    loki-2     (VCI A/B/C, EBS WAL × 3)
          └────────┼────────┘
                   ▼
        vke-cluster TOS bucket        (TSDB index + chunks + retention)
```

- `vke-cluster` 是平台专用 bucket；Loki 通过 S3 兼容 TOS API 原生写入对象，不创建 TOS StorageClass 或 TOS PVC。
- EBS 的职责是 `/var/loki` 下的 WAL、索引缓存和 compactor 临时空间；TOS 是长期数据源。每个副本有独立卷，不能共享。
- `replication_factor: 3`、按 zone `DoNotSchedule` spread、PDB `minAvailable: 2` 和 120 秒优雅退出保证单个 VCI 副本替换时持续可写。
- 所有镜像直接使用 `harbor.wellspiking.ai/hub/grafana/loki`、`harbor.wellspiking.ai/hub/nginxinc/nginx-unprivileged` 与 Harbor BusyBox；不依赖 ECS containerd mirror。
- `loki-tos` 仅以环境变量提供给 Loki。它必须拥有 `vke-cluster` 的 List/Get/Put/Delete 对象权限。下一阶段应为 Loki 单独签发最小权限 TOS 身份；当前不在 values 或日志中保存密钥。
- Loki gateway 仅 ClusterIP；Alloy 和平台后端通过 `loki-gateway.loki.svc.cluster.local` 访问，外部不暴露 Loki。

已存在但无所属工作负载的 `data-loki-write-{0,1,2}` Pending PVC 是旧 Simple Scalable 安装残留。正式部署前在确认无 owner 后删除；不会删除任何 Bound 训练或平台 PVC。

## GPU 多数据盘缓存

缓存与数据空间严格分层：

| 层级 | 介质 | 用途 | 是否可丢失 |
| --- | --- | --- | --- |
| 数据 / checkpoint | TOS FSX、IDC NFS（只读） | 训练输入真相 | 否 |
| 训练产物 | TOS FSX（任务隔离写入） | checkpoint、模型、结果 | 否 |
| Pod 高速缓存 | VKE `csi-local` 通用临时 PVC | dataset shard、tokenizer、Ray spill | 是 |
| Ray 共享内存 | `emptyDir: Memory` | Plasma / 临时 IPC | 是 |

新增 GPU 节点后，运维将每块高速数据盘交给 VKE `csi-local`（LVM 管理或 dedicated local storage），创建 `ray-cache-local` StorageClass。Ray Head 和每个 Worker 使用 Kubernetes generic ephemeral volume，分别获得调度到本机盘的 RWO 临时 PVC，并在容器中看到 `/mnt/cache` 与 `PLATFORM_CACHE_PATH`。任务结束后缓存自动回收；清理失败也只能留下无凭据、无长期业务真相的缓存文件。

不使用节点 `fstab`、SSHFS 或宽泛 `hostPath` 把数据盘暴露给用户 Pod。IDC 的 NFS 只读导出继续以一个目录一个静态 PVC 的方式注册。个人读写空间如要进入所有分布式 Worker，后续必须以用户级 RWX NAS/NFS PVC 或平台同步任务接入。

## 不在本期范围

- Portal 不创建 bucket、PV/PVC、Local StorageClass、TOS policy 或 TOS Secret。
- 不自动把完整数据集复制到每个节点；训练代码可按 shard 将热点数据复制到 `PLATFORM_CACHE_PATH`，后续随真实训练代码加入预取器。
- Prometheus、DCGM、Grafana dashboard 作为下一项可观测性工作；本期先让 Loki 日志在 Portal 可查询并可保留。

## 验收

1. Loki 三个 StatefulSet Pod 分布在三个 VCI zone，三个 EBS PVC 均为 Bound；gateway 有两个 Ready 副本。
2. 通过 gateway 写入一条带唯一标签的日志并查询返回；Alloy 的目标无持续发送错误；Portal 的已完成任务可读取保留日志。
3. 删除任一 Loki Pod 后，该副本在同一 WAL 卷重建并恢复 Ready，另外两个副本持续服务。
4. 一个用户无法列出、预览或下载另一个 tenant 的任务产物；返回数据不包含 TOS 凭据、bucket 或 PVC。
5. 在尚未安装 `csi-local` 的当前单卡测试集群，任务仍可提交运行；缓存功能显式显示“节点缓存尚未配置”，不会悄悄退化为 hostPath。
