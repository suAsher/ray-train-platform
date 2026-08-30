package datasetpublisher

import (
	"errors"
	"testing"
	"time"
)

func TestManifestDigestIsIndependentOfInventoryAndRuleOrder(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	inventory := observedBefore(now,
		object("raw/s1h/scene-b/token-2/sensor.pkl", 128, "points-b"),
		object("raw/s1h/scene-a/token-1/labels.pkl", 64, "anno-a"),
		object("raw/s1h/scene-a/token-1/sensor.pkl", 128, "points-a"),
		object("raw/s1h/scene-b/token-2/labels.pkl", 64, "anno-b"),
	)
	rules := datasetRules(
		sample("token-2", "scene-b", "", SplitTrain,
			ruleObject("raw/s1h/scene-b/token-2/sensor.pkl", ObjectRolePoints),
			ruleObject("raw/s1h/scene-b/token-2/labels.pkl", ObjectRoleAnnotation),
		),
		sample("token-1", "scene-a", "", SplitTrain,
			ruleObject("raw/s1h/scene-a/token-1/sensor.pkl", ObjectRolePoints),
			ruleObject("raw/s1h/scene-a/token-1/labels.pkl", ObjectRoleAnnotation),
		),
	)

	first, err := PlanDatasetPublication(inventory, PlanOptions{Now: now, StableWindow: time.Minute, Rules: rules})
	if err != nil {
		t.Fatalf("plan first: %v", err)
	}
	reversedRules := rules
	reversedRules.Samples = reverseSamples(rules.Samples)
	second, err := PlanDatasetPublication(reverseObjects(inventory), PlanOptions{Now: now, StableWindow: time.Minute, Rules: reversedRules})
	if err != nil {
		t.Fatalf("plan reversed: %v", err)
	}
	if first.Manifest().Digest != second.Manifest().Digest {
		t.Fatalf("digest depends on order: %q != %q", first.Manifest().Digest, second.Manifest().Digest)
	}

	changedSchema := rules
	changedSchema.SchemaVersion = "s1h-v2"
	third, err := PlanDatasetPublication(inventory, PlanOptions{Now: now, StableWindow: time.Minute, Rules: changedSchema})
	if err != nil {
		t.Fatalf("plan changed schema: %v", err)
	}
	if first.Manifest().Digest == third.Manifest().Digest {
		t.Fatal("manifest digest did not include schema version")
	}
}

func TestManifestAndShardViewsAreDefensiveCopies(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	plan, err := PlanDatasetPublication(observedBefore(now,
		object("scene-a/token-1/points.pkl", 128, "points"),
		object("scene-a/token-1/annotation.pkl", 64, "annotation"),
	), PlanOptions{Now: now, StableWindow: time.Minute, Rules: datasetRules(
		sample("token-1", "scene-a", "", SplitTrain,
			ruleObject("scene-a/token-1/points.pkl", ObjectRolePoints),
			ruleObject("scene-a/token-1/annotation.pkl", ObjectRoleAnnotation),
		),
	)})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	manifest := plan.Manifest()
	manifest.Shards[0].ObjectKeys[0] = "mutated"
	manifest.Shards[0].SampleTokens[0] = "other-token"
	manifest.Objects[0].ETag = "mutated"
	manifest.Metadata["format"] = "mutated"
	added := plan.AddedShards()
	added[0].ObjectKeys[0] = "also-mutated"
	added[0].SampleTokens[0] = "also-mutated"

	fresh := plan.Manifest()
	if fresh.Shards[0].ObjectKeys[0] == "mutated" || fresh.Shards[0].SampleTokens[0] == "other-token" || fresh.Objects[0].ETag == "mutated" || fresh.Metadata["format"] == "mutated" {
		t.Fatalf("manifest leaked mutable state: %+v", fresh)
	}
	if plan.AddedShards()[0].ObjectKeys[0] == "also-mutated" || plan.AddedShards()[0].SampleTokens[0] == "also-mutated" {
		t.Fatalf("shards leaked mutable state: %+v", plan.AddedShards())
	}
}

func TestManifestRejectsInvalidPreviousManifestBeforeReuse(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	rules := datasetRules(sample("token-1", "scene-a", "", SplitTrain,
		ruleObject("scene-a/token-1/points.pkl", ObjectRolePoints),
		ruleObject("scene-a/token-1/annotation.pkl", ObjectRoleAnnotation),
	))
	initial, err := PlanDatasetPublication(observedBefore(now,
		object("scene-a/token-1/points.pkl", 128, "points"),
		object("scene-a/token-1/annotation.pkl", 64, "annotation"),
	), PlanOptions{Now: now, StableWindow: time.Minute, Rules: rules})
	if err != nil {
		t.Fatalf("initial plan: %v", err)
	}
	invalidPrevious := initial.Manifest()
	invalidPrevious.Digest = "not-the-real-digest"

	_, err = PlanDatasetPublication(observedBefore(now,
		object("scene-a/token-1/points.pkl", 128, "points"),
		object("scene-a/token-1/annotation.pkl", 64, "annotation"),
	), PlanOptions{Now: now, StableWindow: time.Minute, Rules: rules, Previous: invalidPrevious})
	if !errors.Is(err, ErrInvalidPreviousManifest) {
		t.Fatalf("error=%v want invalid previous manifest", err)
	}
}

