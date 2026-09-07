package objectstore

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

var cleanupIdentity = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

// PurgeFailedPublicationObjects never scans shared shard or receipt directories.
// The caller must fence the FAILED version and establish publisher quiescence.
// Enumeration finishes and validates every returned key before any deletion.
func (s *TOSStore) PurgeFailedPublicationObjects(ctx context.Context, bucket, root, datasetID, versionID string, runIDs []string) (int, error) {
	if s == nil || bucket == "" || s.bucket != bucket {
		return 0, ErrUnavailable
	}
	clean, err := cleanPublicationObjectKey(root)
	if err != nil || clean != root || !strings.HasSuffix(root, "/platform/datasets") {
		return 0, fmt.Errorf("invalid cleanup root")
	}
	for _, id := range append([]string{datasetID, versionID}, runIDs...) {
		if !cleanupIdentity.MatchString(id) || id == "." || id == ".." {
			return 0, fmt.Errorf("invalid cleanup identity")
		}
	}
	if len(runIDs) > 100 {
		return 0, fmt.Errorf("cleanup run limit exceeded")
	}
	lister, ok := s.client.(tosArtifactClient)
	if !ok {
		return 0, ErrUnavailable
	}
	deleter, ok := s.client.(tosDeleteClient)
	if !ok {
		return 0, ErrUnavailable
	}
	base := root + "/" + datasetID + "/"
	manifest := base + "manifests/" + versionID + ".parquet"
	prefixes := []string{base + "publication/" + versionID + "/"}
	for _, id := range runIDs {
		prefixes = append(prefixes, base+"temp/"+id+"/")
	}
	keys := []string{}
	seen := map[string]bool{}
	// Include exact manifest only after Head confirms it exists.
	if _, err := s.client.Head(ctx, bucket, manifest); err == nil {
		keys = append(keys, manifest)
		seen[manifest] = true
	} else if err != ErrNotFound {
		return 0, ErrUnavailable
	}
	for _, prefix := range prefixes {
		cursor := ""
		cursors := map[string]bool{}
		for {
			page, err := lister.ListArtifacts(ctx, tosArtifactListRequest{Bucket: bucket, Prefix: prefix, MaxKeys: 1000, ContinuationToken: cursor})
			if err != nil || len(page.Directories) > 0 {
				return 0, ErrUnavailable
			}
			for _, obj := range page.Objects {
				if _, err := cleanPublicationObjectKey(obj.Key); err != nil || !strings.HasPrefix(obj.Key, prefix) || obj.SizeBytes < 0 {
					return 0, ErrUnavailable
				}
				if !seen[obj.Key] {
					keys = append(keys, obj.Key)
					seen[obj.Key] = true
				}
				if len(keys) > 10000 {
					return 0, fmt.Errorf("cleanup object limit exceeded")
				}
			}
			if page.NextContinuationToken == "" {
				break
			}
			if cursors[page.NextContinuationToken] || len(cursors) >= 100 {
				return 0, ErrUnavailable
			}
			cursors[page.NextContinuationToken] = true
			cursor = page.NextContinuationToken
		}
	}
	deleted := 0
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		if err := deleter.DeleteObject(ctx, bucket, key); err != nil && err != ErrNotFound {
			return deleted, ErrUnavailable
		}
		deleted++
	}
	return deleted, nil
}
