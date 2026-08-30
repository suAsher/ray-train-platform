package datasetpublisher

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

var publisherIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

type PlanOptions struct {
	Now          time.Time
	StableWindow time.Duration
	Rules        DatasetRules
	Previous     Manifest
}

type DryRunSummary struct {
	AddedShardCount            int
	ReusedShardCount           int
	EstimatedAddedStorageBytes int64
}

type Plan struct {
	manifest Manifest
	added    []Shard
	reused   []Shard
	dryRun   DryRunSummary
}

func (plan Plan) Manifest() Manifest {
	return cloneManifest(plan.manifest)
}

func (plan Plan) AddedShards() []Shard {
	return cloneShards(plan.added)
}

func (plan Plan) ReusedShards() []Shard {
	return cloneShards(plan.reused)
}

func (plan Plan) DryRun() DryRunSummary {
	return plan.dryRun
}

func PlanDatasetPublication(inventory []InventoryObject, options PlanOptions) (Plan, error) {
	if options.Now.IsZero() || options.StableWindow <= 0 {
		return Plan{}, &ValidationError{Kind: ErrInvalidPlanOptions, Message: "now and stable window are required"}
	}
	rules, err := validateRules(options.Rules)
	if err != nil {
		return Plan{}, err
	}
	observations, err := validateInventory(inventory, options.Now)
	if err != nil {
		return Plan{}, err
	}
	if rules.SuccessMarker != "" {
		if _, exists := observations[rules.SuccessMarker]; !exists {
			return Plan{}, &ValidationError{Kind: ErrMissingSuccessMarker, Key: rules.SuccessMarker}
		}
	}
	previous := Manifest{}
	if !isZeroManifest(options.Previous) {
		previous, err = validatePreviousManifest(options.Previous, rules)
		if err != nil {
			return Plan{}, err
		}
	}
	objects, groups, err := buildShardGroups(observations, rules, options)
	if err != nil {
		return Plan{}, err
	}
	shards, err := planShards(groups, previous, rules)
	if err != nil {
		return Plan{}, err
	}
	manifest := Manifest{
		SchemaVersion:         rules.SchemaVersion,
		PublisherVersion:      rules.PublisherVersion,
		PointRecordWidthBytes: rules.PointRecordWidthBytes,
		Shards:                cloneShards(shards),
		Objects:               append([]ObjectRef(nil), objects...),
		Metadata:              map[string]string{"format": "datasetpublisher/v1"},
	}
	manifest.Digest = manifestDigest(manifest.SchemaVersion, manifest.PublisherVersion, manifest.PointRecordWidthBytes, manifest.Shards)
	added, reused, addedBytes, err := splitPlannedShards(shards)
	if err != nil {
		return Plan{}, err
	}
	return Plan{
		manifest: cloneManifest(manifest),
		added:    added,
		reused:   reused,
		dryRun: DryRunSummary{
			AddedShardCount:            len(added),
			ReusedShardCount:           len(reused),
			EstimatedAddedStorageBytes: addedBytes,
		},
	}, nil
}

type inventoryObservations map[string][]InventoryObject

type shardGroup struct {
	split        Split
	scene        string
	partition    string
	sampleTokens map[string]struct{}
	objects      []ObjectRef
	objectBytes  map[string]int64
}

