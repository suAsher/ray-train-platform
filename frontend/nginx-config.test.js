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

test('data-space uploads stream through the API proxy up to the backend limit', async () => {
  const config = await readFile(configURL, 'utf8')
  const block = locationBlock(config, '/api/')

  assert.match(block, /client_max_body_size 5g;/)
  assert.match(block, /proxy_request_buffering off;/)
  assert.match(block, /proxy_read_timeout 3600s;/)
  assert.match(block, /proxy_send_timeout 3600s;/)
})

test('health endpoints reach the backend instead of falling back to the SPA', async () => {
  const config = await readFile(configURL, 'utf8')

  for (const location of ['/healthz', '/readyz']) {
    const block = locationBlock(config, location)
    assert.match(block, /proxy_pass http:\/\/ray-train-backend:8080/)
  }
})

test('MLflow stays behind the backend proxy with upload-safe streaming settings', async () => {
  const config = await readFile(configURL, 'utf8')
  const block = locationBlock(config, '/mlflow/')

  assert.match(block, /proxy_pass http:\/\/ray-train-backend:8080\/mlflow\/;/)
  assert.match(block, /proxy_set_header Host \$http_host;/)
  assert.match(block, /proxy_set_header X-Real-IP \$remote_addr;/)
  assert.match(block, /proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;/)
  assert.match(block, /proxy_set_header X-Forwarded-Proto \$scheme;/)
  assert.match(block, /proxy_http_version 1\.1;/)
  assert.match(block, /proxy_read_timeout 3600s;/)
  assert.match(block, /proxy_send_timeout 3600s;/)
  assert.match(block, /proxy_request_buffering off;/)
  assert.match(block, /proxy_buffering off;/)
  assert.match(block, /client_max_body_size 2048m;/)
  assert.doesNotMatch(block, /mlflow\.mlflow-system|NodePort/i)
})

test('MLflow access tokens are excluded from Nginx access logs without disabling other access logs', async () => {
  const config = await readFile(configURL, 'utf8')
  const block = locationBlock(config, '/mlflow/')
  const disabledAccessLogs = config.match(/\baccess_log\s+off;/g) ?? []

  assert.match(block, /access_log off;/)
  assert.equal(disabledAccessLogs.length, 1)
})
