package domain

import (
	"math"
	"testing"
)

func TestPlanDataSpaceUpload(t *testing.T) {
	tests := []struct {
		name       string
		size       int64
		mode       DataSpaceUploadMode
		partSize   int64
		totalParts int
		wantErr    bool
	}{
		{name: "empty remains single", size: 0, mode: DataSpaceUploadSingle},
		{name: "threshold remains single", size: DataSpaceMultipartThresholdBytes, mode: DataSpaceUploadSingle},
		{name: "first multipart byte", size: DataSpaceMultipartThresholdBytes + 1, mode: DataSpaceUploadMultipart, partSize: DataSpacePreferredPartBytes, totalParts: 5},
		{name: "larger than legacy five gib", size: 6 * 1024 * 1024 * 1024, mode: DataSpaceUploadMultipart, partSize: DataSpacePreferredPartBytes, totalParts: 96},
		{name: "grows parts to stay below provider count", size: DataSpacePreferredPartBytes*DataSpaceMaxMultipartParts + 1, mode: DataSpaceUploadMultipart, partSize: 65 * 1024 * 1024, totalParts: 9847},
		{name: "provider maximum", size: DataSpaceMaxPartBytes * DataSpaceMaxMultipartParts, mode: DataSpaceUploadMultipart, partSize: DataSpaceMaxPartBytes, totalParts: DataSpaceMaxMultipartParts},
		{name: "past provider maximum", size: DataSpaceMaxPartBytes*DataSpaceMaxMultipartParts + 1, wantErr: true},
		{name: "negative", size: -1, wantErr: true},
		{name: "integer max", size: math.MaxInt64, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := PlanDataSpaceUpload(tt.size)
			if (err != nil) != tt.wantErr {
				t.Fatalf("PlanDataSpaceUpload(%d) error = %v, wantErr %v", tt.size, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if plan.Mode != tt.mode || plan.PartSizeBytes != tt.partSize || plan.TotalParts != tt.totalParts {
				t.Fatalf("plan = %+v, want mode=%s partSize=%d totalParts=%d", plan, tt.mode, tt.partSize, tt.totalParts)
			}
		})
	}
}

func TestExpectedDataSpacePartSize(t *testing.T) {
	plan := DataSpaceUploadPlan{Mode: DataSpaceUploadMultipart, SizeBytes: 130, PartSizeBytes: 64, TotalParts: 3}
	for part, want := range map[int]int64{1: 64, 2: 64, 3: 2} {
		got, err := plan.ExpectedPartSize(part)
		if err != nil || got != want {
			t.Fatalf("part %d = %d, %v; want %d", part, got, err, want)
		}
	}
	if _, err := plan.ExpectedPartSize(0); err == nil {
		t.Fatal("expected invalid part number error")
	}
}