func validateRules(rules DatasetRules) (DatasetRules, error) {
	if !validIdentifier(rules.SchemaVersion) || !validIdentifier(rules.PublisherVersion) {
		return DatasetRules{}, &ValidationError{Kind: ErrInvalidRules, Message: "schema and publisher versions are required"}
	}
	if rules.PointRecordWidthBytes <= 0 || rules.PointRecordWidthBytes%4 != 0 {
		return DatasetRules{}, &ValidationError{Kind: ErrInvalidRules, Message: "point record width must be a positive multiple of 4"}
	}
	if rules.SuccessMarker != "" && !validRelativePath(rules.SuccessMarker) {
		return DatasetRules{}, &ValidationError{Kind: ErrInvalidRules, Key: rules.SuccessMarker, Message: "success marker path is invalid"}
	}
	if len(rules.Samples) == 0 {
		return DatasetRules{}, &ValidationError{Kind: ErrEmptySamples}
	}
	clone := rules
	clone.Samples = make([]SampleRule, len(rules.Samples))
	trainTokens := map[string]struct{}{}
	valTokens := map[string]struct{}{}
	seenSplitTokens := map[string]struct{}{}
	objectRoles := map[string]ObjectRole{}
	for sampleIndex, sample := range rules.Samples {
		if !validIdentifier(sample.Token) || !validIdentifier(sample.Scene) || !validOptionalIdentifier(sample.Partition) {
			return DatasetRules{}, &ValidationError{Kind: ErrInvalidRules, Token: sample.Token, Message: "sample identity is invalid"}
		}
		if !validSplit(sample.Split) {
			return DatasetRules{}, &ValidationError{Kind: ErrInvalidRules, Token: sample.Token, Message: "sample split is invalid"}
		}
		if len(sample.Objects) == 0 {
			return DatasetRules{}, &ValidationError{Kind: ErrInvalidRules, Token: sample.Token, Message: "sample objects are required"}
		}
		splitToken := string(sample.Split) + "\x00" + sample.Token
		if _, exists := seenSplitTokens[splitToken]; exists {
			return DatasetRules{}, &ValidationError{Kind: ErrInvalidRules, Token: sample.Token, Message: "duplicate sample token in split"}
		}
		seenSplitTokens[splitToken] = struct{}{}
		if sample.Split == SplitTrain {
			trainTokens[sample.Token] = struct{}{}
		}
		if sample.Split == SplitVal {
			valTokens[sample.Token] = struct{}{}
		}
		hasPoints := false
		hasAnnotation := false
		seenObjects := map[string]struct{}{}
		copiedObjects := make([]SampleObjectRule, len(sample.Objects))
		for objectIndex, object := range sample.Objects {
			if !validRelativePath(object.Key) {
				return DatasetRules{}, &ValidationError{Kind: ErrInvalidRules, Key: object.Key, Message: "object path is invalid"}
			}
			if !validRole(object.Role) {
				return DatasetRules{}, &ValidationError{Kind: ErrInvalidRules, Key: object.Key, Message: "object role is invalid"}
			}
			if _, exists := seenObjects[object.Key]; exists {
				return DatasetRules{}, &ValidationError{Kind: ErrInvalidRules, Key: object.Key, Message: "duplicate sample object"}
			}
			seenObjects[object.Key] = struct{}{}
			if previousRole, exists := objectRoles[object.Key]; exists && previousRole != object.Role {
				return DatasetRules{}, &ValidationError{Kind: ErrInvalidRules, Key: object.Key, Message: "object role conflicts across samples"}
			}
			objectRoles[object.Key] = object.Role
			hasPoints = hasPoints || object.Role == ObjectRolePoints
			hasAnnotation = hasAnnotation || object.Role == ObjectRoleAnnotation
			copiedObjects[objectIndex] = object
		}
		if !hasPoints {
			return DatasetRules{}, &ValidationError{Kind: ErrInvalidRules, Token: sample.Token, Message: "points object is required"}
		}
		if !hasAnnotation {
			return DatasetRules{}, &ValidationError{Kind: ErrMissingAnnotation, Token: sample.Token}
		}
		sample.Objects = copiedObjects
		clone.Samples[sampleIndex] = sample
	}
	for token := range trainTokens {
		if _, exists := valTokens[token]; exists {
			return DatasetRules{}, &ValidationError{Kind: ErrSplitTokenOverlap, Token: token}
		}
	}
	return clone, nil
}

func validateInventory(inventory []InventoryObject, now time.Time) (inventoryObservations, error) {
	byKey := make(inventoryObservations, len(inventory))
	for _, object := range inventory {
		if !validRelativePath(object.Key) || object.SizeBytes < 0 || object.ObservedAt.IsZero() || object.ObservedAt.After(now) || !validETag(object.ETag) {
			return nil, &ValidationError{Kind: ErrInvalidInventory, Key: object.Key, Message: "inventory object is invalid"}
		}
		byKey[object.Key] = append(byKey[object.Key], object)
	}
	for key, observations := range byKey {
		sort.SliceStable(observations, func(left, right int) bool {
			return observations[left].ObservedAt.Before(observations[right].ObservedAt)
		})
		for index := 1; index < len(observations); index++ {
			previous := observations[index-1]
			current := observations[index]
			if current.ObservedAt.Equal(previous.ObservedAt) && (current.SizeBytes != previous.SizeBytes || current.ETag != previous.ETag) {
				return nil, &ValidationError{Kind: ErrUnstableInventory, Key: key}
			}
		}
		byKey[key] = observations
	}
	return byKey, nil
}

