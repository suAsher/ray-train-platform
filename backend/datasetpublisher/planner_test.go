package datasetpublisher

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func TestPlannerUsesStableWindowWhenSuccessMarkerIsOmitted(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	plan, err := PlanDatasetPublication(observedBefore(now,
		object("incremental/scene-a/token-1/points.pkl", 128, "points"),
		object("incremental/scene-a/token-1/annotation.pkl", 64, "annotation"),
	), PlanOptions{Now: now, StableWindow: time.Minute, Rules: datasetRules(
		sample("token-1", "scene-a", "", SplitTrain,
			ruleObject("incremental/scene-a/token-1/points.pkl", ObjectRolePoints),
			ruleObject("incremental/scene-a/token-1/annotation.pkl", ObjectRoleAnnotation),
		),
	)})
	if err != nil {
		t.Fatalf("markerless incremental plan was rejected: %v", err)
	}
	if plan.DryRun().AddedShardCount != 1 {
		t.Fatalf("dry-run = %+v, want one added shard", plan.DryRun())
	}
}

func TestPlannerRequiresConfiguredSuccessMarkerButStillValidatesSamples(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	rules := datasetRulesWithMarker("_SUCCESS",
		sample("token-1", "scene-a", "", SplitTrain,
			ruleObject("scene-a/token-1/points.pkl", ObjectRolePoints),
			ruleObject("scene-a/token-1/annotation.pkl", ObjectRoleAnnotation),
		),
	)
	_, err := PlanDatasetPublication(observedBefore(now,
		object("scene-a/token-1/points.pkl", 128, "points"),
		object("scene-a/token-1/annotation.pkl", 64, "annotation"),
	), PlanOptions{Now: now, StableWindow: time.Minute, Rules: rules})
	if !errors.Is(err, ErrMissingSuccessMarker) {
		t.Fatalf("error=%v want missing success marker", err)
	}
}

func TestPlannerRejectsRecentOrChangingReferencedObjects(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	rules := datasetRules(
		sample("token-1", "scene-a", "", SplitTrain,
			ruleObject("scene-a/token-1/points.pkl", ObjectRolePoints),
			ruleObject("scene-a/token-1/annotation.pkl", ObjectRoleAnnotation),
		),
	)
	tests := []struct {
		name      string
		inventory []InventoryObject
	}{
		{
			name: "single recent observation",
			inventory: []InventoryObject{
				{Key: "scene-a/token-1/points.pkl", SizeBytes: 128, ETag: "points", ObservedAt: now.Add(-30 * time.Second)},
				{Key: "scene-a/token-1/annotation.pkl", SizeBytes: 64, ETag: "annotation", ObservedAt: now.Add(-time.Hour)},
			},
		},
		{
			name: "changed after stable baseline",
			inventory: []InventoryObject{
				{Key: "scene-a/token-1/points.pkl", SizeBytes: 128, ETag: "old", ObservedAt: now.Add(-time.Hour)},
				{Key: "scene-a/token-1/points.pkl", SizeBytes: 128, ETag: "new", ObservedAt: now.Add(-30 * time.Second)},
				{Key: "scene-a/token-1/annotation.pkl", SizeBytes: 64, ETag: "annotation", ObservedAt: now.Add(-time.Hour)},
			},
		},
		{
			name: "same timestamp conflict",
			inventory: []InventoryObject{
				{Key: "scene-a/token-1/points.pkl", SizeBytes: 128, ETag: "a", ObservedAt: now.Add(-time.Hour)},
				{Key: "scene-a/token-1/points.pkl", SizeBytes: 256, ETag: "b", ObservedAt: now.Add(-time.Hour)},
				{Key: "scene-a/token-1/annotation.pkl", SizeBytes: 64, ETag: "annotation", ObservedAt: now.Add(-time.Hour)},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := PlanDatasetPublication(test.inventory, PlanOptions{Now: now, StableWindow: time.Minute, Rules: rules})
			if !errors.Is(err, ErrUnstableInventory) {
				t.Fatalf("error=%v want unstable inventory", err)
			}
		})
	}
}

