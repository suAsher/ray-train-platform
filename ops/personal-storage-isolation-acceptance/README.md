# 双账号个人存储隔离验收

仅用于本次明确授权的 `guofeng.su` 与 `raytrain-test`，平台用户 ID、合规区端点、namespace 和可信主机指纹均固定。它是受限操作员工具，不是用户 API，不修改 membership、生产绑定数据库或 Helm。`raytrain-test` 的现有 `pending-users` 状态保持不变。

从构建机隔离候选的 `backend/` 编译：

```sh
go build -o /restricted-state/personal-storage-isolation-acceptance \
  ../ops/personal-storage-isolation-acceptance/main.go \
  ../ops/personal-storage-isolation-acceptance/isolation.go
```

所有动作都要求显式的 `--kubeconfig`、`--known-hosts`、`--state-dir`（第二账号目录）。状态目录须位于 root 独占的可信父目录中，权限 `0700`，文件只含 `0600` 公开元数据。私钥仅保存于各自独立的不可变 Kubernetes Secret，不接收或保存密码。

- `--action=enroll-test`：生成或复用第二账号专属公钥，用户亲自在该账号的存储门户登记，保留已有公钥。该动作不建立 SFTP 连接。
- `--action=isolation --a-state-dir=<guofeng.su原状态目录>`：两个真实账号分别正向读写各自新建的 UUID 标记，然后验证双方无法列出、stat 或打开对方标记。负向写打开不使用 CREATE/TRUNC，也不写入意外打开的文件。交叉密钥测试必须在可信主机校验成功后得到明确认证拒绝，不能把网络故障当作隔离成功；随后再次建立正常会话读回标记作对照。
- 增加 `--with-fuse`：同时验证两个实际内核挂载。必须在独立容器私有挂载命名空间执行，为 `/run` 单独配置 tmpfs，提供 `/dev/fuse`、`SYS_ADMIN` 和必要的 AppArmor 配置；不得挂载宿主 kubelet 目录或设置双向传播。

只清理本次创建的 UUID 内固定文件及空目录，不递归删除，不输出私人根目录名称。成功证据在清理结束后输出；失败只输出固定 stage 及本次 UUID，供定点恢复。

通过只证明本次两账号及专建标记的当前访问边界。已知共享入口隐藏属于适配器策略，不能当作上游 jail 证明。上游脱敏有效配置、Portal 认证和生产调试 PVC 默认挂载仍需单独验收；此工具不会启用生产功能。
