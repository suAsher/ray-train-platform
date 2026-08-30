import assert from 'node:assert/strict'
import test from 'node:test'

import * as submission from './submission.js'

const {
  buildJobSpec,
  equivalentSubmitCommand,
  equivalentSubmitCommandPreview,
  equivalentSubmitCommandForJob,
  parseEntrypoint,
  shellArg,
} = submission

test('submit preview remains renderable while a streaming dataset is incomplete', () => {
  const form = {
    ...baseForm(),
    trainingEngine: 'ray-train',
    dataMode: 'streaming',
    datasetRef: { dataset: '', version: 'latest' },
    datasetCachePolicy: 'auto',
    workerReplicas: 2,
    gpusPerWorker: 8,
  }

  assert.throws(() => equivalentSubmitCommand(form), /数据集/)
  assert.match(equivalentSubmitCommandPreview(form), /完成上述必填项/)
})

const baseForm = () => ({
  name: 'support-sft-001',
  image: 'registry.example/ray@sha256:' + 'a'.repeat(64),
  codeSourceType: 'git',
  gitURL: 'https://git.example.com/ml/support-sft.git',
  gitCommit: '0123456789abcdef',
  workspaceSnapshot: '',
  tosCodeURI: '',
  entrypoint: 'python train.py --epochs 3',
  input: { spaceId: 'team-shared', relativePath: 'train' },
  checkpoint: { spaceId: 'public', relativePath: 'models/base' },
  output: { spaceId: 'my-runs' },
  workerReplicas: 2,
  gpusPerWorker: 8,
  cpuPerWorker: 32,
  memoryPerWorker: '128Gi',
  timeoutSeconds: 3600,
  maxRetries: 1,
})

const cacheLimits = (overrides = {}) => ({
  cache: {
    enabled: true,
    defaultMode: 'off',
    modes: ['off', 'runtime'],
    allowedSizes: ['100Gi', '200Gi'],
    defaultSize: '200Gi',
    maxSize: '200Gi',
    mountPath: '/mnt/cache',
    ...overrides,
  },
})

test('parseEntrypoint preserves quoted arguments', () => {
  assert.deepEqual(
    parseEntrypoint('python train.py --run-name "support sft"'),
    ['python', 'train.py', '--run-name', 'support sft'],
  )
})

test('shellArg uses POSIX single-quote escaping, including for an empty value', () => {
  assert.equal(shellArg(''), "''")
  assert.equal(shellArg("a b'c;$(d)`e`&&f"), "'a b'\"'\"'c;$(d)`e`&&f'")
})

test('buildJobSpec maps a Git submission into the platform runtime contract', () => {
  const spec = buildJobSpec(baseForm())

  assert.deepEqual(spec.source, {
    type: 'git',
    url: 'https://git.example.com/ml/support-sft.git',
    commit: '0123456789abcdef',
  })
  assert.deepEqual(spec.entrypoint, {
    command: ['python'],
    args: ['train.py', '--epochs', '3'],
  })
  assert.deepEqual(spec.resources, {
    workerReplicas: 2,
    gpusPerWorker: 8,
    cpuPerWorker: 32,
    memoryPerWorker: '128Gi',
  })

  assert.deepEqual(spec.execution, { mode: 'ray_train' })
  assert.deepEqual(spec.input, { space: 'team-shared', relativePath: 'train' })
  assert.deepEqual(spec.checkpoint, { space: 'public', relativePath: 'models/base' })
  assert.deepEqual(spec.output, { space: 'my-runs' })
  assert.equal('datasetStorage' in spec, false)
  assert.equal('datasetUri' in spec, false)
  assert.equal(spec.timeoutSeconds, 3600)
  assert.deepEqual(spec.retryPolicy, { maxRetries: 1 })
  assert.equal(spec.trainingEngine, 'ray-ddp')
  assert.equal('rayVersion' in spec, false)
})

