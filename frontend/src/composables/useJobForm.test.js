import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import { jobFormStepIssues } from './jobFormIssues.js'

const readFormSource = () => readFile(new URL('./useJobForm.js', import.meta.url), 'utf8')

test('job form defaults to Ray orchestrated DDP and keeps managed recovery defaults separate', async () => {
  const source = await readFormSource()

  assert.match(source, /trainingEngine:\s*normalizeTrainingEngine\(/)
  assert.match(source, /maxFailures:\s*queryNonNegativeInteger\([^,]+,\s*2\)/)
  assert.match(source, /checkpointEveryEpochs:\s*queryNonNegativeInteger\([^,]+,\s*1\)/)
  assert.match(source, /checkpointKeepLatest:\s*queryNonNegativeInteger\([^,]+,\s*3\)/)
  assert.match(source, /checkpointKeepBest:\s*queryNonNegativeInteger\([^,]+,\s*1\)/)
})

test('job form preserves an explicit managed engine and parent from rerun query state', async () => {
  const source = await readFormSource()

  assert.match(source, /route\?\.query\?\.trainingEngine/)
  assert.match(source, /route\?\.query\?\.parentJobId/)
})

test('runtime step blocks submission with a clear Chinese message when team quota is exhausted', () => {
  const issues = jobFormStepIssues({
    step: 1,
    form: { entrypoint: 'python train.py', workerReplicas: 1, gpusPerWorker: 1 },
    limits: {
      maxWorkerReplicas: 0,
      maxGpusPerWorker: 0,
      maxTotalGpus: 0,
      tenantQuota: { gpuLimit: 8, gpuUsed: 8, gpuAvailable: 0 },
    },
    commandWarnings: [],
  })

  assert.deepEqual(issues, ['团队当前没有可用 GPU 配额，请等待正在运行的任务释放资源，或联系超级管理员调整配额'])
})

test('runtime step remains valid when newly fetched limits have available GPUs', () => {
  const issues = jobFormStepIssues({
    step: 1,
    form: { entrypoint: 'python train.py', workerReplicas: 3, gpusPerWorker: 8 },
    limits: {
      maxWorkerReplicas: 4,
      maxGpusPerWorker: 8,
      maxTotalGpus: 32,
      tenantQuota: { gpuLimit: 32, gpuUsed: 8, gpuAvailable: 24 },
    },
    commandWarnings: [],
  })

  assert.deepEqual(issues, [])
})
