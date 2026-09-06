export const environmentTopic = {
  id: 'custom-environment', group: '入门', title: '缺少环境？自定义训练镜像',
  summary: '先选环境，再提交代码。镜像只保存依赖和平台运行时；代码用 Git、工作区快照或 spk-rayjob 单独提交。修改训练代码不需要重新构建镜像，只有依赖变化才需要。',
  prerequisites: [
    '在训练环境列表核对 Ray、Python、CUDA、PyTorch、MLflow、依赖清单和适用场景。环境信息与检查备注是管理员声明，不等于平台已自动检测。未提供的版本不能按镜像名猜测。',
    '准备项目依赖清单、目标 GPU/训练模式，以及你有权推送的镜像仓库。Harbor 和其他地址均可；集群必须能访问该仓库。私有仓库的拉取凭据由管理员安全配置，不写入代码、镜像或提交说明。',
  ],
  blocks: [
    { kind: 'note', text: '已发布基础镜像：harbor.wellspiking.ai/guofeng.su/raytrain-base:ray2.58.0-py310-torch2.4.1-cu121-20260906。实际 CPU 自检确认 Python 3.10.14、CUDA runtime 12.1、PyTorch 2.4.1、Ray 2.58.0、MLflow skinny 3.14.0、PyArrow 25.0.1。GPU 与多卡训练尚未验证，不是所有模型通用的已验收训练方案。' },
    { kind: 'table', headers: ['组件', '负责什么'], rows: [
      ['PyTorch', '模型、自动求导和优化器更新。'],
      ['Ray Train', '分布式训练编排；启动训练进程并通过适配后的代码接入指标、checkpoint 和恢复。不是替代 PyTorch。'],
      ['Ray Data', '数据读取、变换和分片；训练代码需要消费提供的 shard。'],
      ['KubeRay', '在 Kubernetes 中管理 Ray 集群。operator 版本和镜像内 Ray 版本不是同一个版本号。'],
      ['RayTrain 平台', '账号权限、代码提交、镜像目录、数据与任务管理。选择引擎不会自动改造任意训练代码。'],
    ] },
    { kind: 'steps', items: [
      { title: '先判断是否真的需要新镜像', body: '若目录已有符合版本要求的环境，直接复用。调试环境临时 pip 安装可验证依赖，但不会自动带入正式任务；验证后固定依赖版本。CUDA、Torch 或编译扩展不兼容，应选其他基础环境，不能靠随意升级 Ray 解决。' },
      { title: '选择平台基础环境', body: '使用上方已发布 tag，版本约束在镜像内 /opt/raytrain/constraints.txt，自检命令为 raytrain-selfcheck。其他新版本发布前不能把建议 tag 当可用镜像。通用底座不含 BEVFusion 等项目代码，现有模型专用镜像不是通用底座。' },
      { title: '单独建立环境构建目录', body: '目录只放 Dockerfile、requirements.txt 和 .dockerignore。禁止 COPY . .；不打包训练数据、权重、Git 凭据或 .env。只固定项目额外依赖，不覆盖平台约束中的 Ray、Torch、CUDA 等版本。自定义算子可作为版本化 wheel 安装，并记录代码兼容版本。' },
      { title: '构建并自检', body: '使用 linux/amd64 目标，先运行 pip check 和基础镜像配套 CPU 自检，再推送到你有权限的仓库。CPU 检查不能证明 CUDA 算子、单卡或多卡训练可用；GPU 验证必须另选空闲资源，不能占用或中断别人任务。' },
      { title: '提交管理员登记', body: '提供完整 tag 或 digest 地址、环境版本、额外依赖、适用项目、支持引擎、自检结果与尚未验证项目。团队管理员登记本团队镜像；全平台共享由超级管理员登记。私有仓库另通过安全渠道处理凭据，不粘贴密码到描述。' },
      { title: '独立提交训练代码', body: '登记后刷新训练环境列表并选择新镜像。代码仍通过 Git、快照、代码 ZIP 或 spk-rayjob 提交；先单卡小样本确认导入、loss、产物，再逐级扩卡。Ray Train 与 Ray Data 的入口需要适配，并非安装对应包就自动获得托管能力。' },
    ] },
    { kind: 'code', filename: 'Dockerfile', label: '依赖层 Dockerfile（BASE_IMAGE 必须由管理员提供已发布地址）', lang: 'dockerfile', text: `ARG BASE_IMAGE
FROM \${BASE_IMAGE}
USER root
COPY requirements.txt /tmp/environment-requirements.txt
RUN python -m pip install --no-cache-dir -c /opt/raytrain/constraints.txt -r /tmp/environment-requirements.txt \\
 && python -m pip check \\
 && rm /tmp/environment-requirements.txt
USER ray
RUN raytrain-selfcheck
WORKDIR /home/ray
# 不复制训练脚本，不覆盖平台启动器或 ENTRYPOINT。` },
    { kind: 'code', filename: '.dockerignore', label: '.dockerignore：只允许依赖清单进入构建上下文', text: '**\n!Dockerfile\n!requirements.txt\n!.dockerignore' },
    { kind: 'code', filename: 'requirements.txt', label: 'requirements.txt：只添加固定版本的额外依赖', text: '# 按项目填写 package==version；默认不增加依赖。\n# 不在这里覆盖平台 Ray、Torch、CUDA 或 Python 版本。\n# 无法满足约束时，请管理员提供兼容底座。\n' },
    { kind: 'code', label: '构建与推送（Bash；替换变量再执行）', lang: 'bash', text: `# 在仅含环境文件的目录运行；不要把项目根目录当构建上下文。
BASE_IMAGE='harbor.wellspiking.ai/guofeng.su/raytrain-base:ray2.58.0-py310-torch2.4.1-cu121-20260906'
MY_IMAGE='registry.example.com/my-team/training-env:v1'
# 先按仓库要求安全登录；不要在命令中写明文密码。
docker buildx build --platform linux/amd64 --build-arg BASE_IMAGE="$BASE_IMAGE" -t "$MY_IMAGE" --load . &&
docker run --rm --network none --cap-drop ALL --security-opt no-new-privileges --entrypoint python "$MY_IMAGE" -m pip check &&
# CPU 自检不分配 GPU；前一步失败时不要继续。
docker run --rm --network none --cap-drop ALL --security-opt no-new-privileges --entrypoint raytrain-selfcheck "$MY_IMAGE" &&
docker push "$MY_IMAGE"` },
    { kind: 'note', text: '允许 registry.example.com/team/env:v1，也允许 registry.example.com/team/env@sha256:摘要，不限 harbor.wellspiking.ai。当前平台不会自动把 tag 固定成 digest；tag 被覆盖后，新任务可能用到不同内容。建议使用不覆盖的版本 tag；严格复现使用 digest。运行后可请管理员核对 Pod imageID，不能将登记时的版本说明当作实际检测结果。' },
    { kind: 'warning', title: '环境可用不等于代码已适配', text: '普通 DDP DataLoader 与 Ray Data shard 的分片规则不同。使用 Ray Train 托管需要兼容训练入口；streaming 还需要匹配的数据 schema 和模型适配器。基础镜像包含平台组件，不承诺任意新模型无需改造即可使用全部功能。' },
  ],
  success: ['环境在本团队列表可见，版本与依赖描述符合实际；CPU 自检通过并保留结果。随后单卡验证能够导入依赖、读取样本、完成训练步骤并生成持久化产物；多卡与恢复须另行验证。'],
  troubleshooting: [
    'ImagePullBackOff / ErrImagePull：核对完整地址和 tag 是否存在、集群到仓库的网络与 DNS、证书链及命名空间拉取凭据。你本机能 docker pull 不等于集群能拉取；不要通过关闭 TLS 校验处理证书错误。',
    'ModuleNotFoundError：确认安装到运行入口使用的 Python 环境，且选择了新镜像。undefined symbol / CUDA 错误：核对 Torch、CUDA 和算子 wheel 的 ABI；不要盲目 pip upgrade 全部依赖。',
    'pip check 或版本约束冲突：保留冲突输出，调整额外依赖或请管理员提供另一版本底座；不删除约束绕过。Ray 版本不匹配：登记信息必须与镜像内版本一致，head/worker 也必须一致。',
    '求助时提供任务 ID、发生时间、镜像完整地址、实际 imageID（管理员查询）、Python/框架版本、完整错误和最小复现步骤。不要附加令牌、密码、私有仓库密钥或全量环境变量。',
  ],
  relatedLinks: [{ label: '选择训练环境', to: '/job/create' }, { label: '外部提交', to: '/external-submit' }, { label: '调试环境', to: '/devcenter' }],
}
