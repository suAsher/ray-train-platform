<template>
  <div class="max-w-5xl space-y-6">
    <section class="rounded-2xl border border-blue-500/20 bg-gradient-to-br from-blue-950/40 to-[#131826] p-7 shadow-xl">
      <p class="text-[11px] font-semibold uppercase tracking-[0.2em] text-blue-400">External Development</p>
      <h3 class="mt-2 text-2xl font-bold text-white">从集群外提交训练</h3>
      <p class="mt-3 max-w-3xl text-sm leading-6 text-slate-300">
        在自己的机器上改完代码后，可选择 <code>spk-rayjob</code> 或原生 <code>ray job submit</code>
        将当前目录提交为新的训练任务。
      </p>
      <div class="mt-5 grid gap-4 md:grid-cols-2">
        <div class="rounded-xl border border-blue-500/30 bg-blue-500/10 p-4">
          <p class="font-semibold text-blue-200">方式一：spk-rayjob</p>
          <p class="mt-2 text-xs leading-5 text-slate-300">适合日常训练：登录平台账号，配置一次项目默认值，之后改完代码直接提交。</p>
        </div>
        <div class="rounded-xl border border-violet-500/30 bg-violet-500/10 p-4">
          <p class="font-semibold text-violet-200">方式二：原生 Ray CLI</p>
          <p class="mt-2 text-xs leading-5 text-slate-300">适合已有 Ray 自动化脚本：配置平台 PAT，使用 <code>--working-dir .</code> 提交当前代码。</p>
        </div>
      </div>
    </section>

    <section class="grid gap-5 lg:grid-cols-4">
      <article v-for="step in steps" :key="step.number" class="panel panel-hover p-5">
        <p class="font-mono text-xs text-blue-400">0{{ step.number }}</p>
        <h4 class="mt-2 font-bold text-white">{{ step.title }}</h4>
        <p class="mt-2 text-sm leading-6 text-slate-400">{{ step.description }}</p>
      </article>
    </section>

    <section class="panel space-y-5 p-6">
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h4 class="font-bold text-white">方式一：spk-rayjob</h4>
          <p class="mt-1 text-xs leading-5 text-slate-400">
            先安装客户端，然后按页面中的登录、初始化和提交命令操作。选择你的系统：
          </p>
        </div>
      </div>

      <el-radio-group v-model="platform" size="small">
        <el-radio-button label="linux">Linux</el-radio-button>
        <el-radio-button label="macos">macOS (Apple Silicon)</el-radio-button>
        <el-radio-button label="windows">Windows</el-radio-button>
      </el-radio-group>

      <CopyBlock :text="installCommands[platform]" label="安装命令" />
      <div class="flex flex-wrap gap-3">
        <a :href="binaries[platform]" class="text-xs text-blue-400 hover:text-blue-300" download>直接下载二进制</a>
        <a :href="checksums" class="text-xs text-blue-400 hover:text-blue-300">SHA256SUMS</a>
      </div>
      <p class="text-[11px] leading-5 text-slate-500">
        单文件、无依赖，不需要 Python 或 Docker。此客户端原名 rayctl，旧下载地址仍可用，但请改用新命令名 <code>spk-rayjob</code>。
      </p>
    </section>

    <section class="panel space-y-5 p-6">
      <div>
        <h4 class="font-bold text-white">2. 使用平台账号登录</h4>
        <p class="mt-1 text-xs leading-5 text-slate-400">
          与网页登录同一个账号。密码仅从标准输入换成短期会话，不进入 Shell 历史，也不会明文保存；会话过期后重新登录即可。
        </p>
      </div>
      <CopyBlock :text="loginCommand" label="登录命令" />
      <p class="text-xs leading-5 text-slate-500">
        自动化脚本或企业 SSO 账号，请在“账户与安全 → 个人访问令牌”创建 PAT，再用下面这种方式登录。
      </p>
      <CopyBlock :text="tokenLoginCommand" label="使用 PAT 登录" />
    </section>

    <section class="panel space-y-5 p-6">
      <div>
        <h4 class="font-bold text-white">3. 在代码目录写一次提交默认值</h4>
        <p class="mt-1 text-xs leading-5 text-slate-400">
          <code>init</code> 会生成 <code>.spk-rayjob.yaml</code>。把它提交进仓库，团队成员的提交形状就一致了；之后 <code>submit</code> 不再需要任何参数。
        </p>
      </div>
      <CopyBlock :text="initCommand" label="初始化命令" />
      <div class="rounded-xl border border-slate-800 bg-slate-950/60 p-4">
        <p class="text-xs font-semibold text-slate-300">.spk-rayjob.yaml 示例</p>
        <CopyBlock class="mt-2" :text="projectFileExample" />
      </div>
      <el-alert type="warning" :closable="false" show-icon>
        <template #title>
          entrypoint 里不要写 torchrun、torch.distributed.launch 或 torchpack dist-run。
        </template>
        平台会根据 <code>executionMode</code> 与 GPU 数自动启动 torchrun；重复包装会导致 rendezvous 失败。
      </el-alert>
    </section>

    <section class="panel space-y-5 p-6">
      <div>
        <h4 class="font-bold text-white">4. 日常循环：改代码即提交</h4>
        <p class="mt-1 text-xs leading-5 text-slate-400">
          代码按 <code>.gitignore</code> 与 <code>.rayignore</code> 打包成不可变版本。结果写入“我的训练结果”，可在网页浏览。
        </p>
      </div>
      <CopyBlock :text="dailyLoopCommand" label="日常循环" />
    </section>

    <section class="panel space-y-5 p-6">
      <div>
        <h4 class="font-bold text-white">方式二：原生 Ray CLI</h4>
        <p class="mt-1 text-xs leading-5 text-slate-400">
          在“账户与安全 → 个人访问令牌”创建 PAT，安装与平台匹配的 Ray 2.35，然后在代码目录执行：
        </p>
      </div>
      <CopyBlock :text="nativeRayCommand" label="原生 Ray 提交" />
      <p class="text-xs leading-5 text-slate-500">
        默认为 1×1 GPU。多机多卡、数据目录选择和断点续训优先使用 <code>spk-rayjob</code>；完整参数见 <code>spk-rayjob --help</code>。
      </p>
    </section>

    <section class="panel space-y-4 p-6">
      <h4 class="font-bold text-white">命令速查</h4>
      <el-table :data="commandReference" class="!bg-transparent text-xs">
        <el-table-column prop="command" label="命令" min-width="320">
          <template #default="scope">
            <div class="flex items-center gap-2">
              <code class="min-w-0 flex-1 truncate font-mono text-blue-300">{{ scope.row.command }}</code>
              <el-button link size="small" icon="DocumentCopy" @click="copyText(scope.row.command)">复制</el-button>
            </div>
          </template>
        </el-table-column>
        <el-table-column prop="purpose" label="用途" min-width="260" />
      </el-table>
    </section>

  </div>
