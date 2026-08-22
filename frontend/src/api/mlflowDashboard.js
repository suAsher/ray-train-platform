const MLFLOW_ACCESS_PREFIX = '/mlflow/?access_token='
const INVALID_MLFLOW_ACCESS_URL = '平台没有返回有效的 MLflow 访问地址'

function isValidMLflowAccessURL(value) {
  if (typeof value !== 'string' || !value.startsWith(MLFLOW_ACCESS_PREFIX)) return false
  if (/[\u0000-\u0020\u007f\\]/.test(value) || value.includes('#')) return false

  try {
    decodeURIComponent(value)
    const baseURL = new URL('https://portal.invalid')
    const parsedURL = new URL(value, baseURL)
    const accessTokens = parsedURL.searchParams.getAll('access_token')
    return parsedURL.origin === baseURL.origin
      && parsedURL.pathname === '/mlflow/'
      && accessTokens.length === 1
      && /^[A-Za-z0-9_-]+$/.test(accessTokens[0])
  } catch {
    return false
  }
}

export async function requestMLflowDashboardAccess(request) {
  const post = request || (await import('./client.js')).apiPost
  const payload = await post('/api/v1/mlflow-dashboard-access', {})
  const accessURL = payload?.url ?? payload?.data?.url

  if (!isValidMLflowAccessURL(accessURL)) {
    throw new Error(INVALID_MLFLOW_ACCESS_URL)
  }

  return accessURL
}