func validatePreviousManifest(previous Manifest, rules DatasetRules) (Manifest, error) {
	manifest := cloneManifest(previous)
	if manifest.SchemaVersion != rules.SchemaVersion || manifest.PublisherVersion != rules.PublisherVersion || manifest.PointRecordWidthBytes != rules.PointRecordWidthBytes {
		return Manifest{}, &ValidationError{Kind: ErrInvalidPreviousManifest, Message: "previous manifest version does not match rules"}
	}
	if len(manifest.Metadata) != 1 || manifest.Metadata["format"] != "datasetpublisher/v1" {
		return Manifest{}, &ValidationError{Kind: ErrInvalidPreviousManifest, Message: "previous manifest format is invalid"}
	}
	if manifest.Digest == "" || manifest.Digest != manifestDigest(manifest.SchemaVersion, manifest.PublisherVersion, manifest.PointRecordWidthBytes, manifest.Shards) {
		return Manifest{}, &ValidationError{Kind: ErrInvalidPreviousManifest, Message: "previous manifest digest is invalid"}
	}
	groups, err := previousManifestGroups(manifest.Objects)
	if err != nil {
		return Manifest{}, err
	}
	seenGroups := make(map[string]struct{}, len(manifest.Shards))
	for _, shard := range manifest.Shards {
		if !validDigest(shard.Digest) || shard.ID != shardIDForDigest(shard.Digest) || !validSplit(shard.Split) || !validIdentifier(shard.Scene) || !validOptionalIdentifier(shard.Partition) || len(shard.SampleTokens) == 0 || len(shard.ObjectKeys) == 0 || shard.InputBytes <= 0 {
			return Manifest{}, &ValidationError{Kind: ErrInvalidPreviousManifest, Message: "previous shard is invalid"}
		}
		for _, token := range shard.SampleTokens {
			if !validIdentifier(token) {
				return Manifest{}, &ValidationError{Kind: ErrInvalidPreviousManifest, Token: token}
			}
		}
		for _, key := range shard.ObjectKeys {
			if !validRelativePath(key) {
				return Manifest{}, &ValidationError{Kind: ErrInvalidPreviousManifest, Key: key}
			}
		}
		groupKey := sampleGroupKey(shard.Split, shard.Scene, shard.Partition)
		if _, duplicate := seenGroups[groupKey]; duplicate {
			return Manifest{}, &ValidationError{Kind: ErrInvalidPreviousManifest, Message: "previous manifest has duplicate scene shards"}
		}
		seenGroups[groupKey] = struct{}{}
		group, exists := groups[groupKey]
		if !exists {
			return Manifest{}, &ValidationError{Kind: ErrInvalidPreviousManifest, Message: "previous shard has no matching objects"}
		}
		tokens := sortedMapKeys(group.sampleTokens)
		objectKeys := sortedObjectKeys(group.objectBytes)
		inputBytes, sumErr := sumObjectBytes(group.objectBytes)
		if sumErr != nil {
			return Manifest{}, &ValidationError{Kind: ErrInvalidPreviousManifest, Message: "previous shard byte estimate is invalid"}
		}
		expectedDigest := shardDigest(rules.SchemaVersion, rules.PublisherVersion, rules.PointRecordWidthBytes, group, tokens, objectKeys)
		if shard.Digest != expectedDigest || shard.InputBytes != inputBytes || !equalStrings(shard.SampleTokens, tokens) || !equalStrings(shard.ObjectKeys, objectKeys) {
			return Manifest{}, &ValidationError{Kind: ErrInvalidPreviousManifest, Message: "previous shard does not match its object manifest"}
		}
	}
	if len(seenGroups) != len(groups) {
		return Manifest{}, &ValidationError{Kind: ErrInvalidPreviousManifest, Message: "previous manifest has unassigned objects"}
	}
	return manifest, nil
}

