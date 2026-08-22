import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const configURL = new URL('./nginx.conf', import.meta.url)

function locationBlock(config, location) {
  const start = config.indexOf(`location ${location} {`)
  assert.notEqual(start, -1, `missing ${location} location block`)
  const end = config.indexOf('\n    }', start)
  assert.notEqual(end, -1, `missing closing brace for ${location} location block`)
  return config.slice(start, end)
}

test('API reverse proxies preserve the browser Host header for WebSocket origin checks', async () => {
  const config = await readFile(configURL, 'utf8')

  for (const location of ['/api/', '/ray/']) {
    const block = locationBlock(config, location)
    assert.match(block, /proxy_set_header Host \$http_host;/)
    assert.match(block, /proxy_set_header Upgrade \$http_upgrade;/)
    assert.match(block, /proxy_set_header Connection \$connection_upgrade;/)
    assert.match(block, /proxy_http_version 1\.1;/)
  }
})

test('health endpoints reach the backend instead of falling back to the SPA', async () => {
  const config = await readFile(configURL, 'utf8')

  for (const location of ['/healthz', '/readyz']) {
    const block = locationBlock(config, location)
    assert.match(block, /proxy_pass http:\/\/ray-train-backend:8080/)
  }
})
