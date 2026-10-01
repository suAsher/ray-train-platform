package helpdocs

const debugSystemPackagesGuide = `#### 临时安装系统包（sudo / apt）

平台升级后，使用已更新调试镜像新建的调试环境允许通过 sudo 安装系统包，默认终端仍以普通用户运行。已有调试实例不会自动获得这项能力。在调试终端执行：

~~~bash
# 将 PACKAGE_NAME 替换为所需的系统包名
sudo apt-get update && sudo apt-get install --no-install-recommends PACKAGE_NAME
~~~

旧镜像可能没有 sudo；如果出现 sudo: command not found，先保存所需文件和受管 Python 依赖版本，再自行新建使用已更新镜像的调试环境；不希望重建时可请管理员准备所需镜像。

系统包不会进入保存的训练环境；它们只在当前调试容器内临时可用，重建后不会保留，需要重新安装。保存训练环境仍只从固定 Base 和受管 Python 依赖重建，不是整个容器的快照。正式训练需要这些系统包时，应将它们写入专用训练镜像的 Dockerfile 并按固定摘要登记。

/workspace 和 /mnt/storage/me 中的个人文件仍由个人持久存储保留。写代码、解压和管理这些文件时使用普通用户，避免用 sudo 在个人持久目录中创建 root 所有者的文件，否则普通用户可能无法继续编辑。

Python 依赖仍使用 /opt/raytrain/environment/bin/python，通过 python -m pip 安装并按保存训练环境流程验证、保存；不要用 sudo pip 改写系统 Python 或绕过受管环境。`

