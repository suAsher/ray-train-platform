const encoded = (value) => encodeURIComponent(String(value ?? ''))

export const datasetPath = (datasetId) => `/api/v1/datasets/${encoded(datasetId)}`
export const datasetVersionsPath = (datasetId) => `${datasetPath(datasetId)}/versions`
export const datasetVersionPath = (datasetId, versionId) => `${datasetVersionsPath(datasetId)}/${encoded(versionId)}`
export const datasetPublicationPath = (datasetId) => `${datasetPath(datasetId)}/publications`
