import { externalSubmitCommands } from './externalSubmit.js'

const origin = 'https://raytrain.example.com'
export const cliOnboarding = {
  id: 'cli-onboarding-v2', group: '入门', title: 'CLI 安装、登录与部署边界',
  summary: '网页和 CLI 连接同一个独立平台地址。先确认服务器地址、客户端版本和启用的存储能力。',
  prerequisites: ['raytrain.example.com 是占位示例，请替换为自己的平台域名。', '基础安装使用 Git 固定提交和管理员登记的 PVC。working-dir 上传和原生 Ray 上传需要可选对象存储配置。'],
  success: ['version 与预期发行版本一致；PAT 登录成功，当前用户和团队符合预期。'],
  troubleshooting: ['登录失败时检查实际平台地址和 PAT 有效性；命令缺失时用 type -a 或 Get-Command 检查当前可执行文件。不要发送令牌或密码。'],
  relatedLinks: [{ label: '账户与安全', to: '/account-security' }, { label: '外部提交', to: '/external-submit' }],
  blocks: [
    { kind: 'note', text: 'CLI 下载服务由管理员单独启用；未启用时按仓库开发文档从 backend/cmd/spk-rayjob 构建。不要从不受信任的站点下载二进制。' },
    ...['linux', 'macos', 'windows'].flatMap(platform => {
      const commands = externalSubmitCommands(origin, platform)
      const lang = platform === 'windows' ? 'powershell' : 'bash'
      return [
        { kind: 'code', label: `${platform}：核对 SHA256 后安装`, lang, text: commands.install },
        { kind: 'code', label: `${platform}：PAT 登录`, lang, text: commands.login },
      ]
    }),
    { kind: 'code', label: '检查当前 CLI', lang: 'bash', text: 'type -a spk-rayjob\nhash -r\nspk-rayjob version\nspk-rayjob --help' },
    { kind: 'note', text: '--help 来自本机客户端，切换 server 不会升级可执行文件。Windows 使用 Get-Command spk-rayjob -All 检查 PATH。需要升级时先核对管理员发布的版本和 SHA256。' },
    { kind: 'note', text: 'GPU 节点加入后，管理员需验证驱动、device plugin、Node Ready、镜像拉取和 PVC 挂载。标签不代表存储就绪：Scheduled 只表示节点分配，FailedMount 与 ImagePullBackOff 分别检查存储和镜像。新增节点不增加运行任务的 Worker 数，也不自动增加团队配额。' },
    { kind: 'warning', title: '凭据与数据边界', text: '密码、PAT 和存储凭据不得进入 Git、训练参数或日志。共享资产只授予登记范围内的数据访问，不能填写任意 PVC 或 hostPath。' },
  ],
}
