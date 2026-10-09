# Envoy 叶证书自动加载

`transform_bootstrap(bootstrap, sds_directory="/etc/envoy-sds")` 返回独立的 bootstrap 和 `{filename: DiscoveryResponse}`。调用方将生成的 JSON 文件加入现有 Envoy ConfigMap，并将 `sds_directory` 指向这个 ConfigMap 的目录挂载位置。此函数不读取文件、不签发证书、不修改 Kubernetes，也不需要 SDS 守护进程。

Envoy 的本地文件 SDS 也要求 `node.id` 和 `node.cluster`。转换器在缺少 `node` 时填入明确的本地默认值；发布后处理器用命名空间和 ConfigMap 名填入组件名称。已有完整 `node` 原样保留，已有但不完整或非法的节点字段会失败。这些字段仅供本地 SDS 标识，不参与证书角色鉴权，也不连接远程控制面。

证书 Secret 必须以整个目录挂载，不能用 `subPath` 单文件挂载。`tls.crt` 和 `tls.key` 必须直接位于同一目录，例如 `/etc/obs-mtls/`。文件 SDS 在该目录监听 kubelet 对 `..data` 的原子替换，成对验证证书和私钥；SDS 配置自身也监听 ConfigMap 的父目录。相同叶证书配置会复用一个稳定文件名。

转换仅替换叶证书来源，保留原配置中的 TLS 最低/最高版本、SAN 校验、客户端证书要求、SNI、HTTP RBAC 等。`validation_context` 和根 CA 信任包继续保持原样：本实现不宣称自动完成根 CA 轮换。根 CA 更换需另行执行信任包重叠发布和验证。

构建机验收：

```sh
python3 -m unittest discover -s ops/observability/next/tests -p test_sds.py
python3 ops/observability/next/mtls/verify_sds_runtime.py --execute --output /root/<evidence>/sds-runtime.json
```

运行时验收使用固定摘要 Envoy 1.39.3、临时本地 CA、私有 Docker 内部网络，无宿主机发布端口或 Kubernetes 写入。检查服务端与客户端证书序列号均在不重启进程的情况下更新，旧的已授权连接继续工作，错误角色/无证书仍被拒绝，HTTP 路径权限保留，错误证书私钥组合不会替换最近有效证书，随后可恢复。生成的密钥和容器在结束时清理；证据只包含公开序列号、计数和通过状态。

证书更新不等于撤销已经建立的 TLS 会话；如需紧急撤销身份，应另行执行连接排空。这里针对正常证书续期。

官方依据（固定 1.39.3）：

- [SDS 及文件证书轮换](https://github.com/envoyproxy/envoy/blob/v1.39.3/docs/root/configuration/security/secret.rst)
- [TlsCertificate.watched_directory：成对读取与一致性检查](https://github.com/envoyproxy/envoy/blob/v1.39.3/api/envoy/extensions/transport_sockets/tls/v3/common.proto)
- [PathConfigSource.watched_directory：Kubernetes ConfigMap 更新](https://github.com/envoyproxy/envoy/blob/v1.39.3/api/envoy/config/core/v3/config_source.proto)