func TestManifestRejectsTamperedPreviousShardIdentityAndObjects(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	rules := datasetRules(sample("token-1", "scene-a", "", SplitTrain,
		ruleObject("scene-a/token-1/points.bin", ObjectRolePoints),
		ruleObject("scene-a/token-1/annotation.pkl", ObjectRoleAnnotation),
	))
	inventory := observedBefore(now,
		object("scene-a/token-1/points.bin", 128, "points"),
		object("scene-a/token-1/annotation.pkl", 64, "annotation"),
	)
	initial, err := PlanDatasetPublication(inventory, PlanOptions{Now: now, StableWindow: time.Minute, Rules: rules})
	if err != nil {
		t.Fatalf("initial plan: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "shard ID", mutate: func(manifest *Manifest) { manifest.Shards[0].ID = "shard-wrong-object" }},
		{name: "missing object reference", mutate: func(manifest *Manifest) { manifest.Objects = manifest.Objects[:1] }},
		{name: "malformed object reference", mutate: func(manifest *Manifest) { manifest.Objects[0].Key = "../escape" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previous := initial.Manifest()
			test.mutate(&previous)
			_, err := PlanDatasetPublication(inventory, PlanOptions{Now: now, StableWindow: time.Minute, Rules: rules, Previous: previous})
			if !errors.Is(err, ErrInvalidPreviousManifest) {
				t.Fatalf("error=%v want invalid previous manifest", err)
			}
		})
	}
}

func TestManifestDigestIncludesPointRecordWidth(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	inventory := observedBefore(now,
		object("scene-a/token-1/points.bin", 128, "points"),
		object("scene-a/token-1/annotation.pkl", 64, "annotation"),
	)
	rules := datasetRules(sample("token-1", "scene-a", "", SplitTrain,
		ruleObject("scene-a/token-1/points.bin", ObjectRolePoints),
		ruleObject("scene-a/token-1/annotation.pkl", ObjectRoleAnnotation),
	))
	first, err := PlanDatasetPublication(inventory, PlanOptions{Now: now, StableWindow: time.Minute, Rules: rules})
	if err != nil {
		t.Fatalf("plan width 16: %v", err)
	}
	rules.PointRecordWidthBytes = 32
	second, err := PlanDatasetPublication(inventory, PlanOptions{Now: now, StableWindow: time.Minute, Rules: rules})
	if err != nil {
		t.Fatalf("plan width 32: %v", err)
	}
	if first.Manifest().Digest == second.Manifest().Digest {
		t.Fatal("manifest digest did not include point record width")
	}
}

func TestManifestValidationErrorsAreClassifiable(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		inventory []InventoryObject
		rules     DatasetRules
		want      error
	}{
		{
			name: "success marker does not bypass missing annotation",
			inventory: observedBefore(now,
				object("_SUCCESS", 0, "done"),
				object("scene-a/token-1/points.pkl", 128, "points"),
			),
			rules: datasetRulesWithMarker("_SUCCESS",
				sample("token-1", "scene-a", "", SplitTrain,
					ruleObject("scene-a/token-1/points.pkl", ObjectRolePoints),
				),
			),
			want: ErrMissingAnnotation,
		},
		{
			name: "train and validation tokens overlap",
			inventory: observedBefore(now,
				object("train-token/points.pkl", 128, "train-points"),
				object("train-token/annotation.pkl", 64, "train-annotation"),
				object("val-token/points.pkl", 128, "val-points"),
				object("val-token/annotation.pkl", 64, "val-annotation"),
			),
			rules: datasetRules(
				sample("shared-token", "scene-a", "", SplitTrain,
					ruleObject("train-token/points.pkl", ObjectRolePoints),
					ruleObject("train-token/annotation.pkl", ObjectRoleAnnotation),
				),
				sample("shared-token", "scene-b", "", SplitVal,
					ruleObject("val-token/points.pkl", ObjectRolePoints),
					ruleObject("val-token/annotation.pkl", ObjectRoleAnnotation),
				),
			),
			want: ErrSplitTokenOverlap,
		},
		{
			name: "point cloud byte length must divide configured record width",
			inventory: observedBefore(now,
				object("scene-a/token-1/points.pkl", 98, "points"),
				object("scene-a/token-1/annotation.pkl", 64, "annotation"),
			),
			rules: datasetRules(sample("token-1", "scene-a", "", SplitTrain,
				ruleObject("scene-a/token-1/points.pkl", ObjectRolePoints),
				ruleObject("scene-a/token-1/annotation.pkl", ObjectRoleAnnotation),
			)),
			want: ErrInvalidPointCloudBytes,
		},
		{
			name:      "empty sample set is rejected",
			inventory: observedBefore(now, object("unreferenced/object", 1, "etag")),
			rules:     datasetRules(),
			want:      ErrEmptySamples,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := PlanDatasetPublication(test.inventory, PlanOptions{Now: now, StableWindow: time.Minute, Rules: test.rules})
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want classification %v", err, test.want)
			}
		})
	}
}