func previousManifestGroups(objects []ObjectRef) (map[string]shardGroup, error) {
	groups := make(map[string]*shardGroup)
	seenRefs := make(map[string]struct{}, len(objects))
	roles := make(map[string]ObjectRole)
	for _, object := range objects {
		if !validRelativePath(object.Key) || !validRole(object.Role) || !validSplit(object.Split) || !validIdentifier(object.Token) || !validIdentifier(object.Scene) || !validOptionalIdentifier(object.Partition) || object.SizeBytes < 0 || !validETag(object.ETag) {
			return nil, &ValidationError{Kind: ErrInvalidPreviousManifest, Key: object.Key, Message: "previous object reference is invalid"}
		}
		if previousRole, exists := roles[object.Key]; exists && previousRole != object.Role {
			return nil, &ValidationError{Kind: ErrInvalidPreviousManifest, Key: object.Key, Message: "previous object roles conflict"}
		}
		roles[object.Key] = object.Role
		refKey := sampleGroupKey(object.Split, object.Scene, object.Partition) + "\x00" + object.Token + "\x00" + string(object.Role) + "\x00" + object.Key
		if _, duplicate := seenRefs[refKey]; duplicate {
			return nil, &ValidationError{Kind: ErrInvalidPreviousManifest, Key: object.Key, Message: "previous object reference is duplicated"}
		}
		seenRefs[refKey] = struct{}{}
		groupKey := sampleGroupKey(object.Split, object.Scene, object.Partition)
		group := groups[groupKey]
		if group == nil {
			group = &shardGroup{split: object.Split, scene: object.Scene, partition: object.Partition, sampleTokens: map[string]struct{}{}, objectBytes: map[string]int64{}}
			groups[groupKey] = group
		}
		group.sampleTokens[object.Token] = struct{}{}
		if size, exists := group.objectBytes[object.Key]; exists && size != object.SizeBytes {
			return nil, &ValidationError{Kind: ErrInvalidPreviousManifest, Key: object.Key, Message: "previous object sizes conflict"}
		}
		group.objectBytes[object.Key] = object.SizeBytes
		group.objects = append(group.objects, object)
	}
	result := make(map[string]shardGroup, len(groups))
	for key, group := range groups {
		sortObjectRefs(group.objects)
		result[key] = *group
	}
	return result, nil
}

func buildShardGroups(observations inventoryObservations, rules DatasetRules, options PlanOptions) ([]ObjectRef, []shardGroup, error) {
	groupsByKey := map[string]*shardGroup{}
	objects := make([]ObjectRef, 0)
	for _, sample := range rules.Samples {
		groupKey := sampleGroupKey(sample.Split, sample.Scene, sample.Partition)
		group := groupsByKey[groupKey]
		if group == nil {
			group = &shardGroup{
				split:        sample.Split,
				scene:        sample.Scene,
				partition:    sample.Partition,
				sampleTokens: map[string]struct{}{},
				objectBytes:  map[string]int64{},
			}
			groupsByKey[groupKey] = group
		}
		group.sampleTokens[sample.Token] = struct{}{}
		for _, objectRule := range sample.Objects {
			inventoryObject, err := stableObjectForKey(observations, objectRule.Key, options)
			if err != nil {
				return nil, nil, err
			}
			if objectRule.Role == ObjectRolePoints && (inventoryObject.SizeBytes == 0 || inventoryObject.SizeBytes%rules.PointRecordWidthBytes != 0) {
				return nil, nil, &ValidationError{Kind: ErrInvalidPointCloudBytes, Key: objectRule.Key}
			}
			ref := ObjectRef{
				Key:       objectRule.Key,
				Role:      objectRule.Role,
				Split:     sample.Split,
				Token:     sample.Token,
				Scene:     sample.Scene,
				Partition: sample.Partition,
				SizeBytes: inventoryObject.SizeBytes,
				ETag:      inventoryObject.ETag,
			}
			group.objects = append(group.objects, ref)
			if _, exists := group.objectBytes[ref.Key]; !exists {
				group.objectBytes[ref.Key] = ref.SizeBytes
			}
			objects = append(objects, ref)
		}
	}
	sortObjectRefs(objects)
	groups := make([]shardGroup, 0, len(groupsByKey))
	for _, group := range groupsByKey {
		sortObjectRefs(group.objects)
		groups = append(groups, *group)
	}
	sort.Slice(groups, func(left, right int) bool {
		return compareGroup(groups[left], groups[right]) < 0
	})
	return objects, groups, nil
}

