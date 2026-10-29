# 开发

## 源码与构建

后端使用 `backend/go.mod` 指定的 Go 版本；前端使用 Node.js 20 和锁定的 npm 依赖。CLI 与帮助命令的合同测试需要 Bash 和 Zsh。根 Dockerfile 构建前端并嵌入 Go 二进制，因此生产只发布一份应用镜像。

在具备依赖与资源的开发机或 CI 中执行：

```bash
cd frontend
npm ci
npm test
npm run build
cd ../backend
gofmt -l .
go vet ./...
go test -p 1 -timeout=25m ./...
```

`gofmt -l` 输出非空时需格式化并审阅差异。完整嵌入式 UI 构建使用根目录 `docker build -t ray-train-platform:dev .`；单独 Go 编译不会自动构建 Vue。

前端热更新使用 `npm run dev`，Vite 的 `/api` 与 `/ws` 代理指向本地8080。开发后端需要自己的数据库和认证配置；不要连接生产数据库。配置入口见 `backend/config/`，不要以匿名模式替代需要验证的身份流程。

## 数据库回归

使用独立、可丢弃的 PostgreSQL 测试库：

```bash
export POSTGRES_TEST_DSN='postgres://testuser:testpassword@127.0.0.1:5432/raytrain_test?sslmode=disable'
cd backend
go test -p 1 -timeout=25m ./...
```

以上密码仅为测试占位。测试库不得包含真实数据。使用 `-p 1` 串行执行测试包，避免共享测试库的迁移锁相互干扰；这不改变生产迁移锁行为。未设置 DSN 时跳过的 PostgreSQL 测试不能视为通过。新增迁移保留5秒锁超时和60秒语句超时，验证空库、重复运行及已有数据升级。SQLite不代替PostgreSQL兼容性验证。

## 其他检查

```bash
bash scripts/test-delivery-render.sh
bash scripts/test-ray-runtime-images.sh --contract-only
git diff --check
```

镜像脚本合同检查不等于真实训练通过。GPU运行验收需要获准的测试集群和独立测试资源；不能停止其他用户的任务。`build-image.sh --help` 给出支持的镜像目标；默认只构建平台，显式选择辅助或训练镜像。

## GitHub CI

[工作流](../.github/workflows/ci.yml)在 Pull Request 和 main 推送时检查 Go 格式、vet、完整回归和真实 PostgreSQL，执行前端测试与构建、Helm渲染、运行时合同，并构建统一镜像。它不访问既有集群。版本tag的发布作业在门禁通过后向该GitHub仓库对应GHCR路径发布统一应用镜像。

统一镜像在 GitHub runner 临时创建的 kind 集群中进行全新安装检查：独立数据库、嵌入式页面、本地登录、API 鉴权，以及重启后的会话持久化。该集群没有 GPU，也不安装训练所需的 Kueue/KubeRay；它验证控制面安装，不代表训练调度、CSI 或 GPU 兼容性已通过。检查结束后销毁临时集群。

先在候选分支验证，再合并主分支。以实际workflow结果为准；配置存在不能证明CI已通过或镜像已经发布。辅助/训练镜像仍按部署文档自行构建和验证。
