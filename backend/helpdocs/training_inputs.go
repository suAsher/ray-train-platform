package helpdocs

const localSourceCLIGuide = `### 本地源码通过 CLI 提交训练

先按「CLI 如何安装、登录和升级？」安装 spk-rayjob。在「账户与安全」创建当前团队的 PAT，包含 jobs:read、jobs:write、sources:write。以下为 Linux / macOS 终端步骤；不要把 PAT 写进命令。

~~~bash
spk-rayjob login --server https://raytrain.wellspiking.ai --token-stdin
spk-rayjob login-check
spk-rayjob images
~~~

进入包含 train.py 的本地源码根目录；把 REPLACE_REGISTERED_IMAGE 替换为上面返回且有权使用的镜像，按实际代码修改入口。先核对当前目录的 .spk-rayjob.yaml，未显式填写的参数可能继承该文件。下面适合不读外部数据的单卡试跑：

~~~bash
spk-rayjob submit \
  --dir . --name my-training \
  --image 'REPLACE_REGISTERED_IMAGE' \
  --engine ray-ddp --execution-mode single_gpu \
  --entrypoint 'python3 train.py' \
  --workers 1 --gpus-per-worker 1 \
  --cpu-per-worker 4 --memory-per-worker 16Gi \
  --output-path experiments/my-training --watch
~~~

--dir . 会打包上传当前源码目录；不要把数据、权重、密钥放进去，用 .gitignore / .rayignore 排除。代码改动后重新提交，不需要重建依赖镜像。需要输入数据时，先在「数据与存储」确认目录，再添加 --input-space my-files --input-path 'datasets/example'；路径规则见「输入数据和训练输出分别放在哪里？」。

提交返回 Job ID 后查看状态和日志；把 JOB_ID 换为本次返回值：

~~~bash
spk-rayjob status JOB_ID
spk-rayjob logs -f JOB_ID
~~~

RUNNING 只表示运行中；成功结束后还应核对训练日志和产物。结果写到 PLATFORM_OUTPUT_PATH，在任务详情的「训练产物」查看。`

const relativeInputPathGuide = `### 输入数据相对路径怎么填

输入由「空间 + 相对路径」组成。先在「数据与存储 → 我的文件」逐级确认数据目录存在且可读。例如目录是 datasets/example，则提交参数是 --input-space my-files --input-path 'datasets/example'。

不要给 --input-path 填 /mnt/storage/me/datasets/example、/mnt/data/input 或本机的绝对路径。/mnt/storage/me/ 是调试环境里的个人空间挂载；训练的输入位置由平台解析。不要加开头的 /、my-files/ 或用 .. 跳出空间。上传到「我的文件」后并不需要把这份数据再次放进 --dir 源码包。

训练进程读取 PLATFORM_DATASET_PATH，输出写到 PLATFORM_OUTPUT_PATH；所选输入只读。用 Python 在训练进程内读取，避免提交侧提前展开变量：

~~~python
import os
from pathlib import Path

dataset = Path(os.environ["PLATFORM_DATASET_PATH"])
output = Path(os.environ["PLATFORM_OUTPUT_PATH"])
output.mkdir(parents=True, exist_ok=True)
# 在 dataset 下读取实际存在的数据；权重和报告写到 output。
~~~

PLATFORM_DATASET_PATH 已指向你选择的子目录，不要再重复拼接 datasets/example。平台不会生成缺失的 dataset.yaml；代码需要的文件必须真实存在，或在可写输出目录生成后明确传给训练入口。`
