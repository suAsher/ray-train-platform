import { apiGet } from './client.js'
import { jobGPUHistoryPath } from '../gpuMetrics.js'

export function fetchJobGPUHistory(jobId, window) {
  return apiGet(jobGPUHistoryPath(jobId, window))
}