func stableObjectForKey(observations inventoryObservations, key string, options PlanOptions) (InventoryObject, error) {
	values, exists := observations[key]
	if !exists {
		return InventoryObject{}, &ValidationError{Kind: ErrMissingObject, Key: key}
	}
	cutoff := options.Now.Add(-options.StableWindow)
	baselineIndex := -1
	for index, observation := range values {
		if observation.ObservedAt.After(cutoff) {
			continue
		}
		baselineIndex = index
	}
	if baselineIndex < 0 {
		return InventoryObject{}, &ValidationError{Kind: ErrUnstableInventory, Key: key}
	}
	baseline := values[baselineIndex]
	latest := baseline
	for _, observation := range values[baselineIndex:] {
		if observation.SizeBytes != baseline.SizeBytes || observation.ETag != baseline.ETag {
			return InventoryObject{}, &ValidationError{Kind: ErrUnstableInventory, Key: key}
		}
		if observation.ObservedAt.After(latest.ObservedAt) {
			latest = observation
		}
	}
	return latest, nil
}

func planShards(groups []shardGroup, previous Manifest, rules DatasetRules) ([]Shard, error) {
	previousByDigest := map[string]Shard{}
	for _, shard := range previous.Shards {
		previousByDigest[shardReuseKey(shard.Split, shard.Scene, shard.Partition, shard.Digest)] = shard
	}
	shards := make([]Shard, 0, len(groups))
	for _, group := range groups {
		tokens := sortedMapKeys(group.sampleTokens)
		objectKeys := sortedObjectKeys(group.objectBytes)
		inputBytes, err := sumObjectBytes(group.objectBytes)
		if err != nil {
			return nil, err
		}
		digest := shardDigest(rules.SchemaVersion, rules.PublisherVersion, rules.PointRecordWidthBytes, group, tokens, objectKeys)
		shard := Shard{
			ID:           shardIDForDigest(digest),
			Split:        group.split,
			Scene:        group.scene,
			Partition:    group.partition,
			SampleTokens: tokens,
			Digest:       digest,
			ObjectKeys:   objectKeys,
			InputBytes:   inputBytes,
		}
		if previousShard, exists := previousByDigest[shardReuseKey(group.split, group.scene, group.partition, digest)]; exists {
			shard.ID = previousShard.ID
			shard.Reused = true
		}
		shards = append(shards, shard)
	}
	return shards, nil
}

func splitPlannedShards(shards []Shard) ([]Shard, []Shard, int64, error) {
	added := make([]Shard, 0)
	reused := make([]Shard, 0)
	var addedBytes int64
	for _, shard := range shards {
		if shard.Reused {
			reused = append(reused, shard)
			continue
		}
		added = append(added, shard)
		if shard.InputBytes > math.MaxInt64-addedBytes {
			return nil, nil, 0, &ValidationError{Kind: ErrInvalidInventory, Message: "dataset byte estimate overflows int64"}
		}
		addedBytes += shard.InputBytes
	}
	return cloneShards(added), cloneShards(reused), addedBytes, nil
}