func TestPlannerRejectsInvalidInputsAndMissingReferencedObjects(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	validInventory := observedBefore(now,
		object("scene-a/token-1/points.pkl", 128, "points"),
		object("scene-a/token-1/annotation.pkl", 64, "annotation"),
	)
	validRules := datasetRules(sample("token-1", "scene-a", "", SplitTrain,
		ruleObject("scene-a/token-1/points.pkl", ObjectRolePoints),
		ruleObject("scene-a/token-1/annotation.pkl", ObjectRoleAnnotation),
	))
	tests := []struct {
		name      string
		now       time.Time
		window    time.Duration
		inventory []InventoryObject
		rules     DatasetRules
		want      error
	}{
		{name: "zero now", now: time.Time{}, window: time.Minute, inventory: validInventory, rules: validRules, want: ErrInvalidPlanOptions},
		{name: "zero stable window", now: now, window: 0, inventory: validInventory, rules: validRules, want: ErrInvalidPlanOptions},
		{name: "unsafe object path", now: now, window: time.Minute, inventory: observedBefore(now, object("../private", 1, "etag")), rules: validRules, want: ErrInvalidInventory},
		{name: "empty etag", now: now, window: time.Minute, inventory: observedBefore(now, object("scene-a/token-1/points.pkl", 128, "")), rules: validRules, want: ErrInvalidInventory},
		{name: "unsafe rule path", now: now, window: time.Minute, inventory: validInventory, rules: datasetRules(sample("token-1", "scene-a", "", SplitTrain, ruleObject("../private", ObjectRolePoints), ruleObject("scene-a/token-1/annotation.pkl", ObjectRoleAnnotation))), want: ErrInvalidRules},
		{name: "unsafe scene identifier", now: now, window: time.Minute, inventory: validInventory, rules: datasetRules(sample("token-1", "../scene-a", "", SplitTrain, ruleObject("scene-a/token-1/points.pkl", ObjectRolePoints), ruleObject("scene-a/token-1/annotation.pkl", ObjectRoleAnnotation))), want: ErrInvalidRules},
		{name: "url-like token identifier", now: now, window: time.Minute, inventory: validInventory, rules: datasetRules(sample("https://token", "scene-a", "", SplitTrain, ruleObject("scene-a/token-1/points.pkl", ObjectRolePoints), ruleObject("scene-a/token-1/annotation.pkl", ObjectRoleAnnotation))), want: ErrInvalidRules},
		{name: "oversized publisher identifier", now: now, window: time.Minute, inventory: validInventory, rules: func() DatasetRules {
			rules := validRules
			rules.PublisherVersion = strings.Repeat("p", 256)
			return rules
		}(), want: ErrInvalidRules},
		{name: "missing referenced object", now: now, window: time.Minute, inventory: validInventory, rules: datasetRules(sample("token-1", "scene-a", "", SplitTrain, ruleObject("scene-a/token-1/missing.pkl", ObjectRolePoints), ruleObject("scene-a/token-1/annotation.pkl", ObjectRoleAnnotation))), want: ErrMissingObject},
		{name: "invalid point record width", now: now, window: time.Minute, inventory: validInventory, rules: func() DatasetRules {
			rules := validRules
			rules.PointRecordWidthBytes = 10
			return rules
		}(), want: ErrInvalidRules},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := PlanDatasetPublication(test.inventory, PlanOptions{Now: test.now, StableWindow: test.window, Rules: test.rules})
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want %v", err, test.want)
			}
		})
	}
}

