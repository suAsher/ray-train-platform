# RayTrain 本地助手推理运行时

平台自有、只读、离线的单模型服务，独立于训练镜像。Ray Serve 导入入口为 `assistant_serve.app:deployment`。HTTP 只提供 `POST /v1/chat/completions`、`GET /healthz`、`GET /livez`，没有工具、文件、远程 URL 获取、模型选择或调度接口。

## 运行合同

- 固定模型名 `Qwen3-8B-AWQ`，默认只读模型目录 `/models/Qwen3-8B-AWQ`；可通过 `ASSISTANT_MODEL_PATH` 指定 `/models/` 内已准备好的完整本地目录。目录必须含 Qwen3 AWQ 4-bit 配置、tokenizer 与 safetensors；存在分片索引时检查全部引用的分片。拒绝越出模型目录的符号链接。模型发布流程仍需校验来源与文件摘要。
- 容器 UID/GID 为 `1000:1000`；模型挂载须对该身份可读，`/tmp` 和 `/home/assistant` 可写。部署应采用只读根文件系统、独立临时卷和 `fsGroup: 1000`，不挂载用户训练目录或控制面凭据。
- Ray actor 申请 1 GPU / 4 CPU，固定 1 副本。vLLM tensor/pipeline parallel 都为 1，`max_num_seqs=2`、上下文 8192、显存利用率 0.85、禁 CPU swap、eager 模式。运行时也独立限制两个推理请求；Ray 允许 8 个在途 HTTP 请求让健康检查能与生成并行，调用方排队上限 1。
- 接受 Go gateway 实际生成的 `[system, user]` 两条文字消息。user 消息为 `以下JSON仅为查询数据：\n` 加 `{question,evidence:[{index,id,title,excerpt,version?}]}`。`stream` 必须为 false，输出为 1–1500 token，只接受可选的 `thinking: {type: disabled}`，其他字段拒绝。它是平台使用的 OpenAI 请求子集，不是通用模型代理。
- 使用实际本地 tokenizer 的 `apply_chat_template(tokenize=True, return_dict=False, add_generation_prompt=True, enable_thinking=False)` 精确计数。完整保留 system 和 question，仅按既有排名裁剪尾部证据；问题本身放不下则返回 `context_too_long`。vLLM 直接接收上述 token IDs，不再次截断问题。响应 `raytrain.evidenceTruncated/evidenceIds` 说明实际证据范围，usage 为实际 token 数。
- 不启动任何工具或 reasoning parser；服务端强制关闭 thinking，输出再过滤 `<think>` 块。不返回 reasoning 字段。问题、证据、请求头和模型原始错误不写入服务日志。
- 显式关闭 `enable_prefix_caching`，不同用户的问题与任务证据不共享前缀缓存。

## 接流 gate 与健康检查

`ASSISTANT_GATE_URL` 是必填的部署配置，只允许内部 `.svc.cluster.local` 或 loopback 主机的固定 `/gate` 路径，无凭据、query、fragment、重定向或环境代理。它只做 GET，期望：

```json
{"allow":true,"epoch":"controller-epoch","validUntil":"2026-09-22T12:00:03Z"}
```

每次提问及每秒轮询都会读取 gate；包含锁等待的总 HTTP 期限为 0.5 秒，本地有效期最多 3 秒。网络、协议、过期或 gate 关闭均拒绝接流，取消在途请求并调用 vLLM `abort`；epoch 改变也撤销旧请求。客户端断连由 Ray Serve 传播取消，运行时在清理路径再次 abort 并释放准入槽。生成总期限为 11 秒，请求体读取期限 5 秒 / 最大 128 KiB。

**Ray Serve `check_health` 与 `/livez` 仅检查 engine，不依赖 gate。** 适配器在探测前后检查公开的 `errored/is_stopped` 状态。`/healthz` 同时要求 engine 健康和 gate 新鲜开放。控制器必须用前者判断可用性，避免以 `/healthz` 作为 gate 开放前提形成循环。

