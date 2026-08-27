import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'

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
  assert.match(quotaManage, /supportedEngines:\s*newImage\.value\.supportedEngines\.filter[\s\S]*ray-train/)
  assert.match(quotaManage, /至少选择一个.*引擎/)
})

test('image create request clones supported engines and sends explicit compatibility metadata', () => {
  assert.match(quotaManage, /createImage\(\{[\s\S]*name:\s*newImage\.value\.name/)
  assert.match(quotaManage, /rayVersion:\s*newImage\.value\.rayVersion/)
  assert.match(quotaManage, /supportedEngines:\s*\[\.\.\.newImage\.value\.supportedEngines\]/)
  assert.match(quotaManage, /shared:\s*Boolean\(newImage\.value\.shared\)/)
})
