package api

import (
	ss "ray-train-platform-backend/storagesync"
	"testing"
)

func TestStorageSyncMetadataConfinementIncludesSeparatorAndOperation(t *testing.T) {
	spec := ss.WorkSpec{Mappings: []ss.ResolvedMapping{{Source: ss.ResolvedLocation{Kind: "TOS", Bucket: "bucket", Prefix: "root/a"}, Destination: ss.ResolvedLocation{Kind: "TOS", Bucket: "bucket", Prefix: "root/target"}}}}
	for _, tc := range []struct {
		op, bucket, value string
		want              bool
	}{{"head", "bucket", "root/a", true}, {"head", "bucket", "root/a/file", true}, {"list", "bucket", "root/a/", true}, {"browse", "bucket", "root/target", true}, {"read", "bucket", "root/ab/private", false}, {"list", "bucket", "root/", false}, {"head", "other", "root/a", false}, {"delete", "bucket", "root/a", false}, {"head", "bucket", "root/a/../private", false}, {"head", "bucket", "root/a/%2e%2e/private", false}} {
		request := storageSyncMetadataRequest{Operation: tc.op, Bucket: tc.bucket, Key: tc.value, Prefix: tc.value}
		if got := storageSyncMetadataAllowed(spec, request); got != tc.want {
			t.Errorf("%#v allowed=%v", tc, got)
		}
	}
}
