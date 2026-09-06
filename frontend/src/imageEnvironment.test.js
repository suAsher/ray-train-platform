import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'
import { emptyImageEnvironment, imageEnvironmentRows } from './imageEnvironment.js'

test('environment rows never infer missing versions or claim detection', () => {
  const rows = imageEnvironmentRows({ rayVersion: '2.58.0', environment: { python: '3.11', cuda: '  ' } })
  assert.equal(rows.find((row) => row.key === 'python').value, '3.11')
  assert.equal(rows.find((row) => row.key === 'cuda').value, '未提供')
  assert.equal(rows.find((row) => row.key === 'ray').value, '2.58.0')
  assert.ok(imageEnvironmentRows().every((row) => row.value === '未提供'))
  assert.notStrictEqual(emptyImageEnvironment(), emptyImageEnvironment())
})

test('environment component renders declarations as text, not trusted HTML', () => {
  const component = fs.readFileSync(new URL('./components/ImageEnvironment.vue', import.meta.url), 'utf8')
  assert.match(component, /管理员声明.*未经平台自动验证/)
  assert.match(component, /CPU 检查不代表 GPU/)
  assert.doesNotMatch(component, /v-html/)
  assert.match(component, /image.description/)
})