test('managed engine is opt-in and carries managed recovery and checkpoint policy', () => {
  const form = {
    ...baseForm(),
    trainingEngine: 'ray-train',
    maxFailures: 2,
    checkpointEveryEpochs: 1,
    checkpointKeepLatest: 3,
    checkpointKeepBest: 1,
  }
  const before = structuredClone(form)
  const spec = buildJobSpec(form)

  assert.equal(spec.trainingEngine, 'ray-train')
  assert.deepEqual(spec.managed, {
    maxFailures: 2,
    checkpoint: { everyEpochs: 1, keepLatest: 3, keepBest: 1 },
  })
  assert.equal('rayVersion' in spec, false)
  assert.deepEqual(form, before)
})

test('Ray Data staging is explicit, uses the selected input root, and keeps init preload disabled', () => {
  const spec = buildJobSpec({
    ...baseForm(),
    trainingEngine: 'ray-train',
    maxFailures: 2,
    checkpointEveryEpochs: 1,
    checkpointKeepLatest: 3,
    checkpointKeepBest: 1,
    dataMode: 'ray-data-stage',
    cacheMode: 'runtime',
    cacheSize: '200Gi',
    cachePreload: '',
    input: { spaceId: 'public', relativePath: 'labeled' },
  }, cacheLimits())

  assert.equal(spec.dataMode, 'ray-data-stage')
  assert.deepEqual(spec.cache, { mode: 'runtime', size: '200Gi' })
  assert.deepEqual(spec.managed.rayData, { format: 'files', uri: '/mnt/data/input' })
})

test('Ray Data staging rejects the compatibility engine before submit', () => {
  assert.throws(() => buildJobSpec({
    ...baseForm(),
    trainingEngine: 'ray-ddp',
    dataMode: 'ray-data-stage',
    cacheMode: 'runtime',
    cacheSize: '200Gi',
    input: { spaceId: 'public', relativePath: 'labeled' },
}, cacheLimits()), /Ray Train/)
})

test('Ray Data staging rejects a whole governed space without a dataset path', () => {
  assert.throws(() => buildJobSpec({
    ...baseForm(),
    trainingEngine: 'ray-train',
    dataMode: 'ray-data-stage',
    cacheMode: 'runtime',
    cacheSize: '200Gi',
    input: { spaceId: 'public', relativePath: '' },
  }, cacheLimits()), /具体的数据集子目录/)
})

test('versioned streaming submits only a logical immutable dataset selector and cache policy', () => {
  const form = {
    ...baseForm(),
    trainingEngine: 'ray-train',
    maxFailures: 2,
    checkpointEveryEpochs: 1,
    checkpointKeepLatest: 3,
    checkpointKeepBest: 1,
    dataMode: 'streaming',
    datasetRef: { dataset: 'labeled-full', version: 'latest' },
    datasetCachePolicy: 'auto',
    cacheMode: 'off',
    cacheSize: '',
    input: {},
  }
  const spec = buildJobSpec(form, cacheLimits({ mountPaths: ['/mnt/cache', '/mnt/cache2'] }))

  assert.equal(spec.dataMode, 'streaming')
  assert.deepEqual(spec.datasetRef, { dataset: 'labeled-full', version: 'latest' })
  assert.equal(spec.cachePolicy, 'auto')
  assert.deepEqual(spec.input, {})
  assert.equal('cache' in spec, false)
  assert.equal('rayData' in spec.managed, false)
})

