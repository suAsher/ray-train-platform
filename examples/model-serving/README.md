# 内网模型推理适配器

先在平台选择固定模型版本、已上传的 Python 适配器 ZIP 和包含依赖的运行镜像。平台通过队列申请一个 Worker；服务就绪后经平台推理入口调用，停止服务会释放资源。训练节点不下载 GitHub 或在线安装依赖，代码与权重不进入训练镜像。

ZIP 至少包含 `serving_sdk.py` 和自己的 `adapter.py`。入口为 `python adapter.py`，例如：

```python
import tempfile
from pathlib import Path
from serving_sdk import ServingClient

client = ServingClient.from_environment()
with tempfile.TemporaryDirectory() as directory:
    weight = client.download_model(Path(directory) / 'model.bin')
    model = load_your_model(weight)  # 使用你自己的模型结构与镜像已有依赖
    def predict(request):
        return {'predictions': model.predict(request['inputs']).tolist()}
    client.serve(predict)
```

适配器必须在 Worker 的 8000 端口提供 `GET /healthz` 和 `POST /invocations`。SDK 在模型加载完成后才开始监听，就绪响应绑定部署 ID 与权重 SHA-256。调用内容是最多 1 MiB 的 JSON 对象，结果是 JSON 对象或数组。模型输入结构和输出含义由适配器定义；平台无法从任意 `.pth` 自动推断网络结构。

SDK 仅使用挂载的当前任务令牌读取固定版本权重，验证完整大小及 SHA-256；不需要个人 PAT。它不跟随下载重定向，不读取个人目录，也不会覆盖已有目标文件。示例 `smoke_adapter.py` 只验证权重传输及 HTTP 协议，不能作为模型精度或真实业务推理的验收。
