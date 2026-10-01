package storagesync

import "time"

func previewScanDeadline(preview Preview) time.Time {
	lifetime := 24 * time.Hour
	if preview.Kind == "BROWSE" {
		lifetime = 5 * time.Minute
	}
	return preview.CreatedAt.Add(lifetime)
}

// A live scan renews its short lease, but cannot extend the absolute job
// lifetime. Completed results receive a separate acceptance TTL.
func (m *Manager) previewLease(preview Preview) time.Time {
	expires := m.now().Add(m.options.PreviewTTL)
	if deadline := previewScanDeadline(preview); deadline.Before(expires) {
		return deadline
	}
	return expires
}