test('versioned streaming validates engine, dataset selector, and bounded dual-NVMe capability', () => {
  const streaming = {
    ...baseForm(),
    trainingEngine: 'ray-train',
    dataMode: 'streaming',
    datasetRef: { dataset: 'labeled-full', version: 'latest' },
    datasetCachePolicy: 'bounded',
    input: {},
  }

  assert.throws(() => buildJobSpec({ ...streaming, trainingEngine: 'ray-ddp' }, cacheLimits()), /Ray Train/)
  assert.throws(() => buildJobSpec({ ...streaming, datasetRef: { dataset: '', version: 'latest' } }, cacheLimits()), /数据集/)
  assert.throws(() => buildJobSpec(streaming, cacheLimits({ mountPaths: ['/mnt/cache'] })), /两块 NVMe/)
  assert.doesNotThrow(() => buildJobSpec(streaming, cacheLimits({ mountPaths: ['/mnt/cache', '/mnt/cache2'] })))
})

test('equivalent command renders versioned streaming without internal storage paths', () => {
  const command = equivalentSubmitCommand({
    ...baseForm(),
    trainingEngine: 'ray-train',
    dataMode: 'streaming',
    datasetRef: { dataset: 'labeled-full', version: 'latest' },
    datasetCachePolicy: 'auto',
    input: {},
  })

  assert.match(command, /--dataset 'labeled-full:latest'/)
  assert.match(command, /--data-mode 'streaming'/)
  assert.match(command, /--cache-policy 'auto'/)
  assert.doesNotMatch(command, /--input-(?:space|path)|tos:\/\/|manifest/)
})

test('legacy DDP payload omits managed policy even when stale managed fields are present', () => {
  const spec = buildJobSpec({
    ...baseForm(),
    trainingEngine: 'ray-ddp',
    maxFailures: 9,
    checkpointEveryEpochs: 9,
    checkpointKeepLatest: 9,
    checkpointKeepBest: 9,
  })

  assert.equal(spec.trainingEngine, 'ray-ddp')
  assert.equal('managed' in spec, false)
})

test('resume submission carries an immutable parent job relationship', () => {
  const parentJobId = 'job-0123456789abcdef01234567'
  const spec = buildJobSpec({ ...baseForm(), parentJobId })
  assert.equal(spec.parentJobId, parentJobId)
})

test('resume submission enforces the exact backend parent job ID contract', () => {
  for (const parentJobId of [
    'job-0123456789abcdef0123456',
    'job-0123456789ABCDEF01234567',
    ' job-0123456789abcdef01234567',
    'job-0123456789abcdef01234567 ',
  ]) {
    assert.throws(() => buildJobSpec({ ...baseForm(), parentJobId }), /任务 ID 格式不合法/)
  }
})

test('buildJobSpec rejects a multi-worker torchrun request before submitting it', () => {
  const form = baseForm()
  form.executionMode = 'torchrun'
  assert.throws(() => buildJobSpec(form), /单机多卡 DDP/)
})

test('buildJobSpec maps a workspace snapshot and rejects unsafe logical data paths', () => {
  const form = baseForm()
  form.codeSourceType = 'workspace'
  form.workspaceSnapshot = 'snapshot-20260811'
  form.input = { spaceId: 'team-shared', relativePath: '../private' }

  assert.throws(() => buildJobSpec(form), /训练输入/)
  form.input = { spaceId: 'team-shared', relativePath: 'validation' }
  const spec = buildJobSpec(form)

  assert.deepEqual(spec.source, { type: 'workspace', snapshot: 'snapshot-20260811' })
  assert.deepEqual(spec.input, { space: 'team-shared', relativePath: 'validation' })
})

test('buildJobSpec rejects retired object-store source types', () => {
	const form = baseForm()
	form.codeSourceType = 'tos'
	assert.throws(() => buildJobSpec(form), /Git 仓库或调试工作区代码版本/)
})

test('buildJobSpec always lets the platform allocate a task output directory', () => {
  const form = baseForm()
  form.output = { spaceId: 'team-shared' }
  assert.throws(() => buildJobSpec(form), /我的训练结果/)
})

test('buildJobSpec keeps omitted and explicit-off cache backward compatible', () => {
  const omitted = buildJobSpec(baseForm(), cacheLimits())
  assert.equal('cache' in omitted, false)

  const off = buildJobSpec({ ...baseForm(), cacheMode: 'off', cacheSize: '' }, cacheLimits())
  assert.equal('cache' in off, false)
})