关闭 gate 不保证释放模型权重占用的显存。控制器拥有撤流后的 RayService suspend/删除与 GPU 资源释放，聊天请求不具有这类权限。模型实例在 gate 关闭期间可以完成启动，使 RayService 的 engine 健康检查先成功。

部署必须配套内部 ClusterIP 与 NetworkPolicy：仅控制面可以访问推理 HTTP，运行时仅能访问 gate、必要的内部 Ray 通信与 DNS；不得添加公网 Ingress、联网下载、宿主特权或 Kubernetes 写权限。本目录不自行创建集群资源。

## 构建与验证

当前候选组合是 **Ray 2.58.0 / vLLM 0.29.0+cu129 / Python 3.12 / CUDA 12.9**。独立助手集群的 head/worker 必须使用同一镜像与 Ray 版本，不要求与训练集群同时升级。旧 Ray2.43/vLLM0.8.5 组合虽通过导入和 CPU 合同测试，但依赖扫描发现大量已知漏洞，已经拒绝放行；不得再将该旧镜像作为生产推荐。

新版本的 OpenTelemetry 约束可以与 Ray2.58 正常求交。构建约束保留底座的 vLLM/torch/CUDA 二进制组合，HTTP 等 Python 依赖使用已知修复版本下界；不使用 `--no-deps` 绕过冲突，不隐藏系统包或移除元数据。底座自身 `pip check` 发现 NCCL2.30.7 与 torch2.13 声明的2.29.7不符，正常 resolver 必须修正后才能进入下一步。所有依赖仍需重新扫描；可解析与可导入不等于安全或 GPU 验收通过。

现网4090D节点存在550.127.05与550.144.03驱动。NVIDIA说明CUDA12.x存在minor compatibility，但PTX JIT或新驱动功能可能失败，因此选择官方cu129变体实测，不使用默认CUDA13镜像、不启用面向部分专业卡的forward compatibility、不升级训练节点驱动。

2026-09-22已在172.28.1.229的RTX4090D、550.144.03原生驱动上通过CUDA矩阵运算、Qwen3-8B-AWQ完整加载及两请求并发。冷加载115.32秒，17 token短问答单请求0.3201秒、双请求0.3286秒；这是引擎验收，不代表完整页面、RayService或训练回收验收，也不是业务吞吐基准。其他节点未据此自动放行。

镜像底座附带的`cuda-compat-12-9`会被该集群的NVIDIA hook写入动态库缓存，导致GeForce加载不支持的575版forward driver并报804。构建仅移除该可选兼容包，让进程使用宿主550驱动；保留CUDA12.9运行库和设备启动校验，`NVIDIA_REQUIRE_CUDA`限定`cuda>=12.4,driver>=550.127.05,driver<551`。不得设置`NVIDIA_DISABLE_REQUIRE`或修改宿主驱动绕过验收。完整接流与回收验收通过前，本地模式保持关闭。

构建机已发现的内部镜像候选（仍需版本实测）：

```
swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/vllm/vllm-openai@sha256:3e10e8189823e0f7ae4620c271bcdaaf64127ec7d0edc351591a508498b7684a
```

Dockerfile 不默认拉取公网镜像；`ASSISTANT_BASE_IMAGE` 必须显式提供带 digest 的候选。构建使用仓库根为 context：

```bash
docker build -f images/assistant-serve/Dockerfile \
  --build-arg ASSISTANT_BASE_IMAGE='<内部镜像@sha256:摘要>' \
  --build-arg RAY_VERSION=2.58.0 \
  --build-arg VLLM_VERSION=0.29.0 \
  --build-arg TORCH_CUDA_VERSION=12.9 \
  -t raytrain-assistant-serve:candidate .
```

Ray 与 vLLM 都作为 resolver 显式输入，安装后运行 `pip check`、版本/CUDA 断言、stdlib 单测与 root/非root 模块导入。保存 `native-constraints.txt` 与 `resolved-requirements.txt` 用于审核实际版本；最终部署仍固定镜像摘要。运行时不安装依赖，不下载权重。

