# Nightingale 9.1.1 Sentinel 最小修补

这是新可观测性夜莺的候选派生构建，不能据此宣称已部署或 HA 验收通过。父发布流程统一运行测试、构建、锁定摘要和部署。

固定官方镜像 `/app/n9e` 的实际 build-info 确认 `go-redis/v9 v9.0.2`，源码提交为 `4e056150142b2e1ed6966c9317a9e08442c399f7`；官方产物带 `dirty` 标志。`upstream-lock.json` 记录官方 tag、归档 SHA256、运行镜像、构建器和原模块的两个 Go checksum。不能把官方 dirty 产物和源码归档称为字节可重现。

真实三 Redis/Sentinel 测试中，主 pair 冻结后 Redis 自动升主并完成剩余副本同步，但夜莺持续向验证码接口返回 `context deadline exceeded`。v9.0.2 `MasterAddr` 将连接层 TLS 握手超时的错误当成调用者上下文取消，直接返回而不尝试健康 Sentinel。补丁仅修改该函数三个判断，改为查询调用者的 `ctx.Err()`；真实调用者取消/到期仍返回。参考[上游同类问题](https://github.com/redis/go-redis/issues/3172)和[后续修复](https://github.com/redis/go-redis/commit/e191cf9dbe6e28e2cec5cde18207ea6605116cdc)。这是针对 v9.0.2 的语义 backport，未原样 cherry-pick，也不升级依赖。

`prepare-redis.sh` 开启 Go checksum database，验证模块 checksum 后复制到独立目录；只修改复制品。N9E 构建通过一个本地 `replace` 使用该源码，其他依赖仍按原 `go.mod/go.sum` 和 `-mod=readonly` 构建。不修改共享 module cache，不关闭校验。

## 协议回归

在构建机的独立 Go 容器内运行，建议每容器 2 CPU/2Gi，设置 `OBS_N9E_ISOLATED_BUILDER=1`；不用宿主端口、GPU、Kubernetes 配置或生产凭据：

```bash
bash test-redis.sh baseline /evidence/n9e-redis-red
bash test-redis.sh patched /evidence/n9e-redis-green
```

两个目录必须全新。测试用真实 TCP/TLS：一个端口接受 ClientHello 后保持沉默，另一个要求客户端证书并返回真实 RESP Sentinel 响应。无缓存和已缓存坏 Sentinel 均须在原代码复现断言失败，补丁后访问健康实例；还验证调用者取消、已过期、无客户端证书、错误 CA 和错误主机名。编译失败、网络下载失败不能算 RED。当前补丁不改变 v9.0.2 的 `DialWithDialer` 传输 API，因此握手中的取消仍在连接超时返回后被观察到。

`protocol.json`、`protocol.stderr`、`coverage.out`、`coverage.txt` 和 `vet.txt` 是各阶段证据。协议 fixture 不是三节点 Redis HA 的替代品。

## 构建与资源保持

只替换固定原镜像的 `/app/n9e`，保持 Python、字体、运行配置和 entrypoint。构建使用 `cmd/center`、`sandbox_embed,qa_code_embed`，并重用固定原二进制中的全部八份资源：原 statik 前端 ZIP、三个 sandbox 资产及四个 QA 资产。不能运行抓取 latest 的 `fe.sh` 或原浮动资产生成脚本，不能省略标签后声称功能等价。

`extract-assets.py` 是离线只读提取器；只接受其支持的 ELF 格式，每个资源必须唯一识别、完整校验并记录原始偏移/长度与 SHA256。`assets.sha256` 必须由已审提取证据固定，缺失时构建失败关闭。原 ZIP 通过 `//go:embed` string 和原 `statik/fs.Register` 接口重新嵌入，不重压缩、不重建前端。固定源码归档自带旧的生成前端，准备脚本只替换这个生成文件表示。

2026-10-08 在构建机以固定原镜像、无网络/只读根目录/1 CPU/512Mi 容器完成提取，八项资源校验均通过，前端含374个文件。原二进制 SHA256、Go1.25.12 和首页 SHA256 已记入 lock；八项资源哈希已写入 `assets.sha256`。完整偏移、QA 原始 commit、长度和校验记录保留于构建机 `n9e-source-evidence-20261008T105347Z/asset-inspection/extracted-original-03/extraction.json`。这是原资源提取成功的证据，尚不能替代派生构建和故障后登录验收。

提取器扫描 Go slice 时先验证候选为完整 ELF/TAR；主程序头部等短 slice 伪候选不计入资源集合。实际资源必须仍满足唯一性与完整八项集合检查，不能用跳过无效候选掩盖缺失资源。

```bash
docker buildx build --platform linux/amd64 --load \
  --build-context n9e-upstream=/evidence/official-source \
  --build-context n9e-assets=/evidence/extracted-assets \
  -f Dockerfile -t obs-n9e-derived:<候选SHA> .
```

仅父发布流程执行。建议构建限额 4 CPU/12Gi、临时磁盘预留15Gi；`GOMAXPROCS=2`、编译并发 `-p 2`。本候选仅 amd64，不能把一个架构的 sandbox 资源用于 arm64。

完成构建后先取得精确本地 image ID，以 `../verify-runtime.py --n9e-image-id sha256:<ID>` 执行原完整验收。首页必须与原镜像 HTML 的 SHA256 相同，且从实际 HTML 引用中加载 `/assets/index-0720a85a.js`，验证 JavaScript MIME、17314843字节和固定SHA256；证据保存为 `frontend.json`。页面与资源均经过 UI 身份的 mTLS 入口，不允许跳转或外部源，响应分块哈希并限制大小。

必须证明三 Redis/Sentinel 原生 mTLS 自动选主、健康副本完成同步、同一 N9E 进程在故障前后均完成真实验证码错误拒绝、正确登录和 profile；旧主保持冻结。不通过则禁止发布。之后才由父流程推送、记录 registry digest 并更新新组件 manifest；当前运行组件和 CNPG 固定摘要不在此修补范围内。