test('buildJobSpec maps a policy-approved runtime cache', () => {
  const spec = buildJobSpec({ ...baseForm(), cacheMode: 'runtime', cacheSize: '200Gi' }, cacheLimits())
  assert.deepEqual(spec.cache, { mode: 'runtime', size: '200Gi' })
})

test('buildJobSpec maps parameter-only automatic input preload and requires an exact input directory', () => {
  const spec = buildJobSpec({
    ...baseForm(), cacheMode: 'runtime', cacheSize: '200Gi', cachePreload: 'input',
  }, cacheLimits())
  assert.deepEqual(spec.cache, { mode: 'runtime', size: '200Gi', preload: 'input' })

  assert.throws(
    () => buildJobSpec({
      ...baseForm(), input: { spaceId: 'public', relativePath: '' },
      cacheMode: 'runtime', cacheSize: '200Gi', cachePreload: 'input',
    }, cacheLimits()),
    /选择一个具体的数据集子目录/,
  )
})

test('buildJobSpec rejects unsupported or internally inconsistent cache requests', () => {
  assert.throws(
    () => buildJobSpec({ ...baseForm(), cacheMode: 'persistent', cacheSize: '200Gi' }, cacheLimits()),
    /缓存模式/,
  )
  assert.throws(
    () => buildJobSpec({ ...baseForm(), cacheMode: 'off', cacheSize: '200Gi' }, cacheLimits()),
    /缓存关闭时不能选择容量/,
  )
  assert.throws(
    () => buildJobSpec({ ...baseForm(), cacheMode: 'runtime', cacheSize: '' }, cacheLimits()),
    /请选择运行时缓存容量/,
  )
})

test('buildJobSpec rejects runtime cache disabled or disallowed by the loaded server policy', () => {
  const form = { ...baseForm(), cacheMode: 'runtime', cacheSize: '200Gi' }
  assert.throws(() => buildJobSpec(form, cacheLimits({ enabled: false })), /未开放运行时缓存/)
  assert.throws(() => buildJobSpec(form, cacheLimits({ modes: ['off'] })), /未开放运行时缓存/)
  assert.throws(() => buildJobSpec(form, cacheLimits({ allowedSizes: ['100Gi'] })), /不在平台允许范围/)
})

test('equivalent submit command includes the selected runtime cache flags', () => {
  const command = equivalentSubmitCommand({
    ...baseForm(),
    cacheMode: 'runtime',
    cacheSize: '200Gi',
  })

  assert.match(command, /--cache-mode 'runtime' \\\n  --cache-size '200Gi'/)
})

test('equivalent submit command shows the effective engine but never exposes a Ray version selector', () => {
  const command = equivalentSubmitCommand({
    ...baseForm(),
    trainingEngine: 'ray-train',
    maxFailures: 7,
    checkpointEveryEpochs: 4,
    checkpointKeepLatest: 9,
    checkpointKeepBest: 2,
  })
  assert.match(command, /--engine 'ray-train'/)
  assert.match(command, /--max-failures '7'/)
  assert.match(command, /--checkpoint-every-epochs '4'/)
  assert.match(command, /--checkpoint-keep-latest '9'/)
  assert.match(command, /--checkpoint-keep-best '2'/)
  assert.doesNotMatch(command, /--ray-version/)
})

test('equivalent submit command omits managed policy flags for Ray DDP', () => {
  const command = equivalentSubmitCommand({
    ...baseForm(),
    trainingEngine: 'ray-ddp',
    maxFailures: 7,
    checkpointEveryEpochs: 4,
    checkpointKeepLatest: 9,
    checkpointKeepBest: 2,
  })
  assert.doesNotMatch(command, /--(?:max-failures|checkpoint-every-epochs|checkpoint-keep-latest|checkpoint-keep-best)/)
})

