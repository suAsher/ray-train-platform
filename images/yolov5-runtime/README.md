# YOLOv5 训练环境

团队：`yolo`（yolo模型）。镜像只包含依赖及平台启动器，不包含 YOLOv5 源码、数据或预训练权重。

## 用户如何使用

1. 在训练任务中选择本团队的 **YOLOv5 · PyTorch 2.4.1 / CUDA 12.1** 环境。
2. 上传自己的 YOLOv5 源码 ZIP，或选择平台支持的源码来源。训练节点不能联网克隆 GitHub。
3. 准备并挂载数据集；`data.yaml` 中的路径要指向容器内实际挂载路径。
4. 从随机权重开始可用 `python train.py --weights '' --cfg models/yolov5s.yaml --data /实际挂载路径/data.yaml --epochs 100 --batch-size 8 --project "$PLATFORM_OUTPUT_PATH" --name train`。`PLATFORM_OUTPUT_PATH` 若未设置，使用任务详情显示的持久化输出路径。
5. 使用预训练或续训权重时，先上传/挂载本地 `.pt`，用 `--weights /挂载路径/model.pt` 或 `--resume /挂载路径/last.pt`。不能依赖默认权重名称触发外网下载。

`--batch-size` 按模型、图片尺寸和 GPU 显存调整。此镜像补齐环境，不保证业务任务不会 OOM。原生 YOLOv5 使用 `ray-ddp` 执行引擎；多进程训练需按平台 DDP 启动协议准备命令，不能直接宣称兼容 managed Ray Train。安装 MLflow 客户端不代表训练代码会自动上报指标。

## 版本及验证

- 基础镜像固定为 RayTrain Base `sha256:3ec73cf863847a42bcac035309259ad9d08fd74561384cc8476020c62264d6e2`。
- Python 3.10.14 / Ray 2.58.0 / PyTorch 2.4.1 / torchvision 0.19.1 / CUDA 12.1。
- 依赖见 `requirements.txt`；固定 NumPy 1.26.4、OpenCV 4.10，避免 NumPy ABI 升级；固定 filelock 3.19.1，保留 Ray virtualenv 兼容性。
- 离线绘图使用 DejaVu/Noto 系统字体，避免上游下载 Arial。默认关闭 Ultralytics 自动安装与联网检查。
- 官方 YOLOv5 源码验证提交：`402e17ddf820996f51a191cbb798376e1144f069`。不保证所有历史 fork 的额外依赖。
- 已在构建机 `--network none`、4 CPU/8 GiB 容器完成随机数据单轮训练、保存权重和重新加载验证；`pip check`、基础运行时自检通过。
- GPU/CUDA 驱动、NCCL 和多机 DDP 是独立验证项，不能用 CPU 验收替代。

## 构建与复验

仅在构建机使用显式目标（不在 `all` 中）：

```bash
BUILD_TARGETS=yolov5-runtime IMAGE_TAG=<新版本> PUSH_IMAGE=false bash build-image.sh
```

将上述提交的官方源码放在构建机独立目录，不复制到镜像。复验：

```bash
docker run --rm --network none --cpus 4 --memory 8g --shm-size 1g \
  -v /绝对路径/yolov5:/test:ro \
  -v "$PWD/images/yolov5-runtime/smoke.py:/smoke.py:ro" \
  --entrypoint python3 <候选镜像> /smoke.py /test /tmp/smoke cpu
```

通过后推送镜像并按不可变摘要登记，`kind=training`、`targetTenantId=yolo`、`shared=false`、`isDefault=false`。不需要重部署前后端。
