# guofeng.su 个人存储验收

此操作员工具固定绑定平台用户 `guofeng.su`、其稳定用户 ID、`ray-train-platform` namespace 和合规区端点，不是面向用户的 API，也不替代 Portal 认证验收。

仅在既有构建机隔离候选源码中编译：

```sh
cd backend
go build -o /restricted-state/personal-storage-acceptance ../ops/personal-storage-acceptance/main.go
```

所有动作都需显式传入 `--kubeconfig`、`--known-hosts`、`--state-dir`。状态目录必须位于 root 独占、父目录可信的位置，权限为 `0700`；只保存 `0600` 的公开绑定元数据与公钥。私钥只在 Kubernetes 不可变 Secret 中。

- `--action=enroll`：创建或复用本人 Secret，导出公钥；不连接 SFTP，不写生产数据库。用户亲自登记该公钥，保留原有密钥。
- `--action=diagnose`：只读逐段诊断固定端点、可信主机密钥、本人公钥认证、SFTP 子系统和根元数据；不列出文件或写数据。
- `--action=check`：验证真实 SFTP 根过滤，以及本次随机 UUID 目录的建目录、排他创建、读写、改名、删除。
- `--action=mount-check`：使用实际内核 FUSE 验证同样操作，并通过独立 SFTP 会话核对本次文件内容。挂载必须在独立容器的私有挂载命名空间内进行，给 `/run` 单独配置 tmpfs、`/dev/fuse`、`SYS_ADMIN` 和必要的 AppArmor 配置。禁止把宿主 kubelet 目录或双向传播挂载带入容器。

默认清理只限本次 UUID 目录内的固定测试路径，使用独立限时的本人 SFTP 会话，不递归删除，不操作已有用户文件。`--keep-markers` 会保留本次标记供本人核对；仍执行独立测试文件的真实删除。清理失败只输出本次生成的标记目录名，必须定点恢复，不可扫描或批量删除同前缀目录。

日志不包含私钥、原始上游错误、私人根目录名称或私人文件内容。真实本人账号的成功只证明功能连通，不能替代获准双账户互不可见验证或上游有效权限配置证明；此工具不启用 Helm、CSI 或 `personalRootIsolationVerified`。
