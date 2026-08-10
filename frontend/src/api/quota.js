import { apiGet } from './client'

/** The signed-in user's own tenant GPU budget. Available to every role. */
export function fetchMyQuota() {
  return apiGet('/api/v1/quota')
}
