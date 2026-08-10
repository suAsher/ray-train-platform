package api

// SubmissionService exposes the existing unified submission path to protocol
// adapters while keeping Handler construction and policy ownership unchanged.
func (h *Handler) SubmissionService() *SubmissionService {
	if h == nil {
		return nil
	}
	return h.submission
}