</template>

<script setup>
import { ElMessage } from 'element-plus'

import { ref } from 'vue'

import CopyBlock from '../../components/CopyBlock.vue'
import { copyToClipboard } from '../../clipboard'

const platformURL = window.location.origin
const checksums = `${platformURL}/downloads/spk-rayjob/SHA256SUMS`

const binaries = {
  linux: `${platformURL}/downloads/spk-rayjob/spk-rayjob-linux-amd64`,
  macos: `${platformURL}/downloads/spk-rayjob/spk-rayjob-darwin-arm64`,
  windows: `${platformURL}/downloads/spk-rayjob/spk-rayjob-windows-amd64.exe`,
}

const platform = ref('linux')

// Each snippet verifies the published checksum before installing. The commands
// differ per OS because the tooling does: sha256sum vs shasum vs Get-FileHash.
const installCommands = {
  linux: `mkdir -p ~/.local/bin ~/.cache/spk-rayjob
curl -fL ${binaries.linux} -o ~/.cache/spk-rayjob/spk-rayjob
curl -fL ${checksums} -o ~/.cache/spk-rayjob/SHA256SUMS
grep 'spk-rayjob-linux-amd64$' ~/.cache/spk-rayjob/SHA256SUMS | awk '{print $1}' > /tmp/spk.want
sha256sum ~/.cache/spk-rayjob/spk-rayjob | awk '{print $1}' > /tmp/spk.got
diff -q /tmp/spk.want /tmp/spk.got && echo "校验通过"
install -m 0755 ~/.cache/spk-rayjob/spk-rayjob ~/.local/bin/spk-rayjob
export PATH="$HOME/.local/bin:$PATH"     # 建议写进 ~/.bashrc
spk-rayjob version`,
  macos: `mkdir -p ~/.local/bin ~/.cache/spk-rayjob
curl -fL ${binaries.macos} -o ~/.cache/spk-rayjob/spk-rayjob
curl -fL ${checksums} -o ~/.cache/spk-rayjob/SHA256SUMS
grep 'spk-rayjob-darwin-arm64$' ~/.cache/spk-rayjob/SHA256SUMS | awk '{print $1}'
shasum -a 256 ~/.cache/spk-rayjob/spk-rayjob | awk '{print $1}'   # 两行应一致
install -m 0755 ~/.cache/spk-rayjob/spk-rayjob ~/.local/bin/spk-rayjob
xattr -d com.apple.quarantine ~/.local/bin/spk-rayjob 2>/dev/null || true
export PATH="$HOME/.local/bin:$PATH"     # 建议写进 ~/.zshrc
spk-rayjob version`,
  windows: `# PowerShell
New-Item -ItemType Directory -Force "$env:USERPROFILE\\.spk-rayjob" | Out-Null
Invoke-WebRequest -Uri "${binaries.windows}" -OutFile "$env:USERPROFILE\\.spk-rayjob\\spk-rayjob.exe"
Invoke-WebRequest -Uri "${checksums}" -OutFile "$env:USERPROFILE\\.spk-rayjob\\SHA256SUMS"
(Get-FileHash "$env:USERPROFILE\\.spk-rayjob\\spk-rayjob.exe" -Algorithm SHA256).Hash.ToLower()
Select-String 'spk-rayjob-windows-amd64.exe$' "$env:USERPROFILE\\.spk-rayjob\\SHA256SUMS"   # 两值应一致
$env:PATH += ";$env:USERPROFILE\\.spk-rayjob"
spk-rayjob version`,
}

