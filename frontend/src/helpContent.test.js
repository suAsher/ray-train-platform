import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import { helpSections, renderHelpMarkdown, CONTRACT_SNIPPET, SMOKE_SCRIPT, NATIVE_RAY_SUBMIT } from './help/content.js'

const read = (path) => readFile(new URL(path, import.meta.url), 'utf8')

// The page and the downloaded file are built from one source so a user cannot
// take away a copy that disagrees with what the platform told them on screen.
test('the downloadable document is generated from the rendered sections', async () => {
  const markdown = renderHelpMarkdown()

  for (const section of helpSections) {
    assert.ok(markdown.includes(`## ${section.title}`), `${section.title} is missing from the download`)
  }
  // The contract snippet is the one thing users copy into their own code.
  assert.ok(markdown.includes(CONTRACT_SNIPPET), 'the Python contract snippet is missing from the download')
  assert.match(markdown, /^# RayTrain 平台使用说明/)
})

// Each of these caused a real failure. Losing one from the help content means
// the next user rediscovers it by burning GPU hours.
test('help content keeps the guidance that prevents known failures', async () => {
  const markdown = renderHelpMarkdown()

  assert.match(markdown, /os\.environ/, 'reading paths from the environment is not explained')
  assert.match(markdown, /PermissionError/, 'the empty shell-expansion failure is not explained')
  assert.match(markdown, /torchrun/, 'the "do not write torchrun yourself" rule is missing')
  assert.match(markdown, /python3/, 'the python vs python3 failure is missing')
  for (const variable of ['PLATFORM_DATASET_PATH', 'PLATFORM_OUTPUT_PATH', 'PLATFORM_CHECKPOINT_PATH', 'PLATFORM_CACHE_PATH']) {
    assert.ok(markdown.includes(variable), `${variable} is not documented`)
  }
  // Five data modes with no decision rule is what made the feature unusable.
  for (const mode of ['mount', 'cache', 'ray-data-stage', 'ray-data', 'streaming']) {
    assert.ok(markdown.includes(mode), `data mode ${mode} has no guidance`)
  }
})

// A page that only exists behind a link nobody follows does not help, so the
// guidance also appears at the two fields where the mistakes are actually made.
test('the submit form carries the guidance inline and links to the help page', async () => {
  const source = await read('./components/job/StepRuntime.vue')

  assert.match(source, /PLATFORM_OUTPUT_PATH/, 'the entrypoint field does not mention the output path variable')
  assert.match(source, /PermissionError/, 'the entrypoint field does not warn about shell expansion')
  assert.match(source, /to="\/help#contract"/, 'the entrypoint field does not link to the contract section')
  assert.match(source, /to="\/help#data-mode"/, 'the data mode selector does not link to the selection guidance')
  assert.match(source, /data_time/, 'the data mode selector gives no rule for when to switch')
})

test('the help page is reachable from the workspace navigation', async () => {
  const [router, layout] = await Promise.all([read('./router/index.js'), read('./layout/Layout.vue')])

  assert.match(router, /path: 'help'/)
  assert.match(router, /views\/Help\/index\.vue/)
  assert.match(layout, /to: '\/help'/)
})

// Downloading reuses the shared blob helper rather than a second implementation
// that could revoke the object URL before the browser has taken the file.
test('the help page downloads through the shared blob helper', async () => {
  const source = await read('./views/Help/index.vue')

  assert.match(source, /import \{ saveBlobAsFile \} from '\.\.\/\.\.\/checkpointDownload'/)
  assert.match(source, /new Blob\(\[markdown\]/)
  assert.equal(source.includes('URL.createObjectURL'), false, 'the page must not carry its own object-URL handling')
})

// The page has to read like a walkthrough, not a reference manual: a first-time
// user needs something to copy and run, not a list of rules to remember. The
// smoke script verifies the four interfaces that every later failure comes from
// - variables resolve, input is readable, GPU is visible, output is writable.
test('the first section is something a user can follow, not just read', async () => {
  const quickstart = helpSections.find((section) => section.id === 'quickstart')
  const steps = quickstart.blocks.find((block) => block.kind === 'steps')

  const runnable = steps.items.filter((item) => item.code)
  assert.ok(runnable.length >= 2, 'the walkthrough must hand the user something to copy and run')
  assert.ok(steps.items.every((item) => item.title && item.body), 'every step needs a title and an explanation')

  for (const probe of ['PLATFORM_DATASET_PATH', 'PLATFORM_OUTPUT_PATH', 'cuda', 'flush=True']) {
    assert.ok(SMOKE_SCRIPT.includes(probe), `the smoke script does not check ${probe}`)
  }
  // The download has to carry the script too, or it is not the same document.
  assert.ok(renderHelpMarkdown().includes(SMOKE_SCRIPT), 'the smoke script is missing from the download')
})

// Users reach the platform through the Portal and the CLI. They have no
// checkout of this repository, no build machine and no cluster access, so
// pointing them at a file path or an internal host names something they cannot
// obtain - it reads as help but is a dead end. Anything the page cannot answer
// itself has to hand off to somewhere they can actually go.
test('user-facing pages never point at things a user cannot reach', async () => {
  const pages = await Promise.all([
    read('./views/Help/index.vue'),
    read('./views/ExternalSubmit/index.vue'),
  ])

  for (const source of pages) {
    const template = source.slice(0, source.indexOf('<script'))
    for (const unreachable of ['docs/', '代码仓库', '提交指南', '构建机', 'build-image', 'helm ', 'kubectl']) {
      assert.equal(
        template.includes(unreachable),
        false,
        `a user-facing page mentions ${unreachable}, which users have no access to`,
      )
    }
  }
})

// Knowing the contract is not enough to submit anything. Both command-line
// entrances need a command that can be copied and a meaning for each option,
// otherwise the page explains the rules of a form the user cannot fill in.
test('submission section gives runnable commands for both command-line entrances', async () => {
  const markdown = renderHelpMarkdown()
  const submit = helpSections.find((section) => section.id === 'submit')

  const commands = submit.blocks.filter((block) => block.kind === 'code')
  assert.ok(commands.length >= 4, 'the section must cover smoke, multi-node, resume and native Ray')
  assert.ok(commands.some((block) => block.text.includes('spk-rayjob submit')))
  assert.ok(commands.some((block) => block.text.includes('ray job submit')))

  // A bare native submit silently runs on one GPU, so the resource metadata has
  // to be shown rather than left for the user to discover.
  assert.match(NATIVE_RAY_SUBMIT, /ray-platform\.worker-replicas/)
  assert.match(NATIVE_RAY_SUBMIT, /ray-platform\.gpus-per-worker/)

  // Every option shown in a command needs a meaning somewhere on the page.
  for (const option of ['--engine', '--input-space', '--data-mode', '--resume-from-job', '--watch']) {
    assert.ok(markdown.includes(option), `${option} is used but never explained`)
  }
  // The three execution modes carry constraints that reject a submission.
  for (const mode of ['single_gpu', 'torchrun', 'ray_train']) {
    assert.ok(markdown.includes(mode), `execution mode ${mode} is not documented`)
  }
})

// The question that exposed the gap: do the data modes require code changes?
// Three are transparent and two are not, and getting that wrong means either
// avoiding a mode that was free, or submitting a run that silently trains on a
// fraction of the data.
test('each data mode states whether it requires a code change', async () => {
  const dataMode = helpSections.find((section) => section.id === 'data-mode')
  const table = dataMode.blocks.find((block) => block.kind === 'table')

  assert.ok(table.headers.some((header) => header.includes('改代码')), 'the table never answers the code-change question')
  const answers = Object.fromEntries(table.rows.map((row) => [row[0], row[row.length - 1]]))
  for (const transparent of ['mount', 'cache', 'ray-data-stage']) {
    assert.equal(answers[transparent], '不用', `${transparent} is transparent to training code`)
  }
  for (const invasive of ['ray-data', 'streaming']) {
    assert.equal(answers[invasive], '要改', `${invasive} requires the training loop to consume shards`)
  }
})

// Each topic a user asked for has to be reachable and carry something to act
// on, otherwise the page lists subjects instead of answering questions.
test('the page covers the requested topics with actionable content', async () => {
  const markdown = renderHelpMarkdown()
  const ids = helpSections.map((section) => section.id)

  for (const topic of ['cache', 'ray-data', 'streaming', 'submit', 'code', 'observability', 'resume', 'storage']) {
    assert.ok(ids.includes(topic), `topic ${topic} is missing`)
  }
  // Ray Data's two forms differ in whether manual sharding must be removed;
  // leaving that out is how a run silently trains on a fraction of the data.
  assert.match(markdown, /DistributedSampler/)
  assert.match(markdown, /iter_torch_batches/)
  // The cache only prewarms input when asked to, which surprises people.
  assert.match(markdown, /--cache-preload input/)
  // The streaming path is not fully verified yet and must say so.
  assert.match(markdown, /尚未完成全量验证/)
  // Retrying a submission is not the same as resuming training.
  assert.match(markdown, /不是续训|从头重跑/)
})

// A capability the platform has but the page never mentions is a capability
// users do not know exists. These are the ones reachable from the sidebar.
test('every user-facing platform capability is covered', async () => {
  const markdown = renderHelpMarkdown()

  for (const [capability, evidence] of [
    ['交互式调试', /JupyterLab|调试环境/],
    ['训练产物下载', /训练产物/],
    ['版本化数据集', /不可变版本/],
    ['访问令牌', /个人访问令牌|PAT/],
    ['配额与排队', /配额/],
    ['实验中心', /实验中心|MLflow/],
  ]) {
    assert.match(markdown, evidence, `${capability} is not explained anywhere`)
  }
})

// A flat list of seventeen topics is a wall to scan, so each one is filed under
// a stage of the journey rather than left to be read in order.
test('topics are grouped so the list stays scannable', async () => {
  const ungrouped = helpSections.filter((section) => !section.group)
  assert.equal(ungrouped.length, 0, `topics without a group: ${ungrouped.map((s) => s.id).join(', ')}`)

  const groups = [...new Set(helpSections.map((section) => section.group))]
  assert.ok(groups.length >= 4 && groups.length <= 6, `grouping should stay coarse, got ${groups.length}`)
  for (const group of groups) {
    const size = helpSections.filter((section) => section.group === group).length
    assert.ok(size <= 7, `group ${group} has ${size} topics, which is no longer scannable`)
  }
})
