# 上游包注入合同

这些文件是独立构建机验证的输入，不在本机编译或执行。相同测试必须先注入 SHA 固定的未修改上游运行 RED，再对同一上游应用受审阅补丁运行 GREEN。RED 必须来自安全断言失败，编译错误或环境故障不算 RED。

| 输入 | 上游目标 |
| --- | --- |
| `sharedfixture/fixture.go` | `internal/observabilitytest/fixture.go` |
| `fixtures/webserver_access.go` | `pkg/management/postgres/webserver/observability_fixture_access.go` |
| `metrics_tls_test.go` | `pkg/management/postgres/webserver/metricserver/observability_metrics_tls_test.go` |
| `status_auth_test.go` | `pkg/management/postgres/webserver/observability_status_auth_test.go` |
| `status_retry_test.go` | `pkg/management/postgres/webserver/client/remote/observability_status_retry_test.go` |
| `webhook_source_test.go` | `pkg/observabilityauth/observability_webhook_source_test.go` |

所有协议回归以 `TestObservability` 开头。先运行这些测试证明安全边界，再运行受影响包的现有完整回归；不能删上游用例获得通过。

8000 的真实构造器使用 `rest.InClusterConfig`。运行器必须在隔离容器内预置 `/var/run/secrets/kubernetes.io/serviceaccount/token`，内容仅为 `observability-protocol-fixture-only`，禁止挂载真实集群凭据。测试将 Kubernetes 目标设置为 `127.0.0.1:1`，健康与 status 采用无需 Kubernetes 请求或 PostgreSQL 的真实分支。测试专属 `webserver_access.go` 仅暴露内部 server 供临时监听，不能进入补丁或镜像。

未修改上游没有 `pkg/observabilityauth.NewWebhookHandler`。仅 RED 工作区额外注入 `baseline/webhook_identity_adapter.go` 到 `pkg/observabilityauth/observability_baseline_adapter.go`，该恒等适配器忠实表达上游 webhook 不过滤来源的行为。它只解决测试符号不存在的问题，不增加认证，不允许带入 GREEN 或镜像。GREEN 必须在全新工作区从受审阅补丁获得真正 helper，并验证 Operator 启动路径确实使用它。

证书和 CA 都在测试中即时生成，客户端校验服务端证书，不使用 `InsecureSkipVerify`。8000 和 9187 测试保留真实生产构造器的路由及 TLS 配置，只将监听端口换成测试临时端口。9443 通过真实 TCP/TLS/HTTP 请求，利用 accepted-connection fixture 将连接的 `RemoteAddr` 设为控制面地址；这验证 HTTP 层地址传播与认证逻辑，不证明集群 apiserver 的实际 SNAT 来源。实际来源仍需 canary 验收。
