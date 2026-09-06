import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, existsSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
import { externalSubmitCommands } from './externalSubmit.js'

for (const platform of ['linux', 'macos']) {
  test(`${platform}: failed checksum never installs or executes downloaded code`, () => {
    const dir = mkdtempSync(join(tmpdir(), 'spk-guide-test-'))
    try {
      const bin = join(dir, 'bin'); mkdirSync(bin)
      const stub = (name, body) => writeFileSync(join(bin, name), `#!/bin/sh\n${body}\n`, { mode: 0o755 })
      stub('uname', `case "$1" in -s) echo ${platform === 'linux' ? 'Linux' : 'Darwin'};; *) echo ${platform === 'linux' ? 'x86_64' : 'arm64'};; esac`)
      stub('curl', 'while [ "$#" -gt 0 ]; do if [ "$1" = -o ]; then shift; printf "invalid payload\\n" > "$1"; fi; shift; done')
      stub('install', 'touch "$SPK_TEST_ROOT/installed"')
      const result = spawnSync(platform === 'macos' ? '/bin/zsh' : '/bin/bash', ['-c', externalSubmitCommands('https://example.test', platform).install], { env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, SPK_TEST_ROOT: dir }, encoding: 'utf8' })
      assert.notEqual(result.status, 0, result.stdout)
      assert.equal(existsSync(join(dir, 'installed')), false)
    } finally { rmSync(dir, { recursive: true, force: true }) }
  })
  test(`${platform}: shell syntax and quoting`, () => {
    const commands = externalSubmitCommands("https://example.test/a'b", platform)
    for (const name of ['install', 'login', 'tokenLogin', 'nativeRay', 'nativeCustomImage', 'customImage', 'init', 'dailyLoop']) {
      const result = spawnSync(platform === 'macos' ? '/bin/zsh' : '/bin/bash', ['-n'], { input: commands[name], encoding: 'utf8' })
      assert.equal(result.status, 0, `${name}: ${result.stderr}`)
    }
    assert.doesNotMatch(commands.login, /read -rp/)
  })
  test(`${platform}: native custom metadata survives quoted input without shell evaluation`, () => {
    const dir = mkdtempSync(join(tmpdir(), 'spk-native-guide-'))
    try {
      const bin = join(dir, 'bin'); mkdirSync(bin)
      writeFileSync(join(bin, 'ray'), '#!/bin/sh\npython3 -c \'import json,sys,os; open(os.environ["SPK_TEST_CAPTURE"],"w").write(json.dumps(sys.argv[1:]))\' "$@"\n', { mode: 0o755 })
      const capture = join(dir, 'args.json')
      const command = externalSubmitCommands('https://example.test', platform).nativeCustomImage.replace("python3 -m pip install 'ray[default]==2.35.0'", ': # omit network installation in test')
      const image = 'registry.example/team/quoted"image:tag'
      const result = spawnSync(platform === 'macos' ? '/bin/zsh' : '/bin/bash', ['-c', command], {
        input: `test-token\n${image}\nteam-queue\n`, encoding: 'utf8',
        env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, SPK_TEST_CAPTURE: capture },
      })
      assert.equal(result.status, 0, result.stderr)
      const args = JSON.parse(readFileSync(capture, 'utf8'))
      const metadata = JSON.parse(args[args.indexOf('--metadata-json') + 1])
      assert.equal(metadata['ray-platform.image'], image)
      assert.equal(metadata['ray-platform.queue'], 'team-queue')
      assert.equal(Object.keys(metadata).length, 6)
    } finally { rmSync(dir, { recursive: true, force: true }) }
  })
}

test('PowerShell uses secure prompts, literal quoting and fail-stop checksum before copy', () => {
  const commands = externalSubmitCommands("https://example.test/a'b", 'windows')
  assert.match(commands.install, /ErrorActionPreference = 'Stop'/)
  assert.match(commands.install, /throw 'SHA256/)
  assert.ok(commands.install.indexOf("throw 'SHA256") < commands.install.indexOf('Copy-Item'))
  assert.match(commands.install, /example.test\/a''b/)
  assert.match(commands.login, /Read-Host .* -AsSecureString/)
  assert.match(commands.nativeRay, /ConvertTo-Json/)
  assert.doesNotMatch(commands.dailyLoop, /<JOB ID>/)
})

test('custom image examples list accessible images and provide complete native resource metadata', () => {
  for (const platform of ['linux', 'macos', 'windows']) {
    const commands = externalSubmitCommands('https://example.test', platform)
    assert.match(commands.customImage, /spk-rayjob images/)
    assert.match(commands.customImage, /--image/)
    assert.match(commands.customImage, /--workers 1 --gpus-per-worker 1/)
    for (const key of ['image', 'worker-replicas', 'gpus-per-worker', 'cpu-per-worker', 'memory-per-worker', 'queue']) {
      assert.ok(commands.nativeCustomImage.includes(`ray-platform.${key}`), key)
    }
    assert.match(commands.nativeCustomImage, /--metadata-json/)
    assert.match(commands.nativeCustomImage, /json.dumps|ConvertTo-Json/)
    assert.match(commands.nativeCustomImage, /Read-Host .* -AsSecureString|read -rs SPK_TOKEN/)
    assert.doesNotMatch(commands.nativeCustomImage, /<TOKEN>/)
  }
})
