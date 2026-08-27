import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'

import {
  buildCreateImageRequest,
  defaultImageCompatibilityState,
  reconcileImageCompatibility,
} from './imageCompatibility.js'

const catalogPanel = fs.readFileSync(new URL('./components/admin/CatalogPanel.vue', import.meta.url), 'utf8')
const quotaManage = fs.readFileSync(new URL('./views/QuotaManage/index.vue', import.meta.url), 'utf8')
const catalogApi = fs.readFileSync(new URL('./api/catalog.js', import.meta.url), 'utf8')
const apiClient = fs.readFileSync(new URL('./api/client.js', import.meta.url), 'utf8')

test('image catalogue explains and accepts either a tag or a digest', () => {
  assert.match(catalogPanel, /tag.*digest|digest.*tag/i)
  assert.match(quotaManage, /镜像.*tag.*digest|镜像.*digest.*tag/i)
  assert.doesNotMatch(quotaManage, /必须带 @sha256 digest/)
})

test('super administrators can change an existing image between team and platform scope', () => {
  assert.match(catalogPanel, /edit-scope/)
  assert.match(quotaManage, /updateImageScope/)
  assert.match(catalogApi, /apiPatch/)
  assert.match(apiClient, /method: 'PATCH'/)
  assert.match(catalogApi, /\/api\/v1\/images\/\$\{id\}/)
  assert.match(catalogApi, /targetTenantId/)
  assert.match(quotaManage, /currentTenantId\.value/)
})

test('image catalogue admin captures and displays Ray compatibility metadata', () => {
  assert.match(catalogPanel, /rayVersion/)
  assert.match(catalogPanel, /supportedEngines/)
  assert.match(quotaManage, /Ray 版本/)
  for (const version of ['2.35.0', '2.56.1', '2.58.0']) {
    assert.match(quotaManage, new RegExp(`value=["']${version.replaceAll('.', '\\.')}`))
  }
  assert.match(quotaManage, /el-checkbox-group[^>]+newImage\.supportedEngines/)
  assert.match(quotaManage, /ray-ddp/)
  assert.match(quotaManage, /ray-train/)
})

test('legacy Ray removes ray-train and publishing requires at least one engine', () => {
  assert.match(quotaManage, /newImage(?:\.value)?\.rayVersion\s*===\s*['"]2\.35\.0['"]/)
  assert.match(quotaManage, /reconcileImageCompatibility/)
  assert.match(quotaManage, /至少选择一个.*引擎/)
})

test('image create request clones supported engines and sends explicit compatibility metadata', () => {
  assert.match(quotaManage, /buildCreateImageRequest/)
})

test('compatibility defaults are fresh legacy values', () => {
  const first = defaultImageCompatibilityState()
  const second = defaultImageCompatibilityState()

  assert.deepEqual(first, { rayVersion: '2.35.0', supportedEngines: ['ray-ddp'] })
  assert.deepEqual(second, first)
  assert.notStrictEqual(second.supportedEngines, first.supportedEngines)
})

test('Ray version transitions reconcile engines immutably', () => {
  const trainOnly = { rayVersion: '2.56.1', supportedEngines: ['ray-train'], name: 'runtime' }
  const legacyFromTrain = reconcileImageCompatibility(trainOnly, '2.35.0')
  assert.deepEqual(legacyFromTrain, { rayVersion: '2.35.0', supportedEngines: ['ray-ddp'], name: 'runtime' })
  assert.deepEqual(trainOnly.supportedEngines, ['ray-train'])

  const mixed = { rayVersion: '2.58.0', supportedEngines: ['ray-ddp', 'ray-train'] }
  assert.deepEqual(reconcileImageCompatibility(mixed, '2.35.0').supportedEngines, ['ray-ddp'])

  const legacy = { rayVersion: '2.35.0', supportedEngines: ['ray-ddp'] }
  for (const version of ['2.56.1', '2.58.0']) {
    assert.deepEqual(reconcileImageCompatibility(legacy, version), {
      rayVersion: version,
      supportedEngines: ['ray-ddp'],
    })
  }
})

test('helpers preserve explicit empty metadata until compatibility reconciliation requires a legacy fallback', () => {
  const form = { rayVersion: '', supportedEngines: [], name: 'empty', shared: false }
  const request = buildCreateImageRequest(form)
  assert.equal(request.rayVersion, '')
  assert.deepEqual(request.supportedEngines, [])
  assert.deepEqual(reconcileImageCompatibility(form, '2.56.1').supportedEngines, [])
  assert.deepEqual(reconcileImageCompatibility(form, '2.35.0').supportedEngines, ['ray-ddp'])
})

test('create-image request owns its supported engine slice', () => {
  const form = {
    name: 'runtime', reference: 'registry.example/runtime:tag', kind: 'training',
    rayVersion: '2.56.1', supportedEngines: ['ray-ddp'], shared: 1,
    framework: 'PyTorch', isDefault: true,
  }
  const request = buildCreateImageRequest(form)
  assert.deepEqual(request, {
    name: 'runtime', reference: 'registry.example/runtime:tag', kind: 'training',
    rayVersion: '2.56.1', supportedEngines: ['ray-ddp'], shared: true,
    framework: 'PyTorch', isDefault: true,
  })
  assert.notStrictEqual(request.supportedEngines, form.supportedEngines)
  request.supportedEngines.push('ray-train')
  assert.deepEqual(form.supportedEngines, ['ray-ddp'])
})