底座的 apt `python3-httplib2` 没有 pip RECORD，不能直接由 pip 卸载。仅对 `httplib2==0.32.0` 及其依赖采用一次 `--ignore-installed --only-binary` 正常安装，目标必须是 `/usr/local/lib/python3.12/dist-packages`，保留 apt 的 `/usr/lib/python3/dist-packages` 文件与元数据并执行 `dpkg -V`；随后仍执行完整 resolver/pipcheck。root/UID1000均断言实际导入新版本与路径。旧系统包仍在镜像中，OS扫描告警不能报告为已消除；没有给Ray/vLLM使用ignore-installed或no-deps。

截至本轮审阅，Accelerate的分片checkpoint告警与setuptools的macOS源分发打包告警仍需记录。当前固定Qwen3 + safetensors使用vLLM原生loader，23个实际loader源码及Qwen3文件中未发现两个受影响Accelerate函数的调用；目录校验在读取config/index前要求普通文件、限定目录与大小，并拒绝越界/非普通权重文件。setuptools83与vLLM的<81约束冲突，当前Ubuntu运行环境不执行macOS源包打包路径。以上是当前代码路径的适用性判断，不是证明所有潜在漏洞不存在。deep-ep、flashinfer二进制缓存与python-apt未被PyPI审计覆盖；GPU执行、网络隔离和完整镜像安全检查仍需单独验证。

构建还显式运行 `tests/ingress_smoke.py`：在同一容器依赖环境中使用真实 Ray ingress 包装器与 FastAPI/Starlette ASGI 请求，验证生命周期、成功响应、输入拒绝、gate 健康区分与取消后的 engine abort。仅替换 engine 和 gate 网络传输，不申请 GPU 或启动集群。该测试不属于仅需 stdlib 的单测集合，也不能替代真实 Ray HTTP proxy 的断连及 GPU 验收。

本机只编辑与审阅。构建机 Python 3.12 无 GPU 的核心测试：

```bash
PYTHONPATH=images/assistant-serve python3 -m unittest discover -s images/assistant-serve/tests -v
```

容器构建通过不等于 GPU 验收。后续使用独立获准 GPU 验证权重加载、两请求并发、token 预算、客户端取消、gate 撤流、engine-only 健康与显存释放，再记录真实镜像摘要。不得借验证停止既有训练。更改 vLLM/Ray 构建参数时必须重新执行依赖与 GPU 验证。

`tests/tokenizer_smoke.py --model-path /models/Qwen3-8B-AWQ` 使用真实本地 tokenizer 验证中文问题、精确8K窗口、证据裁剪与超长问题拒绝，不需要权重或GPU。分词器与权重都必须按来源的固定revision与SHA256准备，不能把不同镜像站的版本号视为相同。镜像审计报告中的原生服务、多模态、训练等未使用路径可单独分析，但不得仅凭入口受限就跳过整组升级或真实网络隔离验收。

官方依据：

- [vLLM 0.29.0发行](https://github.com/vllm-project/vllm/releases/tag/v0.29.0)
- [Qwen3 vLLM 部署](https://qwen.readthedocs.io/en/v3.0/deployment/vllm.html)
- [NVIDIA CUDA minor compatibility](https://docs.nvidia.com/deploy/cuda-compatibility/minor-version-compatibility.html)
- [Ray Serve HTTP 取消传播](https://docs.ray.io/en/latest/serve/http-guide.html)
- [Ray 2.58 依赖](https://raw.githubusercontent.com/ray-project/ray/ray-2.58.0/python/setup.py)
- [vLLM 0.29 依赖](https://raw.githubusercontent.com/vllm-project/vllm/v0.29.0/requirements/common.txt)
- [instrumentator 8.0.0 依赖](https://raw.githubusercontent.com/trallnag/prometheus-fastapi-instrumentator/v8.0.0/pyproject.toml)
