const shellQuote = (value) => `'${value.replaceAll("'", "'\\''")}'`
const psQuote = (value) => `'${value.replaceAll("'", "''")}'`

export function externalSubmitCommands(origin, platform) {
  const windows = platform === 'windows'
  const mac = platform === 'macos'
  const filename = windows ? 'spk-rayjob-windows-amd64.exe' : mac ? 'spk-rayjob-darwin-arm64' : 'spk-rayjob-linux-amd64'
  const base = `${origin}/downloads/spk-rayjob`
  const install = windows ? `# PowerShell；失败不会覆盖已安装版本
& {
  $ErrorActionPreference = 'Stop'
  if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -ne 'X64') { throw '仅提供 Windows x64 二进制' }
  $spkTemp = Join-Path ([IO.Path]::GetTempPath()) ([guid]::NewGuid().ToString())
  New-Item -ItemType Directory -Path $spkTemp | Out-Null
  try {
    $spkFile = Join-Path $spkTemp '${filename}'
    $spkSums = Join-Path $spkTemp 'SHA256SUMS'
    Invoke-WebRequest -Uri ${psQuote(`${base}/${filename}`)} -OutFile $spkFile
    Invoke-WebRequest -Uri ${psQuote(`${base}/SHA256SUMS`)} -OutFile $spkSums
    $spkMatches = @(Get-Content $spkSums | Where-Object { $_ -match '^[0-9a-fA-F]{64}\\s+\\*?spk-rayjob-windows-amd64\\.exe$' })
    if ($spkMatches.Count -ne 1) { throw 'SHA256 清单缺失或重复，请重新下载' }
    $spkExpected = ($spkMatches[0] -split '\\s+')[0]
    if ((Get-FileHash $spkFile -Algorithm SHA256).Hash -ne $spkExpected) { throw 'SHA256 不一致，安装已停止' }
    $spkBin = Join-Path $env:USERPROFILE '.spk-rayjob'
    New-Item -ItemType Directory -Force $spkBin | Out-Null
    Copy-Item $spkFile (Join-Path $spkBin 'spk-rayjob.exe') -Force
    $env:PATH = "$spkBin;$env:PATH"
    & (Join-Path $spkBin 'spk-rayjob.exe') version
    if ($LASTEXITCODE -ne 0) { throw '客户端版本检查失败' }
  } finally { Remove-Item -LiteralPath $spkTemp -Recurse -Force }
}` : `# ${mac ? 'macOS Apple Silicon / zsh' : 'Linux x86_64 / Bash'}；在子 Shell 中失败即停
(
  set -eu
  [ "$(uname -s)" = '${mac ? 'Darwin' : 'Linux'}' ] && [ "$(uname -m)" = '${mac ? 'arm64' : 'x86_64'}' ] || { echo '系统或架构不匹配，请选择对应下载；Intel Mac / Linux ARM 暂无此构建' >&2; exit 1; }
  spk_tmp=$(mktemp -d) || exit 1
  trap 'rm -rf -- "$spk_tmp"' EXIT
  curl -fL ${shellQuote(`${base}/${filename}`)} -o "$spk_tmp/${filename}" || exit 1
  curl -fL ${shellQuote(`${base}/SHA256SUMS`)} -o "$spk_tmp/SHA256SUMS" || exit 1
  spk_want=$(awk '$2 == "${filename}" || $2 == "*${filename}" { print $1 }' "$spk_tmp/SHA256SUMS")
  [ "\${#spk_want}" -eq 64 ] || { echo 'SHA256 清单缺失或重复' >&2; exit 1; }
  spk_got=$(${mac ? 'shasum -a 256' : 'sha256sum'} "$spk_tmp/${filename}" | awk '{print $1}')
  [ "$spk_want" = "$spk_got" ] || { echo 'SHA256 不一致，安装已停止' >&2; exit 1; }
  mkdir -p "$HOME/.local/bin" || exit 1
  install -m 0755 "$spk_tmp/${filename}" "$HOME/.local/bin/spk-rayjob" || exit 1
  "$HOME/.local/bin/spk-rayjob" version
) && export PATH="$HOME/.local/bin:$PATH"`
  const login = windows ? `& {
  $spkUsername = Read-Host '平台用户名'
  $spkSecret = Read-Host '平台密码' -AsSecureString
  $spkPtr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($spkSecret)
  try {
    [Runtime.InteropServices.Marshal]::PtrToStringBSTR($spkPtr) | spk-rayjob login --server ${psQuote(origin)} --username $spkUsername --password-stdin
    if ($LASTEXITCODE -eq 0) { spk-rayjob login-check }
  } finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($spkPtr); $spkSecret.Dispose() }
}` : `(
  printf '平台用户名: '
  read -r SPK_USERNAME
  printf '平台密码: '
  read -rs SPK_PASSWORD
  printf '\\n'
  printf '%s\\n' "$SPK_PASSWORD" | spk-rayjob login --server ${shellQuote(origin)} --username "$SPK_USERNAME" --password-stdin
  spk_result=$?
  unset SPK_USERNAME SPK_PASSWORD
  [ "$spk_result" -eq 0 ] && spk-rayjob login-check
)`
  const tokenLogin = windows ? login.replace("  $spkUsername = Read-Host '平台用户名'\n", '').replace("'平台密码'", "'平台 PAT'").replace('--username $spkUsername --password-stdin', '--token-stdin') : `(
  printf '平台 PAT: '
  read -rs SPK_TOKEN
  printf '\\n'
  printf '%s\\n' "$SPK_TOKEN" | spk-rayjob login --server ${shellQuote(origin)} --token-stdin
  spk_result=$?
  unset SPK_TOKEN
  [ "$spk_result" -eq 0 ] && spk-rayjob login-check
)`
  const init = windows ? `# 先进入你的真实代码目录（不要照抄占位路径）
& {
$ErrorActionPreference = 'Stop'
Set-Location 'C:\\path\\to\\my-training-project'
spk-rayjob init --name my-training --workers 1 --gpus-per-worker 1
if ($LASTEXITCODE -ne 0) { throw '初始化失败' }
notepad .spk-rayjob.yaml
}` : `# 先进入你的真实代码目录（不要照抄占位路径）
cd '/path/to/my-training-project' &&
spk-rayjob init --name my-training --workers 1 --gpus-per-worker 1 &&
\${EDITOR:-vi} .spk-rayjob.yaml`
  const dailyLoop = `# 在代码目录修改并保存训练脚本；确认 YAML 已填写镜像和入口
spk-rayjob submit --watch
# 将 JOB_ID 替换为上一步输出的真实任务 ID；不要加尖括号
spk-rayjob logs -f JOB_ID
spk-rayjob jobs --state RUNNING`
  const nativeRay = windows ? `# 已安装 Python；在项目代码目录运行。先在账户与安全创建 PAT
& {
  python -m pip install 'ray[default]==2.35.0'
  if ($LASTEXITCODE -ne 0) { throw 'Ray 安装失败' }
  $spkSecret = Read-Host '平台 PAT' -AsSecureString
  $spkPtr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($spkSecret)
  try {
    $env:RAY_JOB_HEADERS = @{ Authorization = ('Bearer ' + [Runtime.InteropServices.Marshal]::PtrToStringBSTR($spkPtr)) } | ConvertTo-Json -Compress
    ray job submit --address ${psQuote(`${origin}/ray`)} --working-dir . -- python3 train.py
  } finally { Remove-Item Env:RAY_JOB_HEADERS -ErrorAction SilentlyContinue; [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($spkPtr); $spkSecret.Dispose() }
}` : `# 已安装 Python；在项目代码目录运行，入口 train.py 按项目替换
(
  set -e
  python3 -m pip install 'ray[default]==2.35.0'
  printf '平台 PAT: '
  read -rs SPK_TOKEN
  printf '\\n'
  export RAY_JOB_HEADERS=$(printf '%s' "$SPK_TOKEN" | python3 -c 'import json,sys; print(json.dumps({"Authorization": "Bearer " + sys.stdin.read()}))')
  unset SPK_TOKEN
  ray job submit --address ${shellQuote(`${origin}/ray`)} --working-dir . -- python3 train.py
)`
  return { install, login, tokenLogin, init, dailyLoop, nativeRay }
}