const loginCommand = `read -rp '平台用户名: ' SPK_USERNAME
read -rs SPK_PASSWORD && echo
printf '%s\\n' "$SPK_PASSWORD" | spk-rayjob login --server ${platformURL} \\
  --username "$SPK_USERNAME" --password-stdin
unset SPK_USERNAME SPK_PASSWORD`

const tokenLoginCommand = `read -rs SPK_TOKEN && echo
printf '%s\\n' "$SPK_TOKEN" | spk-rayjob login --server ${platformURL} --token-stdin
unset SPK_TOKEN`

const initCommand = `cd ~/my-training-project
spk-rayjob init --name my-training --gpus-per-worker 8
$EDITOR .spk-rayjob.yaml   # 填写 image 与 entrypoint`

const projectFileExample = `name: bevfusion-lidar
image: harbor.wellspiking.ai/<项目>/<镜像>@sha256:<digest>
entrypoint: python tools/westwell_train.py configs/lidar.yaml --launcher pytorch
workers: 1
gpusPerWorker: 8
cpuPerWorker: 32
memoryPerWorker: 128Gi
executionMode: torchrun      # single_gpu | torchrun | ray_train
input:
  space: public
  path: bevfusion/2026-08-0429
output:
  path: bevfusion-lidar`

const dailyLoopCommand = `vim tools/westwell_train.py      # 改代码
spk-rayjob submit --watch        # 提交并等待结束
spk-rayjob logs -f <JOB ID>      # 跟随日志
spk-rayjob jobs --state RUNNING  # 看在跑什么`

const nativeRayCommand = `python3 -m pip install "ray[default]==2.35.0"
export RAY_ADDRESS="${platformURL}/ray"
export RAY_JOB_HEADERS='{"Authorization":"Bearer <平台 PAT>"}'

cd ~/my-training-project
ray job submit --address "$RAY_ADDRESS" --working-dir . -- python3 train.py`

const steps = [
  { number: 1, title: '安装客户端', description: '单文件二进制，校验发布摘要后放进 PATH。' },
  { number: 2, title: '登录平台账号', description: '与网页同一账号，命令行只保存短期会话。' },
  { number: 3, title: '写一次默认值', description: 'init 生成并提交 .spk-rayjob.yaml，团队共用同一提交形状。' },
  { number: 4, title: '改完直接提交', description: '之后 submit 不带参数，平台创建新的不可变代码版本。' },
]

const commandReference = [
  { command: 'spk-rayjob submit --watch', purpose: '提交当前目录并阻塞显示排队 → 运行 → 结束' },
  { command: 'spk-rayjob submit --resume-from-job <ID>', purpose: '把上一次运行的结果目录作为只读 checkpoint 续训' },
  { command: 'spk-rayjob jobs --state FAILED', purpose: '按状态列出任务' },
  { command: 'spk-rayjob status <ID>', purpose: '查看单个任务的状态、规模与结果目录' },
  { command: 'spk-rayjob logs -f <ID>', purpose: '实时跟随日志，任务结束自动退出' },
  { command: 'spk-rayjob cancel <ID>', purpose: '停止任务' },
  { command: '<任意命令> --output json', purpose: '输出原始 JSON，供脚本解析' },
]

const copyText = async (value) => {
  if (await copyToClipboard(value)) ElMessage.success('已复制')
  else ElMessage.warning('浏览器阻止了剪贴板访问，请手动复制')
}
</script>
