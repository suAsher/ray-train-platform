import { jobQuotaModel } from '../platformLimits.js'
import { parseEntrypoint } from '../submission.js'

/** Pure runtime-step validation used by the submit form and focused tests. */
export function jobFormStepIssues({ step, form, limits, commandWarnings = [] }) {
  if (step !== 1) return []
  const issues = []
  const quota = jobQuotaModel(limits)
  if (quota.blocked) issues.push(quota.blockMessage)
  if (parseEntrypoint(form.entrypoint).length === 0) issues.push('请填写训练启动命令')
  const totalGPUs = Number(form.workerReplicas || 0) * Number(form.gpusPerWorker || 0)
  if (totalGPUs < 1) issues.push('至少申请 1 张 GPU')
  issues.push(...commandWarnings)
  return issues
}
