export const canOpenRayDashboard = (job) => Boolean(job?.rayClusterName)

export const jobDashboardAccessPath = (jobId) => `/api/v1/jobs/${encodeURIComponent(jobId)}/dashboard-access`
