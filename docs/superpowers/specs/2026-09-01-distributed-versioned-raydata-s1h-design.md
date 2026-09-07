# 分布式版本化 Ray Data S1H 设计

## 目标

将持续同步的公共原始区发布为不可变、可恢复、增量的 Parquet 数据版本；由 Ray Train 托管的 S1H BEVFusion 通过 Ray Data 流式读取，并把 NVMe 仅作为有界热点 shard 缓存。使用同一全量版本验证 Portal、`spk-rayjob` 和原生 Ray Job 三个提交入口。

## 范围与非目标

- 源数据根是平台已登记的公共 `labeled` 数据空间。发布器只接收控制面生成的可信索引；不接收租户提供的对象存储路径或凭据。
- `READY` 是训练唯一可选状态。`PACKING`、失败分区和半成品对象均不得暴露为训练输入。
- 本次 S1H 目标是现有 lidar-only Parquet v1 适配器。WebDataset/TAR 的图像和通用二进制 schema 是后续独立版本，不混入 v1。
- 数据链路验收和模型收敛验收分开。S1H 的类别头与数值稳定性问题不能被链路成功掩盖。

## 架构

发布控制面冻结 source inventory，基于样本元数据、源对象版本信息和 schema 生成确定性的分区输入指纹。每个分区都有独立状态、尝试次数、租约所有者、租约到期时间、输出 shard digest 和经过校验的 receipt。

控制面为一个发布 run 创建固定数量的低优先级、CPU-only worker Job。worker 循环领取一个尚未完成或租约过期的分区，使用受限的服务身份读取可信源对象，并写入内容寻址的 Parquet shard。对象以条件写入和 digest 校验保证可重放；分区 receipt 在对象成功可读后才提交。worker 故障不会使 run 失败，过期分区由其他 worker 接管。达到失败重试上限的分区令 run 保持失败可诊断状态。

finalizer 只在每一个分区都有完整 receipt 后写入单个不可变 manifest，并在一次比较交换中使版本从 `PACKING` 转为 `READY`。新版本计划时会查找同数据集、同 schema 的最近 `READY` 版本；输入指纹相同且对应对象仍能通过校验的分区直接复用，只有新增或变化分区重新物化。

## 数据与缓存契约

manifest 记录版本、schema、样本 split、shard 相对路径、shard digest、行范围与版本化统计信息。运行时不得从 manifest 推导任意对象键，也不得接受未校验路径。

Ray Data 从受控 manifest 构建流式 Dataset，并把批次按 Ray Train worker 分配。S1H adapter 仅将 v1 Parquet 行解码为 lidar-only BEVFusion 所需的数组与标注；不再逐文件遍历原始小文件。

`auto` / `bounded` 策略只按 `(dataset version, shard digest)` 缓存完整 Parquet shard 到节点 NVMe。缓存使用容量上限和 LRU 回收；未命中时从持久对象存储流式读取，缓存丢失不得影响正确性。Ray object spilling 仅使用独立的本地临时空间，不能成为数据真相、checkpoint 位置或跨任务缓存。

## 失败处理与可观测性

- 发布进度显示总数、复用数、完成数、失败数、活跃租约、最近失败阶段和重试次数；错误信息脱敏，不包含对象存储凭据或不必要的源路径。
- 控制面重启、worker 退出、节点丢失和条件写入冲突都必须可重试且幂等。
- 训练指标至少包含 manifest/version provenance、Ray Data 吞吐、数据等待时间、cache hit/miss/eviction、spilling bytes、GPU 利用率与训练 iteration 时间。

## 验收

先完成小规模合成数据的发布、恢复、复用、manifest 完整性和缓存单元/集成测试。随后将全量 `labeled` 发布至新的 `READY` 版本，执行受控的读取预检，再用新的干净 S1H checkout、同一代码 commit、镜像 digest、版本、资源规格和训练参数分别从 Portal、`spk-rayjob`、原生 Ray Job 提交 2×8 任务。

三入口任务比较提交参数、版本 provenance、Ray Data/NVMe 指标、Ray Train 编排、日志、MLflow、checkpoint 和结果。性能对比另行在同一入口及相同训练配置下运行“原始小文件直读、原始小文件 bounded NVMe、Parquet Ray Data bounded NVMe”，避免把入口排队差异混为 I/O 差异。每条任务记录 ID、代码 commit、镜像 digest、版本、输入、资源、关键指标、产物和算法异常。