func TestPlannerRejectsAmbiguousRulesAndInvalidInventoryBoundaries(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	baseInventory := observedBefore(now,
		object("scene-a/token-1/points.bin", 128, "points"),
		object("scene-a/token-1/annotation.pkl", 64, "annotation"),
	)
	baseSample := sample("token-1", "scene-a", "", SplitTrain,
		ruleObject("scene-a/token-1/points.bin", ObjectRolePoints),
		ruleObject("scene-a/token-1/annotation.pkl", ObjectRoleAnnotation),
	)
	tests := []struct {
		name      string
		inventory []InventoryObject
		rules     DatasetRules
		want      error
	}{
		{
			name: "duplicate token in one split",
			inventory: observedBefore(now,
				object("scene-a/token-1/points.bin", 128, "points-a"),
				object("scene-a/token-1/annotation.pkl", 64, "annotation-a"),
				object("scene-b/token-1/points.bin", 128, "points-b"),
				object("scene-b/token-1/annotation.pkl", 64, "annotation-b"),
			),
			rules: datasetRules(baseSample, sample("token-1", "scene-b", "", SplitTrain,
				ruleObject("scene-b/token-1/points.bin", ObjectRolePoints),
				ruleObject("scene-b/token-1/annotation.pkl", ObjectRoleAnnotation),
			)),
			want: ErrInvalidRules,
		},
		{
			name:      "one key has conflicting roles",
			inventory: baseInventory,
			rules: datasetRules(sample("token-1", "scene-a", "", SplitTrain,
				ruleObject("scene-a/token-1/points.bin", ObjectRolePoints),
				ruleObject("scene-a/token-1/points.bin", ObjectRoleAnnotation),
			)),
			want: ErrInvalidRules,
		},
		{
			name: "future observation",
			inventory: []InventoryObject{
				{Key: "scene-a/token-1/points.bin", SizeBytes: 128, ETag: "points", ObservedAt: now.Add(time.Second)},
				{Key: "scene-a/token-1/annotation.pkl", SizeBytes: 64, ETag: "annotation", ObservedAt: now.Add(-time.Hour)},
			},
			rules: datasetRules(baseSample),
			want:  ErrInvalidInventory,
		},
		{
			name: "zero byte points",
			inventory: observedBefore(now,
				object("scene-a/token-1/points.bin", 0, "points"),
				object("scene-a/token-1/annotation.pkl", 64, "annotation"),
			),
			rules: datasetRules(baseSample),
			want:  ErrInvalidPointCloudBytes,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := PlanDatasetPublication(test.inventory, PlanOptions{Now: now, StableWindow: time.Minute, Rules: test.rules})
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want %v", err, test.want)
			}
		})
	}
}

func TestPlannerRejectsStorageEstimateOverflow(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	rules := datasetRules(sample("token-1", "scene-a", "", SplitTrain,
		ruleObject("scene-a/token-1/points.bin", ObjectRolePoints),
		ruleObject("scene-a/token-1/annotation.pkl", ObjectRoleAnnotation),
	))
	_, err := PlanDatasetPublication(observedBefore(now,
		object("scene-a/token-1/points.bin", math.MaxInt64-15, "points"),
		object("scene-a/token-1/annotation.pkl", 64, "annotation"),
	), PlanOptions{Now: now, StableWindow: time.Minute, Rules: rules})
	if !errors.Is(err, ErrInvalidInventory) {
		t.Fatalf("error=%v want invalid inventory overflow", err)
	}
}

