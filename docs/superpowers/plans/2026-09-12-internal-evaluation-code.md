# 内网评估代码分发实施计划

> 执行方式：按 release skill 与分工完成实现、独立审阅、构建机验证和生产验收。所有格式化、测试、编译和构建只在构建机执行。

**Goal:** 评估程序先通过平台上传为不可变代码包，训练节点通过内网读取代码并实际回传报告，不再依赖节点访问 GitHub。

**Architecture:** 复用现有本人源码ZIP上传接口。管理员登记共享评估方案时，服务端校验本人READY源码包并复制到独立不可变对象；方案JSON固定代码包ID/SHA/大小。任务专属凭据只可下载该任务冻结的代码包与权重，通用源码工具校验摘要后安全解压。普通成员从模型版本选择方案和数据版本发起真实任务，权限维持当前合同。

**Tech Stack:** Go/Gin/GORM/PostgreSQL JSONB、TOS、Python stdlib、独立 Portal Vue、现有 Kueue/RayJob。

## 基线与范围

后端四端24e64ab，业务镜像release-20260912-11-31e8cdf，Helm222/schema50；Portal dev6258338d。开始已实时核对。只修改backend、source-materializer通用工具及Portal；不改训练镜像、用户配额、调度或既有任务。不新增schema，保留旧Git方案读取与历史记录。

## 代码合同与实现步骤

- [ ] 先添加领域回归测试，验证ZIP快照可用而Git+ZIP混填、错误SHA、超64MiB被拒绝；在构建机基线候选运行并记录RED。
- [ ] `backend/modelevaluation/code.go`定义：

```go
type CodeSnapshot struct {
    ID string `json:"id"`
    SHA256 string `json:"sha256"`
    SizeBytes int64 `json:"sizeBytes"`
    Format string `json:"format"`
}
```

  Evaluator的Code非空时GitURL/GitCommit为空；比较合同包含代码快照身份。`EvaluationCodeStore.Publish(ctx,id,artifact)`校验复制，`Open(ctx,snapshot)`校验读取。
- [ ] `backend/objectstore/evaluation_code.go`使用服务端生成32位hex ID及固定前缀`raytrain-evaluation-code/<id>/source.zip`。受限临时文件流式校验长度/SHA、ZIP路径与symlink，写入禁止覆盖，Close/错误删除临时文件。不开放任意对象键读取、不删除不确定是否已被引用的对象。
- [ ] `backend/api/model_evaluation_evaluators.go`接收sourceArtifactId，按当前tenant+subject取READY包；仍仅SuperAdmin可登记共享方案。返回快照元信息，不暴露原个人对象路径。失败不给可执行方案。
- [ ] `backend/api/model_evaluation_submission.go`冻结新`evaluation-archive`来源和已有数据/镜像/参数；只有评估提交服务能使用该来源。普通API、CLI、Portal任务不可伪造。预检、幂等提交、配额继续走现有链路。
- [ ] `backend/api/model_evaluation_reports.go`新增job专属GET `/jobs/:id/model-evaluation/code`，沿用任务令牌认证和限流；只能返回该评估固定快照，拒绝跨任务、终态/过期凭据及Range。
- [ ] `backend/main_evaluation_runtime.go`从数据库重建可信代码上下文；`backend/k8s/rayjob.go`给源码init容器挂载现有job token，从内部服务下载并校验。不挂方案作者个人存储，不给TOS凭据。
- [ ] `images/source-materializer/`只增加通用Python下载器，拒绝重定向、错误摘要与超限流，再调用既有安全解压器。运行时已有模型SDK继续负责权重和报告。
- [ ] Portal评估方案表单默认上传ZIP，显示进度和摘要，上传未完成不能登记；预检/详情显示代码摘要。旧Git来源仍可读，明确节点网络要求。普通用户无可用方案时说明准备步骤，实际提交仍是preflight+POST。
- [ ] 评估Job详情不允许普通训练重试入口绕开评估记录，提供返回评估入口；普通训练保留原流程。
- [ ] 使用说明维持38篇问题，补ZIP准备和上传、真实评估入口、数据发布/使用权限与PAT自助签发。

## 构建机验证

- [ ] 新领域/对象/API测试：READY和归属、损坏ZIP/路径穿越、SHA/长度、不可覆盖、复制失败、跨任务令牌、客户端伪造来源、旧Git与普通训练兼容。
- [ ] Python下载器与SDK测试：无外网、短读/超长、摘要错误、重定向拒绝、文件收尾；RayJob渲染测试证明只有评估init新增凭据与代码下载。
- [ ] 配置真实隔离PostgreSQL后运行`go test -timeout=20m ./...`，新领域覆盖率至少80%。
- [ ] Portal候选通过官方Dockerfile.lint、生产编译及隔离Chromium上传/登记/预检/提交/报告路径；不使用真实服务替代fixture。
- [ ] 独立安全与兼容性审阅后，先推两main并同步builder，再构建backend及source-materializer；Portal单独推dev。

## 部署与验收

- [ ] 记录现有任务UID/重启数；Helm server dry-run只允许backend镜像及sourceMaterializerImage变更。schema仍为50，不复用旧授权导出新数据库备份。
- [ ] 候选ZIP在构建机从所需两份示例源码打包（不含.git、环境配置、权重、数据），通过Portal本人上传并登记明确协议验收方案。
- [ ] 沿用已说明的专用156字节权重、公开val版本、1Worker/1GPU/4CPU/16Gi，按本次继续修复请求补验；异常只停止本验收任务，成功须真实job终态及报告156字节校验。不得冒充模型精度评估。
- [ ] 验收后停用方案、归档模型、保留审计和报告，确认GPU回收和既有训练未变；报告原生产失败记录仍保留。
- [ ] 分别记录后端与工具摘要、Portal CI/资产/Pod证据、四端源码SHA、测试日志及尚未完成业务模型适配。