const environmentImageGuide = `### 在 ~/ 执行安装，能保存吗？

**当前目录不决定安装位置，实际使用的 Python 和安装方式才决定。** 在 ~/、/workspace 或其他目录执行 python -m pip，只要使用 /opt/raytrain/environment/bin/python、没有改用其他安装目录，并且依赖满足下面的 wheel 条件，就可以保存。反过来，在 ~/venv、~/conda 或 ~/.local 里自行安装的环境不会因此进入训练镜像。

这里的“保存训练环境”，是通过「保存当前调试环境」从固定 Base 和受管 Python 依赖重建一个训练镜像；它不是 docker commit，不会把整个容器或整个 home 复制过去。源码与数据需要分别准备；只改 train.py、配置或模型结构时，重新提交源码即可。

### 1. 选择支持保存的 Base 调试镜像

在「交互式调试」选择支持保存环境的 Base 调试镜像，创建自己的工作区，等工作区运行后打开 VS Code 或 Jupyter。原有 Base 训练镜像仍可继续使用，已有其他类型的工作区不会自动转换为可保存环境。

### 2. 确认 Python，再安装并测试

在 VS Code 终端先检查：

~~~bash
python -c "import sys; print(sys.executable); print(sys.prefix)"
python -m pip --version
~~~

前两项应分别显示 /opt/raytrain/environment/bin/python 和 /opt/raytrain/environment；pip 的路径也应位于该环境内。若不是，先退出自行激活的 Conda/venv，重新打开终端并检查，或显式使用 /opt/raytrain/environment/bin/python。不要仅凭终端显示的目录、环境名称或 pip 命令判断。

Jupyter 选择「RayTrain Environment」内核，在单元格执行 import sys; print(sys.executable) 确认同一路径。安装和调试必须使用同一个 Python，避免终端装好了、Notebook 却在另一个环境运行。

将项目需要新增的依赖写入 requirements.txt，每个包使用固定版本；在该文件所在目录执行：

~~~bash
python -m pip install --only-binary=:all: -r requirements.txt
python -m pip check
~~~

如果 requirements.txt 在别处，给 -r 提供它的实际路径即可，不必为了保存环境而切换到特定目录。新增 Base 调试镜像默认使用火山内网源 https://mirrors.ivolces.com/pypi/simple/。保存时平台还需从配置的镜像源取得完全匹配的 wheel；仅在本机或私有地址可用的安装包不能通过此入口保存。

不要用 pip install --user、--target、--prefix 改变安装目录，也不要用 pip install -e、Git URL 或本地包路径安装需要保存的依赖。原 Base 的 Ray、PyTorch、CUDA 等核心依赖不能通过此入口替换。需要这些改动时，使用本文后续的手工 Docker 构建与登记指南准备专用镜像。

安装完成后，用同一 Python 验证 import、读取一个小样本并运行项目的最小训练。记录所用源码版本；确认依赖可用后再保存。

` + debugSystemPackagesGuide + `

### 3. 保存到自己有写权限的 Harbor 项目

1. 保持工作区运行，点击「保存当前调试环境」，填写环境名称和说明，选择「仅本人」或「当前团队」可用。此范围控制平台镜像目录与训练提交权限；Harbor 仓库自身的可见范围由 Harbor 项目权限决定。
2. 选择目标镜像仓库，输入自己的 Harbor 用户名及对应凭据：

| 目标镜像仓库 | 使用的凭据 |
| --- | --- |
| harbor.wellspiking.ai | 个人资料中的 CLI Secret |
| harbor.qomolo.com | 账号密码 |

这两种凭据都不是平台 PAT。不要把凭据写入源码、终端命令或截图；切换仓库后需要重新授权。

3. 选择自己有写权限的项目，填写镜像名称。完整地址为 <所选仓库>/<所选项目>/<镜像名称>:<自动版本>；项目和镜像名称由你填写，没有固定到某个人的项目。
4. 点击「验证目标写权限」，通过后点击「构建并推送」。能登录 Harbor 或能看到项目，不代表有推送权限；验证失败时请项目管理员检查权限。
5. 排队和捕获期间请勿继续安装、卸载或修改依赖，平台以实际捕获时的依赖为准。平台使用集群 CPU 和临时磁盘重建、校验环境，再用本次授权推送；每次保存产生一个新版本，标签由平台生成。

### 4. 等待 READY，再用于训练

等待状态变为“可用于训练”（READY），再点击「用于训练」。页面会带入固定摘要镜像，继续选择源码、数据和资源后提交。

READY 表示镜像构建与拉取检查通过，不包含业务模型或 GPU 兼容性验收。先用 1 Worker、1 GPU 跑自己的小样本训练，再扩大规模。停止或重建调试工作区可能丢失尚未保存的依赖，应先等待版本 READY；已有任务仍使用提交时的镜像，不会自动换成新版本。

### 哪些进入镜像，哪些只在存储中保留

“文件还在”与“训练镜像包含它”是两件事。下面的持久化说明以平台已挂载个人存储为前提；普通 home 不是个人持久空间。

| 内容或位置 | 会进入保存的训练镜像吗？ | 停止或重建调试环境后 |
| --- | --- | --- |
| /opt/raytrain/environment 中新增的 wheel 依赖 | 可以：需能从配置的镜像源重建，并通过文件校验 | 未保存的安装可能丢失；等待 READY 后可在训练中选择该版本 |
| ~/venv、~/conda、~/.local 等自建环境 | 不进入；保存不会扫描或打包这些环境 | 普通 home 是临时目录，可能丢失；即使另存到持久空间，也不等于进入镜像 |
| 普通 home（通常为 /home/ray）中的代码、配置和文件 | 不进入 | 临时目录，可能丢失；需要保留的项目放到 /workspace |
| /workspace 中的项目和文件 | 不进入；训练源码需另建代码快照、上传 ZIP 或固定 Git commit | 由个人持久存储保留，和环境镜像版本分别管理 |
| /mnt/storage/me 中的数据、权重和结果 | 不进入；训练时通过数据与输出路径使用 | 由个人持久存储保留，和环境镜像版本分别管理 |
| APT/系统软件与容器其他目录 | 不进入；此入口不捕获系统层改动 | 系统包重建后不会保留，需重新安装；训练需要时写入专用镜像的 Dockerfile 并重新构建 |

如果你使用 ~/anaconda3/bin/python，或在 ~/anaconda3/envs/ 下新建 Conda 环境，后续安装不属于可保存的受管环境。原 Base 自带的 /home/ray/anaconda3 由基础镜像提供，不能通过自动保存来替换它；上表的“不进入”指你后来新增或修改的内容。

受管环境中的可编辑安装、直接 URL 或本地安装、被手动修改的依赖文件会被拒绝；无匹配 wheel 或重建校验不通过也会失败。系统层或自建环境中的改动不在捕获范围内，保存成功不代表这些改动已包含在镜像中。工作区文件快照也不能代替环境版本。

### 保存后，浏览器怎样提交训练

环境版本只保存依赖，不包含训练代码。点击「用于训练」带入镜像后，还要选择一个代码版本：

- 代码在本机：在「数据与存储 → 我的工作区」上传源码 ZIP 并创建任务；ZIP 根目录放启动脚本，随后在任务表单选择刚保存的环境。
- 代码已在工作区：进入自己的项目子目录，点击「创建训练代码版本」，用生成的快照创建任务。先确认当前目录；开发中心的快捷快照覆盖整个工作区根目录，不适合混放多个项目的工作区。
- 代码在 Git：在创建任务页输入平台能访问的仓库地址及分支/标签，点击解析并确认固定 commit。修改地址或分支后需要重新解析。训练节点不需要直接访问 GitHub；平台准备源码包后从内网提供给任务。

接着填写相对于源码根目录的启动命令，选择数据位置和资源。先用 1 Worker、1 GPU 跑小样本。数据目录通过 PLATFORM_DATASET_PATH 读取；checkpoint、报告等保存到 PLATFORM_OUTPUT_PATH。不要把调试工作区的绝对路径直接当作训练源码路径，也不要把数据、权重、虚拟环境或凭据打进代码包。

任务成功后，在任务详情查看日志与训练产物；在环境版本上保存成功不代表业务代码已完成训练验收。

### 同一环境怎样通过命令行提交

在「账户与安全」创建含 jobs:read、jobs:write、sources:write 的个人访问令牌，在自己的电脑安装平台 CLI。训练令牌与推送镜像用的 Harbor CLI Secret 或密码是不同凭据。

~~~bash
# 登录会交互提示输入平台 PAT；不要把令牌直接写进命令历史。
spk-rayjob login --server https://raytrain.wellspiking.ai --token-stdin
spk-rayjob images

# 进入包含 train.py 的本地源码目录，复制保存版本的完整镜像地址（含所选仓库和摘要）。
spk-rayjob submit \
  --dir . \
  --name my-training \
  --image '<所选仓库>/<项目>/<镜像>@sha256:<保存版本的摘要>' \
  --engine ray-ddp \
  --execution-mode single_gpu \
  --entrypoint 'python train.py' \
  --workers 1 --gpus-per-worker 1 \
  --cpu-per-worker 4 --memory-per-worker 16Gi \
  --output-path experiments/my-training \
  --watch
~~~

上面的命令适用于不读取外部数据的最小训练。如果需要个人数据，加 --input-space my-files --input-path '<个人数据相对路径>'；不要填写 /mnt/storage/me/... 这样的容器绝对路径。数据版本、托管 Ray Train、多卡及续训请使用对应使用说明，不能只把单卡示例的 Worker 数量调大。

--dir 会上传本地源码，不会自动取得网页选中的 Git commit 或工作区快照。要比较两种入口，请先准备相同版本的源码。保存环境为「仅本人」时只有本人能在当前团队选择并提交；「当前团队」允许当前团队成员使用，浏览器、CLI 和原生 Ray 入口执行相同的镜像准入检查。

### Ray Dashboard 什么时候能打开

在自己的任务详情点击「Ray Dashboard」。需要任务正在运行且 Ray Head 已就绪；排队或初始化阶段请稍后重试。入口会建立短期的任务访问会话，不要复制跳转中的短期访问票据给他人。任务结束并回收集群后，Dashboard 不再可用，历史日志、产物和实验指标继续在平台查看。

### 构建失败、取消或凭据过期怎么办

- 查看环境版本详情中的失败阶段和提示。凭据失效或项目无写权限时，在原仓库重新授权后重试同一条记录；重试保持原仓库、项目、镜像名和已捕获材料。需要换目标时创建新版本。
- 已推送但拉取验证失败时，镜像还不能用于平台训练。公开镜像要求训练节点能访问仓库；私有镜像需要平台配置该仓库的只读拉取机器人并覆盖相应项目。请联系平台管理员检查拉取权限；平台不会把你的推送密码长期保存为训练拉取凭据。
- 找不到所需 wheel 或文件校验失败时，按提示修正安装方式并重新测试，再创建新版本。若依赖只能从本地、Git 或私有源安装，或必须修改系统库，按后续手工 Docker 指南构建专用镜像并登记。
- 取消仅停止本次构建，不停止工作区或训练任务。已推送到 Harbor 的镜像不会因取消而被自动删除。
- 平台授权最长保留 24 小时，操作结束后清理本次推送凭据。清理平台副本不等于撤销 Harbor 本身的 CLI Secret 或修改账号密码；如需撤销，前往 Harbor 处理。

环境构建使用独立的集群 CPU 和临时磁盘；训练与 GPU 调试仍按当前团队配额提交。构建镜像不会自动启动训练，也不会更改已有任务使用的镜像。`
