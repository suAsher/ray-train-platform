import { apiFetch, apiGet, apiPost } from './client.js'
const admin = '/api/v1/admin/help/documents'
const path = id => `${admin}/${encodeURIComponent(id)}`
export const listHelpDocuments = () => apiGet('/api/v1/help/documents')
export const listHelpDrafts = () => apiGet(admin)
export const createHelpDocument = doc => apiPost(admin, doc)
export const saveHelpDocument = (id, doc) => apiFetch(path(id), { method: 'PUT', body: JSON.stringify(doc) })
export const publishHelpDocument = (id, expectedVersion) => apiPost(`${path(id)}/publish`, { expectedVersion })
export const unpublishHelpDocument = (id, expectedVersion) => apiPost(`${path(id)}/unpublish`, { expectedVersion })
export const helpDocumentHistory = id => apiGet(`${path(id)}/history`)
export const restoreHelpDocument = (id, expectedVersion, restoreVersion) => apiPost(`${path(id)}/restore`, { expectedVersion, restoreVersion })
