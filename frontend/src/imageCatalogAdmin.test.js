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
