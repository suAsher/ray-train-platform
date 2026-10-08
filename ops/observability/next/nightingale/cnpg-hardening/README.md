# CNPG authentication build inputs

该目录仅服务新增 `observability-alerting/n9e-postgres`。认证设计、兼容性变化及真实集群闸门见 [DESIGN.md](DESIGN.md)。本目录交付源码级补丁和真实协议回归，不表示已构建、推送或部署。

`upstream-lock.json` 固定官方 tag 对应的 commit、官方源码归档 SHA256、Go 1.27.1 builder 和 CNPG runtime digest。`patches/0001-observability-auth.patch` 仅改动 Operator webhook 接线、原生 status/metrics 服务和状态重试。Dockerfile 同时替换上游镜像中的 amd64/arm64 manager；PostgreSQL 镜像保持独立。

## Builder 验证

只能在既有 builder 的隔离候选运行；本机不执行测试、格式化或构建。由 root agent 统一将已提交候选通过 Git bundle 送入 detached worktree。另传输官方源码归档，以 `cloudnative-pg.tar.gz` 存到只含该文件的输入目录，输入须符合锁文件 checksum。不要把 13 MiB 的上游源码归档或测试生成的密钥放进 Git。

以下命令在 builder 执行；`candidate_dir` 为已核验 SHA 的候选目录，`upstream_dir` 为归档目录，`evidence_dir` 为新建证据目录。RED、GREEN 各用新容器；不挂载任何 kubeconfig、service-account token 或 Docker socket。

```bash
CNPG_BUILDER='harbor.wellspiking.ai/hub/library/golang:1.27.1@sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2'
docker run --rm --cpus=4 --memory=12g \
  -e OBS_CNPG_ISOLATED_BUILDER=1 -e GOPROXY=https://mirrors.tencent.com/go/ \
  -e PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
  -v "$candidate_dir:/workspace:ro" -v "$upstream_dir:/upstream:ro" \
  -v "$evidence_dir:/evidence" -v cnpg-observability-go-mod:/go/pkg/mod \
  -v cnpg-observability-go-build:/root/.cache/go-build \
  "$CNPG_BUILDER" bash /workspace/ops/observability/next/nightingale/cnpg-hardening/test-upstream.sh \
  baseline /upstream/cloudnative-pg.tar.gz /evidence/baseline
```

同一命令改成 `patched ... /evidence/patched` 执行 GREEN。脚本验证源归档、创建新源目录、注入相同测试，并使用纯测试假 token 让真实 CNPG 构造器运行。`baseline` 仅额外注入无保护的 webhook identity adapter，以表达原始上游行为；该适配器、fixture 和测试 accessor 均不进入镜像。

RED 必须是 8000、9187、9443 各自安全断言失败，编译或依赖错误不能算成功。GREEN 运行协议矩阵、原有受影响包全部测试、controller 编译、`go vet`，记录覆盖率，并要求新增认证 helper 的语句覆盖率至少 80%。证据在 `protocol.json`、`regression.json`、`*.stderr`、`coverage.txt` 和 `auth-coverage.txt`；保留候选 SHA 和容器镜像 digest。builder 还须对补丁新增/修改的 Go 文件及注入测试执行 `gofmt -l`，任何差异应回到候选源码修正后重新验证。

## Builder 镜像构建

在上述验证通过后，使用相同候选目录和同一归档输入进行构建；此命令只构建本地候选镜像，不推送：

```bash
docker buildx build --load --platform linux/amd64 \
  --build-context "cnpg-upstream=$upstream_dir" \
  -t cnpg-observability:verification \
  "$candidate_dir/ops/observability/next/nightingale/cnpg-hardening"
```

固定的 stock runtime 保留上游证书处理、非 root 用户、许可证和 `/manager` 入口。公开 CA 在 manager 启动时加载；更新投射 CA 后必须受控重启 manager，不能把 Secret 投射更新当作运行中 trust pool 热加载。容器协议测试不替代三副本新库初始化、复制、故障切换、真实 API server 源地址及 N9E 登录验收。