func TestPlannerAggregatesByScenePartitionAndReusesOnlyUnchangedShards(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	rules := datasetRules(
		sample("token-a", "scene-1", "partition-a", SplitTrain,
			ruleObject("scene-1/token-a/points.pkl", ObjectRolePoints),
			ruleObject("scene-1/token-a/annotation.pkl", ObjectRoleAnnotation),
		),
		sample("token-b", "scene-1", "partition-a", SplitTrain,
			ruleObject("scene-1/token-b/points.pkl", ObjectRolePoints),
			ruleObject("scene-1/token-b/annotation.pkl", ObjectRoleAnnotation),
		),
		sample("token-c", "scene-2", "partition-a", SplitTrain,
			ruleObject("scene-2/token-c/points.pkl", ObjectRolePoints),
			ruleObject("scene-2/token-c/annotation.pkl", ObjectRoleAnnotation),
		),
	)
	baseInventory := observedBefore(now,
		object("scene-1/token-a/points.pkl", 128, "pa"),
		object("scene-1/token-a/annotation.pkl", 64, "aa"),
		object("scene-1/token-b/points.pkl", 128, "pb"),
		object("scene-1/token-b/annotation.pkl", 64, "ab"),
		object("scene-2/token-c/points.pkl", 128, "pc"),
		object("scene-2/token-c/annotation.pkl", 64, "ac"),
	)
	initial, err := PlanDatasetPublication(baseInventory, PlanOptions{Now: now, StableWindow: time.Minute, Rules: rules})
	if err != nil {
		t.Fatalf("initial plan: %v", err)
	}
	if len(initial.AddedShards()) != 2 || initial.AddedShards()[0].SampleTokens[0] != "token-a" || initial.AddedShards()[0].SampleTokens[1] != "token-b" {
		t.Fatalf("expected scene/partition shard aggregation, got %+v", initial.AddedShards())
	}

	changedInventory := observedBefore(now,
		object("scene-1/token-a/points.pkl", 128, "pa"),
		object("scene-1/token-a/annotation.pkl", 64, "aa"),
		object("scene-1/token-b/points.pkl", 128, "pb"),
		object("scene-1/token-b/annotation.pkl", 64, "ab"),
		object("scene-2/token-c/points.pkl", 256, "pc-v2"),
		object("scene-2/token-c/annotation.pkl", 64, "ac"),
	)
	next, err := PlanDatasetPublication(changedInventory, PlanOptions{Now: now, StableWindow: time.Minute, Rules: rules, Previous: initial.Manifest()})
	if err != nil {
		t.Fatalf("changed plan: %v", err)
	}
	if next.DryRun().AddedShardCount != 1 || next.DryRun().ReusedShardCount != 1 || next.DryRun().EstimatedAddedStorageBytes != 320 {
		t.Fatalf("dry-run = %+v, want one changed scene shard", next.DryRun())
	}
	if len(next.ReusedShards()) != 1 || next.ReusedShards()[0].Scene != "scene-1" {
		t.Fatalf("unchanged scene was not reused: %+v", next.ReusedShards())
	}
	if len(next.AddedShards()) != 1 || next.AddedShards()[0].Scene != "scene-2" {
		t.Fatalf("changed scene was not repacked: %+v", next.AddedShards())
	}
}

func object(key string, size int64, etag string) InventoryObject {
	return InventoryObject{Key: key, SizeBytes: size, ETag: etag}
}

func observedBefore(now time.Time, objects ...InventoryObject) []InventoryObject {
	result := make([]InventoryObject, len(objects))
	for index, object := range objects {
		object.ObservedAt = now.Add(-time.Hour)
		result[index] = object
	}
	return result
}

func datasetRules(samples ...SampleRule) DatasetRules {
	return DatasetRules{
		SchemaVersion:         "s1h-pkl-v1",
		PublisherVersion:      "datasetpublisher-test-v1",
		PointRecordWidthBytes: 16,
		Samples:               samples,
	}
}

func datasetRulesWithMarker(marker string, samples ...SampleRule) DatasetRules {
	rules := datasetRules(samples...)
	rules.SuccessMarker = marker
	return rules
}

func sample(token, scene, partition string, split Split, objects ...SampleObjectRule) SampleRule {
	return SampleRule{Token: token, Scene: scene, Partition: partition, Split: split, Objects: objects}
}

func ruleObject(key string, role ObjectRole) SampleObjectRule {
	return SampleObjectRule{Key: key, Role: role}
}

func reverseObjects(objects []InventoryObject) []InventoryObject {
	result := append([]InventoryObject(nil), objects...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func reverseSamples(samples []SampleRule) []SampleRule {
	result := append([]SampleRule(nil), samples...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}