func shardDigest(schemaVersion, publisherVersion string, pointRecordWidthBytes int64, group shardGroup, tokens, objectKeys []string) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "shard\x00%s\x00%s\x00%d\x00%s\x00%s\x00%s\x00", schemaVersion, publisherVersion, pointRecordWidthBytes, group.split, group.scene, group.partition)
	for _, token := range tokens {
		_, _ = fmt.Fprintf(hash, "token\x00%s\x00", token)
	}
	for _, key := range objectKeys {
		_, _ = fmt.Fprintf(hash, "key\x00%s\x00%d\x00", key, group.objectBytes[key])
	}
	for _, object := range group.objects {
		_, _ = fmt.Fprintf(hash, "object\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s\x00", object.Token, object.Role, object.Key, object.Scene, object.SizeBytes, object.ETag)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func manifestDigest(schemaVersion, publisherVersion string, pointRecordWidthBytes int64, shards []Shard) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "manifest\x00%s\x00%s\x00%d\x00", schemaVersion, publisherVersion, pointRecordWidthBytes)
	sorted := cloneShards(shards)
	sort.Slice(sorted, func(left, right int) bool {
		if sorted[left].Split != sorted[right].Split {
			return sorted[left].Split < sorted[right].Split
		}
		if sorted[left].Scene != sorted[right].Scene {
			return sorted[left].Scene < sorted[right].Scene
		}
		if sorted[left].Partition != sorted[right].Partition {
			return sorted[left].Partition < sorted[right].Partition
		}
		return sorted[left].Digest < sorted[right].Digest
	})
	for _, shard := range sorted {
		_, _ = fmt.Fprintf(hash, "shard\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00", shard.ID, shard.Split, shard.Scene, shard.Partition, shard.Digest, shard.InputBytes)
		for _, token := range shard.SampleTokens {
			_, _ = fmt.Fprintf(hash, "token\x00%s\x00", token)
		}
		for _, key := range shard.ObjectKeys {
			_, _ = fmt.Fprintf(hash, "key\x00%s\x00", key)
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func validRelativePath(value string) bool {
	if value == "" || len(value) > 4096 || strings.TrimSpace(value) != value || strings.Contains(value, "\\") || strings.Contains(value, "://") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	if path.IsAbs(value) || path.Clean(value) != value {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func validETag(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 1024 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validIdentifier(value string) bool {
	return publisherIdentifierPattern.MatchString(value)
}

func validOptionalIdentifier(value string) bool {
	return value == "" || validIdentifier(value)
}

func validRole(role ObjectRole) bool {
	return role == ObjectRolePoints || role == ObjectRoleAnnotation || role == ObjectRoleAuxiliary
}

func validSplit(split Split) bool {
	return split == SplitTrain || split == SplitVal || split == SplitTest
}

func isZeroManifest(manifest Manifest) bool {
	return manifest.SchemaVersion == "" &&
		manifest.PublisherVersion == "" &&
		manifest.PointRecordWidthBytes == 0 &&
		manifest.Digest == "" &&
		len(manifest.Shards) == 0 &&
		len(manifest.Objects) == 0 &&
		len(manifest.Metadata) == 0
}

func sampleGroupKey(split Split, scene, partition string) string {
	return string(split) + "\x00" + scene + "\x00" + partition
}

func shardReuseKey(split Split, scene, partition, digest string) string {
	return sampleGroupKey(split, scene, partition) + "\x00" + digest
}

func compareGroup(left, right shardGroup) int {
	if left.split != right.split {
		return strings.Compare(string(left.split), string(right.split))
	}
	if left.scene != right.scene {
		return strings.Compare(left.scene, right.scene)
	}
	return strings.Compare(left.partition, right.partition)
}

func sortedMapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedObjectKeys(values map[string]int64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sumObjectBytes(values map[string]int64) (int64, error) {
	var total int64
	for _, value := range values {
		if value > math.MaxInt64-total {
			return 0, &ValidationError{Kind: ErrInvalidInventory, Message: "shard byte estimate overflows int64"}
		}
		total += value
	}
	return total, nil
}

func shardIDForDigest(digest string) string {
	if len(digest) < 16 {
		return ""
	}
	return "shard-" + digest[:16]
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sortObjectRefs(objects []ObjectRef) {
	sort.Slice(objects, func(left, right int) bool {
		if objects[left].Split != objects[right].Split {
			return objects[left].Split < objects[right].Split
		}
		if objects[left].Scene != objects[right].Scene {
			return objects[left].Scene < objects[right].Scene
		}
		if objects[left].Partition != objects[right].Partition {
			return objects[left].Partition < objects[right].Partition
		}
		if objects[left].Token != objects[right].Token {
			return objects[left].Token < objects[right].Token
		}
		if objects[left].Role != objects[right].Role {
			return objects[left].Role < objects[right].Role
		}
		return objects[left].Key < objects[right].Key
	})
}
