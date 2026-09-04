package domain

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrDataSpaceUploadNotFound   = errors.New("data-space upload not found")
	ErrDataSpaceUploadConflict   = errors.New("data-space upload conflicts with an active session")
	ErrDataSpaceUploadIncomplete = errors.New("data-space upload is incomplete")
)

type DataSpaceUploadMode string

const (
	DataSpaceUploadSingle    DataSpaceUploadMode = "single"
	DataSpaceUploadMultipart DataSpaceUploadMode = "multipart"

	DataSpaceMultipartThresholdBytes int64 = 256 * 1024 * 1024
	DataSpacePreferredPartBytes      int64 = 64 * 1024 * 1024
	DataSpaceMaxPartBytes            int64 = 5 * 1024 * 1024 * 1024
	DataSpaceMaxMultipartParts             = 10000
	DataSpaceUploadSessionTTL              = 24 * time.Hour
)

type DataSpaceUploadPlan struct {
	Mode          DataSpaceUploadMode
	SizeBytes     int64
	PartSizeBytes int64
	TotalParts    int
}

// PlanDataSpaceUpload does not impose a product-level whole-file limit. It
// selects a multipart layout within the object store's physical constraints.
func PlanDataSpaceUpload(sizeBytes int64) (DataSpaceUploadPlan, error) {
	if sizeBytes < 0 {
		return DataSpaceUploadPlan{}, fmt.Errorf("upload size must not be negative")
	}
	if sizeBytes <= DataSpaceMultipartThresholdBytes {
		return DataSpaceUploadPlan{Mode: DataSpaceUploadSingle, SizeBytes: sizeBytes}, nil
	}
	partSize := DataSpacePreferredPartBytes
	minimum := ceilDiv(sizeBytes, int64(DataSpaceMaxMultipartParts))
	if minimum > partSize {
		const mib int64 = 1024 * 1024
		partSize = ceilDiv(minimum, mib) * mib
	}
	if partSize <= 0 || partSize > DataSpaceMaxPartBytes {
		return DataSpaceUploadPlan{}, fmt.Errorf("upload exceeds object-store multipart capacity")
	}
	total := ceilDiv(sizeBytes, partSize)
	if total < 1 || total > DataSpaceMaxMultipartParts {
		return DataSpaceUploadPlan{}, fmt.Errorf("upload exceeds object-store multipart capacity")
	}
	return DataSpaceUploadPlan{Mode: DataSpaceUploadMultipart, SizeBytes: sizeBytes, PartSizeBytes: partSize, TotalParts: int(total)}, nil
}

func (plan DataSpaceUploadPlan) ExpectedPartSize(partNumber int) (int64, error) {
	if plan.Mode != DataSpaceUploadMultipart || plan.PartSizeBytes < 1 || plan.TotalParts < 1 || partNumber < 1 || partNumber > plan.TotalParts {
		return 0, fmt.Errorf("invalid multipart plan or part number")
	}
	if partNumber < plan.TotalParts {
		return plan.PartSizeBytes, nil
	}
	last := plan.SizeBytes - int64(plan.TotalParts-1)*plan.PartSizeBytes
	if last < 1 || last > plan.PartSizeBytes {
		return 0, fmt.Errorf("invalid final part size")
	}
	return last, nil
}

func ceilDiv(value, divisor int64) int64 {
	if value <= 0 || divisor <= 0 {
		return 0
	}
	return 1 + (value-1)/divisor
}

type DataSpaceUploadState string

const (
	DataSpaceUploadActive     DataSpaceUploadState = "ACTIVE"
	DataSpaceUploadCompleting DataSpaceUploadState = "COMPLETING"
	DataSpaceUploadCompleted  DataSpaceUploadState = "COMPLETED"
	DataSpaceUploadAborting   DataSpaceUploadState = "ABORTING"
	DataSpaceUploadAborted    DataSpaceUploadState = "ABORTED"
)

type DataSpaceUploadSession struct {
	ID            string
	TenantID      string
	UserID        string
	SpaceID       DataSpaceID
	RootPrefix    string
	RelativePath  string
	ContentType   string
	SizeBytes     int64
	PartSizeBytes int64
	TotalParts    int
	ProviderID    string
	State         DataSpaceUploadState
	ExpiresAt     time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (session DataSpaceUploadSession) Plan() DataSpaceUploadPlan {
	return DataSpaceUploadPlan{Mode: DataSpaceUploadMultipart, SizeBytes: session.SizeBytes, PartSizeBytes: session.PartSizeBytes, TotalParts: session.TotalParts}
}

type DataSpaceUploadPart struct {
	SessionID  string
	PartNumber int
	SizeBytes  int64
	SHA256     string
	ETag       string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
