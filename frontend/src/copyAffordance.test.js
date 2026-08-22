import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const read = (path) => readFile(new URL(path, import.meta.url), 'utf8')

// Install and login commands were rendered as plain <pre>: a user could read
// them but not copy them, which makes a multi-line shell snippet unusable.
// Every command block now goes through CopyBlock, which carries the button.
test('external submit renders every command through a copyable block', async () => {
  const source = await read('./views/ExternalSubmit/index.vue')
  assert.equal(/<pre[^>]*class="command"/.test(source), false, 'a non-copyable <pre class="command"> remains')
  for (const command of ['loginCommand', 'tokenLoginCommand', 'initCommand', 'dailyLoopCommand', 'projectFileExample', 'nativeRayCommand']) {
    assert.match(source, new RegExp(`<CopyBlock[^>]*:text="${command}"`), `${command} is not copyable`)
  }
  // Install is per-OS; the selected snippet is what must be copyable.
  assert.match(source, /<CopyBlock[^>]*:text="installCommands\[platform\]"/)
})

// Users submit from their own laptops, not from the build machine, so every
// supported OS needs an install path that verifies the published checksum.
test('install instructions cover each supported client platform', async () => {
  const source = await read('./views/ExternalSubmit/index.vue')
  for (const platform of ['linux', 'macos', 'windows']) {
    assert.match(source, new RegExp(`${platform}:`), `${platform} install snippet is missing`)
  }
  assert.match(source, /spk-rayjob-darwin-arm64/)
  assert.match(source, /spk-rayjob-windows-amd64\.exe/)
  // Each snippet must check the checksum before installing.
  for (const verifier of ['sha256sum', 'shasum -a 256', 'Get-FileHash']) {
    assert.ok(source.includes(verifier), `${verifier} verification is missing`)
  }
  assert.equal(source.includes('不需要登录构建机'), false)
})

test('generated commands in the submit flow are copyable', async () => {
  const [preview, runtime] = await Promise.all([
    read('./components/job/SubmitPreview.vue'),
    read('./components/job/StepRuntime.vue'),
  ])
  assert.match(preview, /<CopyBlock[^>]*commandPreview/)
  assert.match(preview, /<CopyBlock[^>]*cliCommand/)
  assert.match(runtime, /<CopyBlock[^>]*commandPreview/)
})

// navigator.clipboard is undefined on plain HTTP and in some embedded
// browsers, so a single implementation with a fallback is shared everywhere.
test('copy uses one shared helper with a non-secure-context fallback', async () => {
  const [helper, block] = await Promise.all([
    read('./clipboard.js'),
    read('./components/CopyBlock.vue'),
  ])
  assert.match(helper, /navigator\.clipboard/)
  assert.match(helper, /execCommand\('copy'\)/)
  assert.match(block, /import \{ copyToClipboard \} from '\.\.\/clipboard'/)
  // The component must not carry a second, divergent implementation.
  assert.equal(block.includes('document.execCommand'), false)
})
