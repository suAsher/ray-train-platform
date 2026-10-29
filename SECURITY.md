# Security

## 报告问题

请优先使用 GitHub 仓库的 Security → Report a vulnerability（若维护者已启用）。若不可用，请提交不含利用细节、凭据或敏感数据的 Issue 请求私下沟通渠道。不要将可复用攻击载荷、真实用户数据或密钥直接写入公开 Issue。

## 部署边界

- 基础模式使用本地账号与 PAT；公网访问应配置 TLS，并保护引导管理员密码与 PAT_PEPPER。
- OIDC 认证与平台授权分离。身份提供方的普通用户身份不自动获得管理员角色。
- 平台控制面具有创建租户资源的集群权限。安装前审阅 Helm RBAC；只向受信任的运维人员开放集群管理入口。
- 训练镜像按允许列表登记，Git 来源按允许列表限制。用户代码在训练容器执行；不应将普通 Kubernetes namespace 隔离宣称为对恶意代码的完整安全沙箱。
- PVC 资产由管理员登记，用户不能直接指定任意节点目录、hostPath 或其他租户 claim。
- 不要公开 Kubernetes API、Ray Dashboard、数据库、对象存储凭据或内部调试端点。集群 NetworkPolicy 是否实际执行取决于所用 CNI，需要单独验证。
- MLflow 共享访问与平台训练记录的角色过滤是不同策略，启用原生访问前评估其读写权限。
- 备份数据库与训练数据并验证恢复。Helm 回滚不撤销数据库迁移。

凭据通过 Kubernetes Secret 或部署者管理的凭据系统注入。不要将密码、PAT、AK/SK、完整 kubeconfig 或 Secret 内容提交到仓库或粘贴到日志。
