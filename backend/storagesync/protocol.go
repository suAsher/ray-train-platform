package storagesync

// WorkSpec is sent only to a dedicated worker and authenticated internal
// metadata gateway. CallbackToken must never appear in public API responses.
type WorkSpec struct {
	SubjectKind       string            `json:"subjectKind"`
	RunID             string            `json:"runId"`
	PreviewID         string            `json:"previewId,omitempty"`
	Attempt           int               `json:"attempt"`
	Generation        int64             `json:"generation"`
	Phase             string            `json:"phase"`
	RecoveryJobUID    string            `json:"recoveryJobUID,omitempty"`
	Config            Config            `json:"config"`
	Mappings          []ResolvedMapping `json:"mappings"`
	ManifestDigest    string            `json:"manifestDigest"`
	SourceFingerprint string            `json:"sourceFingerprint"`
	TargetFingerprint string            `json:"targetFingerprint"`
	CheckpointRef     string            `json:"checkpointRef"`
	BaselineRef       string            `json:"baselineRef,omitempty"`
	CallbackURL       string            `json:"callbackUrl"`
	CallbackToken     string            `json:"callbackToken"`
	MetadataURL       string            `json:"metadataUrl"`
	Cursor            string            `json:"cursor,omitempty"`
	Limit             int               `json:"limit,omitempty"`
}
type Report struct {
	WorkerID          string              `json:"workerId"`
	RunID             string              `json:"runId"`
	PreviewID         string              `json:"previewId,omitempty"`
	Attempt           int                 `json:"attempt"`
	Generation        int64               `json:"generation"`
	Sequence          int64               `json:"sequence"`
	Phase             string              `json:"phase"`
	State             string              `json:"state"`
	ManifestDigest    string              `json:"manifestDigest"`
	SourceFingerprint string              `json:"sourceFingerprint"`
	TargetFingerprint string              `json:"targetFingerprint"`
	Progress          Progress            `json:"progress"`
	MappingProgress   []MappingProgress   `json:"mappingProgress,omitempty"`
	Files             WorkerFileReference `json:"files"`
	FailureReason     string              `json:"failureReason"`
	RequestsDrained   bool                `json:"requestsDrained"`
	BrowseEntries     []BrowseEntry       `json:"browseEntries,omitempty"`
	NextCursor        string              `json:"nextCursor,omitempty"`
	FileResults       []FileResult        `json:"fileResults,omitempty"`
}

// WorkerFileReference has an internal path; public FileReference omits it.
type WorkerFileReference struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Count  int64  `json:"count"`
}
