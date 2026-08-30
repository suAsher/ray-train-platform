package datasetpublisher

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrInvalidPlanOptions      = errors.New("invalid plan options")
	ErrInvalidRules            = errors.New("invalid dataset rules")
	ErrInvalidInventory        = errors.New("invalid dataset inventory")
	ErrInvalidPreviousManifest = errors.New("invalid previous manifest")
	ErrMissingSuccessMarker    = errors.New("missing upstream success marker")
	ErrMissingObject           = errors.New("missing inventory object")
	ErrUnstableInventory       = errors.New("inventory object changed within the stable window")
	ErrMissingAnnotation       = errors.New("missing annotation")
	ErrSplitTokenOverlap       = errors.New("train and validation tokens overlap")
	ErrInvalidPointCloudBytes  = errors.New("invalid float32 point cloud byte length")
	ErrEmptySamples            = errors.New("empty dataset samples")
)

type Split string

const (
	SplitTrain Split = "train"
	SplitVal   Split = "val"
	SplitTest  Split = "test"
)

type ObjectRole string

const (
	ObjectRolePoints     ObjectRole = "points"
	ObjectRoleAnnotation ObjectRole = "annotation"
	ObjectRoleAuxiliary  ObjectRole = "auxiliary"
)

type InventoryObject struct {
	Key        string
	SizeBytes  int64
	ETag       string
	ObservedAt time.Time
}

type DatasetRules struct {
	SchemaVersion         string
	PublisherVersion      string
	SuccessMarker         string
	PointRecordWidthBytes int64
	Samples               []SampleRule
}

type SampleRule struct {
	Token     string
	Scene     string
	Partition string
	Split     Split
	Objects   []SampleObjectRule
}

type SampleObjectRule struct {
	Key  string
	Role ObjectRole
}

type ObjectRef struct {
	Key       string
	Role      ObjectRole
	Split     Split
	Token     string
	Scene     string
	Partition string
	SizeBytes int64
	ETag      string
}

type Shard struct {
	ID           string
	Split        Split
	Scene        string
	Partition    string
	SampleTokens []string
	Digest       string
	ObjectKeys   []string
	InputBytes   int64
	Reused       bool
}

type Manifest struct {
	SchemaVersion         string
	PublisherVersion      string
	PointRecordWidthBytes int64
	Digest                string
	Shards                []Shard
	Objects               []ObjectRef
	Metadata              map[string]string
}

type ValidationError struct {
	Kind    error
	Key     string
	Token   string
	Message string
}

func (err *ValidationError) Error() string {
	if err == nil {
		return ""
	}
	detail := err.Message
	if detail == "" && err.Kind != nil {
		detail = err.Kind.Error()
	}
	if err.Key != "" {
		return fmt.Sprintf("%s: %s", detail, err.Key)
	}
	if err.Token != "" {
		return fmt.Sprintf("%s: %s", detail, err.Token)
	}
	return detail
}

func (err *ValidationError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Kind
}

func cloneManifest(manifest Manifest) Manifest {
	return Manifest{
		SchemaVersion:         manifest.SchemaVersion,
		PublisherVersion:      manifest.PublisherVersion,
		PointRecordWidthBytes: manifest.PointRecordWidthBytes,
		Digest:                manifest.Digest,
		Shards:                cloneShards(manifest.Shards),
		Objects:               append([]ObjectRef(nil), manifest.Objects...),
		Metadata:              cloneStringMap(manifest.Metadata),
	}
}

func cloneShards(shards []Shard) []Shard {
	result := make([]Shard, len(shards))
	for index, shard := range shards {
		result[index] = shard
		result[index].SampleTokens = append([]string(nil), shard.SampleTokens...)
		result[index].ObjectKeys = append([]string(nil), shard.ObjectKeys...)
	}
	return result
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