test('equivalent submit command includes automatic input preload as one user parameter', () => {
  const command = equivalentSubmitCommand({
    ...baseForm(), cacheMode: 'runtime', cacheSize: '200Gi', cachePreload: 'input',
  })
  assert.match(command, /--cache-size '200Gi' \\\n  --cache-preload 'input'/)
})

test('equivalent submit command omits cache flags for off and legacy forms', () => {
  const off = equivalentSubmitCommand({ ...baseForm(), cacheMode: 'off', cacheSize: '' })
  const legacy = equivalentSubmitCommand(baseForm())

  assert.doesNotMatch(off, /--cache-(?:mode|size)/)
  assert.doesNotMatch(legacy, /--cache-(?:mode|size)/)
})

test('equivalent submit command shell-quotes every interpolated flag value exactly', () => {
  const command = equivalentSubmitCommand({
    ...baseForm(),
    name: "team's run; $(name) `name` && next",
    image: "registry.example/train image:latest; $(image) `image` && next's",
    entrypoint: "python train.py --run 'alpha beta'; $(entrypoint) `entrypoint` && next",
    workerReplicas: '2; $(workers)',
    gpusPerWorker: '8 && `gpus`',
    cacheMode: 'runtime',
    cacheSize: "200 Gi; $(cache) `cache` && next's",
    input: {
      spaceId: "team input's; $(input-space) `input-space` && next",
      relativePath: "train path's; $(input-path) `input-path` && next",
    },
    checkpoint: {
      spaceId: "public checkpoint's; $(checkpoint-space) `checkpoint-space` && next",
      relativePath: "models base's; $(checkpoint-path) `checkpoint-path` && next",
    },
  })

  assert.equal(command, [
    'spk-rayjob submit',
    "--name 'team'\"'\"'s run; $(name) `name` && next'",
    "--image 'registry.example/train image:latest; $(image) `image` && next'\"'\"'s'",
    "--entrypoint 'python train.py --run '\"'\"'alpha beta'\"'\"'; $(entrypoint) `entrypoint` && next'",
    "--engine 'ray-ddp'",
    "--workers '2; $(workers)'",
    "--gpus-per-worker '8 && `gpus`'",
    "--cache-mode 'runtime'",
    "--cache-size '200 Gi; $(cache) `cache` && next'\"'\"'s'",
    "--input-space 'team input'\"'\"'s; $(input-space) `input-space` && next'",
    "--input-path 'train path'\"'\"'s; $(input-path) `input-path` && next'",
    "--checkpoint-space 'public checkpoint'\"'\"'s; $(checkpoint-space) `checkpoint-space` && next'",
    "--checkpoint-path 'models base'\"'\"'s; $(checkpoint-path) `checkpoint-path` && next'",
    '--watch',
  ].join(' \\\n  '))
})

test('JobDetail equivalent command includes explicit runtime cache flags through the shared builder', () => {
  const command = equivalentSubmitCommandForJob({
    name: 'support-sft-001',
    entrypoint: 'python train.py --epochs 3',
    spec: {
      image: 'registry.example/ray@sha256:' + 'a'.repeat(64),
      resources: { workerReplicas: 2, gpusPerWorker: 8 },
      input: { space: 'team-shared', relativePath: 'train' },
      cache: { mode: 'runtime', size: '200Gi' },
    },
  })

  assert.match(command, /--cache-mode 'runtime' \\\n  --cache-size '200Gi'/)
  assert.match(command, /--input-space 'team-shared'/)
})

test('JobDetail equivalent command preserves automatic preload', () => {
  const command = equivalentSubmitCommandForJob({
    name: 'cached-run', entrypoint: 'python train.py',
    spec: {
      image: 'registry.example/ray@sha256:' + 'a'.repeat(64),
      resources: { workerReplicas: 2, gpusPerWorker: 8 },
      input: { space: 'public', relativePath: 'labeled/fz-v1' },
      cache: { mode: 'runtime', size: '1Ti', preload: 'input' },
    },
  })
  assert.match(command, /--cache-preload 'input'/)
})

