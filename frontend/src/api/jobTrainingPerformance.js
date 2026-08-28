import { apiGet } from './client.js'
import { jobTrainingPerformancePath } from '../jobTrainingPerformance.js'

export function fetchJobTrainingPerformance(jobId, window) {
  return apiGet(jobTrainingPerformancePath(jobId, window))
}
