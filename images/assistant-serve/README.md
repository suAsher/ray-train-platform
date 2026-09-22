# RayTrain 本地助手推理运行时

平台自有、只读、离线的单模型服务，独立于训练镜像。Ray Serve 导入入口为 `assistant_serve.app:deployment`。HTTP 只提供 `POST /v1/chat/completions`、`GET /healthz`、`GET /livez`，没有工具、文件、远程 URL 获取、模型选择或调度接口。

## 运行合同

- 固定模型名 `Qwen3-8B-AWQ`，默认只读模型目录 `/models/Qwen3-8B-AWQ`；可通过 `ASSISTANT_MODEL_PATH` 指定 `/models/` 内已准备好的完整本地目录。目录必须含 Qwen3 AWQ 4-bit 配置、tokenizer 与 safetensors；存在分片索引时检查全部引用的分片。拒绝越出模型目录的符号链接。模型发布流程仍需校验来源与文件摘要。
- 容器 UID/GID 为 `1000:1000`；模型挂载须对该身份可读，`/tmp` 和 `/home/assistant` 可写。部署应采用只读根文件系统、独立临时卷和 `fsGroup: 1000`，不挂载用户训练目录或控制面凭据。
- Ray actor 申请 1 GPU / 4 CPU，固定 1 副本。vLLM tensor/pipeline parallel 都为 1，`max_num_seqs=2`、上下文 8192、显存利用率 0.85、禁 CPU swap、eager 模式。运行时也独立限制两个推理请求；Ray 允许 8 个在途 HTTP 请求让健康检查能与生成并行，调用方排队上限 1。
- 接受 Go gateway 实际生成的 `[system, user]` 两条文字消息。user 消息为 `以下JSON仅为查询数据：\n` 加 `{question,evidence:[{index,id,title,excerpt,version?}]}`。`stream` 必须为 false，输出为 1–1500 token，只接受可选的 `thinking: {type: disabled}`，其他字段拒绝。它是平台使用的 OpenAI 请求子集，不是通用模型代理。
- 使用实际本地 tokenizer 的 `apply_chat_template(tokenize=True, add_generation_prompt=True, enable_thinking=False)` 精确计数。完整保留 system 和 question，仅按既有排名裁剪尾部证据；问题本身放不下则返回 `context_too_long`。vLLM 直接接收上述 token IDs，不再次截断问题。响应 `raytrain.evidenceTruncated/evidenceIds` 说明实际证据范围，usage 为实际 token 数。
- 不启动任何工具或 reasoning parser；服务端强制关闭 thinking，输出再过滤 `<think>` 块。不返回 reasoning 字段。问题、证据、请求头和模型原始错误不写入服务日志。

## 接流 gate 与健康检查

`ASSISTANT_GATE_URL` 是必填的部署配置，只允许内部 `.svc.cluster.local` 或 loopback 主机的固定 `/gate` 路径，无凭据、query、fragment、重定向或环境代理。它只做 GET，期望：

```json
{"allow":true,"epoch":"controller-epoch","validUntil":"2026-09-22T12:00:03Z"}
```

每次提问及每秒轮询都会读取 gate；包含锁等待的总 HTTP 期限为 0.5 秒，本地有效期最多 3 秒。网络、协议、过期或 gate 关闭均拒绝接流，取消在途请求并调用 vLLM `abort`；epoch 改变也撤销旧请求。客户端断连由 Ray Serve 传播取消，运行时在清理路径再次 abort 并释放准入槽。生成总期限为 11 秒，请求体读取期限 5 秒 / 最大 128 KiB。

**Ray Serve `check_health` 与 `/livez` 仅检查 engine，不依赖 gate。** `/healthz` 同时要求 engine 健康和 gate 新鲜开放。控制器必须用前者判断可用性，避免以 `/healthz` 作为 gate 开放前提形成循环。

关闭 gate 不保证释放模型权重占用的显存。控制器拥有撤流后的 RayService suspend/删除与 GPU 资源释放，聊天请求不具有这类权限。模型实例在 gate 关闭期间可以完成启动，使 RayService 的 engine 健康检查先成功。

部署必须配套内部 ClusterIP 与 NetworkPolicy：仅控制面可以访问推理 HTTP，运行时仅能访问 gate、必要的内部 Ray 通信与 DNS；不得添加公网 Ingress、联网下载、宿主特权或 Kubernetes 写权限。本目录不自行创建集群资源。

## 构建与验证

候选组合是 **Ray 2.58.0 / vLLM 0.8.5 / Python 3.12 / CUDA 12.4**。官方 vLLM 0.8.5 Dockerfile 使用 CUDA 12.4.1，Qwen 官方说明从 vLLM 0.8.5 支持 Qwen3。此组合尚需正常 pip resolver、容器导入和真实 GPU 验证；不能因目标驱动为 550.127.05 就宣称已兼容。

构建机已发现的内部镜像候选（仍需版本实测）：

```
swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/vllm/vllm-openai@sha256:6cf9808ca8810fc6c3fd0451c2e7784fb224590d81f7db338e7eaf3c02a33d33
```

Dockerfile 不默认拉取公网镜像；`ASSISTANT_BASE_IMAGE` 必须显式提供带 digest 的候选。构建使用仓库根为 context：

```bash
docker build -f images/assistant-serve/Dockerfile \
  --build-arg ASSISTANT_BASE_IMAGE='<内部镜像@sha256:摘要>' \
  --build-arg RAY_VERSION=2.58.0 \
  --build-arg VLLM_VERSION=0.8.5 \
  --build-arg TORCH_CUDA_VERSION=12.4 \
  -t raytrain-assistant-serve:candidate .
```

镜像保留底座相互匹配的 torch/vLLM/CUDA，不使用 `--no-deps` 绕过冲突；安装 Ray 后运行 `pip check`、版本/CUDA 断言、stdlib 单测与模块导入。运行时不安装依赖，不下载权重。

本机只编辑与审阅。构建机 Python 3.12 无 GPU 的核心测试：

```bash
PYTHONPATH=images/assistant-serve python3 -m unittest discover -s images/assistant-serve/tests -v
```

容器构建通过不等于 GPU 验收。后续使用独立获准 GPU 验证权重加载、两请求并发、token 预算、客户端取消、gate 撤流、engine-only 健康与显存释放，再记录真实镜像摘要。不得借验证停止既有训练。更改 vLLM/Ray 构建参数时必须重新执行依赖与 GPU 验证。

官方依据：

- [vLLM 0.8.5 Dockerfile](https://raw.githubusercontent.com/vllm-project/vllm/v0.8.5/docker/Dockerfile)
- [Qwen3 vLLM 部署](https://qwen.readthedocs.io/en/v3.0/deployment/vllm.html)
- [vLLM 0.8.5 异步引擎](https://docs.vllm.ai/en/v0.8.5/api/engine/async_llm_engine.html)
- [Ray Serve HTTP 取消传播](https://docs.ray.io/en/latest/serve/http-guide.html)