test('JobDetail equivalent command accepts every backend checkpoint boundary', () => {
  const command = equivalentSubmitCommandForJob({
    name: 'managed-boundary', entrypoint: 'python train.py',
    spec: {
      image: 'registry.example/ray@sha256:' + 'a'.repeat(64),
      trainingEngine: 'ray-train',
      resources: { workerReplicas: 2, gpusPerWorker: 8 },
      managed: {
        maxFailures: 10,
        checkpoint: { everyEpochs: 100000, keepLatest: 1000, keepBest: 1000 },
      },
    },
  })

  assert.match(command, /--max-failures '10'/)
  assert.match(command, /--checkpoint-every-epochs '100000'/)
  assert.match(command, /--checkpoint-keep-latest '1000'/)
  assert.match(command, /--checkpoint-keep-best '1000'/)
})

test('JobDetail equivalent command shell-quotes persisted values exactly', () => {
  const command = equivalentSubmitCommandForJob({
    name: "saved job's; $(name) `name` && next",
    entrypoint: "python saved.py --run 'alpha beta'; $(entrypoint) `entrypoint` && next",
    spec: {
      image: "registry.example/saved image:latest; $(image) `image` && next's",
      resources: { workerReplicas: '3; $(workers)', gpusPerWorker: '4 && `gpus`' },
      input: {
        space: "saved input's; $(input-space) `input-space` && next",
        relativePath: "saved train's; $(input-path) `input-path` && next",
      },
      checkpoint: {
        space: "saved checkpoint's; $(checkpoint-space) `checkpoint-space` && next",
        relativePath: "saved models's; $(checkpoint-path) `checkpoint-path` && next",
      },
      cache: { mode: 'runtime', size: "100 Gi; $(cache) `cache` && next's" },
    },
  })

  assert.equal(command, [
    'spk-rayjob submit',
    "--name 'saved job'\"'\"'s; $(name) `name` && next'",
    "--image 'registry.example/saved image:latest; $(image) `image` && next'\"'\"'s'",
    "--entrypoint 'python saved.py --run '\"'\"'alpha beta'\"'\"'; $(entrypoint) `entrypoint` && next'",
    "--engine 'ray-ddp'",
    "--workers '3; $(workers)'",
    "--gpus-per-worker '4 && `gpus`'",
    "--cache-mode 'runtime'",
    "--cache-size '100 Gi; $(cache) `cache` && next'\"'\"'s'",
    "--input-space 'saved input'\"'\"'s; $(input-space) `input-space` && next'",
    "--input-path 'saved train'\"'\"'s; $(input-path) `input-path` && next'",
    "--checkpoint-space 'saved checkpoint'\"'\"'s; $(checkpoint-space) `checkpoint-space` && next'",
    "--checkpoint-path 'saved models'\"'\"'s; $(checkpoint-path) `checkpoint-path` && next'",
    '--watch',
  ].join(' \\\n  '))
})

test('JobDetail equivalent command omits cache flags for off and legacy jobs', () => {
  const baseJob = {
    name: 'support-sft-001',
    entrypoint: 'python train.py',
    spec: {
      image: 'registry.example/ray@sha256:' + 'a'.repeat(64),
      resources: { workerReplicas: 1, gpusPerWorker: 1 },
    },
  }

  assert.doesNotMatch(equivalentSubmitCommandForJob(baseJob), /--cache-(?:mode|size)/)
  assert.doesNotMatch(
    equivalentSubmitCommandForJob({ ...baseJob, spec: { ...baseJob.spec, cache: { mode: 'off' } } }),
    /--cache-(?:mode|size)/,
  )
})
