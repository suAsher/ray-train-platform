package nodeonboarding

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const emptyConfig = `{"nodePathMap":[{"node":"DEFAULT_PATH_FOR_NON_LISTED_NODES","paths":[]}]}`

func TestMergePreservesConfigAndIsIdempotent(t *testing.T) {
	input := []byte(`{"nodePathMap":[{"node":"DEFAULT_PATH_FOR_NON_LISTED_NODES","paths":[],"extra":true},{"node":"old.example","paths":["/data1/ray-cache"],"metadata":{"x":9007199254740993}}],"other":{"enabled":true,"number":9007199254740993}}`)
	original := bytes.Clone(input)
	output, err := MergeNodePathMap(input, "new.example", "/data1/ray-cache")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(input, original) {
		t.Fatal("mutated input")
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["other"]) != `{"enabled":true,"number":9007199254740993}` {
		t.Fatalf("lost top-level data: %s", output)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(got["nodePathMap"], &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || string(entries[0]["extra"]) != "true" || string(entries[1]["metadata"]) != `{"x":9007199254740993}` {
		t.Fatalf("lost mappings: %s", output)
	}
	again, err := MergeNodePathMap(output, "new.example", "/data1/ray-cache")
	if err != nil || !bytes.Equal(output, again) {
		t.Fatalf("not idempotent: %s, %v", again, err)
	}
	if CacheReadyLabel != "platform.wellspiking.ai/cache-ready" {
		t.Fatal(CacheReadyLabel)
	}
}

func TestMergeRejectsUnsafeInput(t *testing.T) {
	cases := []string{
		``, `null`, `[]`, `{}`, `{"nodePathMap":null}`, `{"nodePathMap":{}}`, `{"nodePathMap":[]}`,
		emptyConfig + `{}`, emptyConfig + ` garbage`,
		`{"nodePathMap":[],"nodePathMap":[]}`,
		`{"nodePathMap":[null]}`, `{"nodePathMap":[{}]}`,
		`{"nodePathMap":[{"node":"DEFAULT_PATH_FOR_NON_LISTED_NODES"}]}`,
		strings.Replace(emptyConfig, `"paths":[]`, `"paths":null`, 1),
		strings.Replace(emptyConfig, `"paths":[]`, `"paths":["/data1/ray-cache"]`, 1),
	}
	for _, entry := range []string{
		`{"node":"old","paths":[]}`, `{"node":"old","paths":["/data2/ray-cache"]}`,
		`{"node":"old","paths":["/data1/ray-cache","/data1/ray-cache"]}`,
		`{"node":"old","paths":[42]}`, `{"node":"UPPER","paths":["/data1/ray-cache"]}`,
		`{"node":"a..b","paths":["/data1/ray-cache"]}`,
		`{"node":"old","node":"other","paths":["/data1/ray-cache"]}`,
		`{"node":"old","paths":["/data1/ray-cache"]},{"node":"old","paths":["/data1/ray-cache"]}`,
		`{"node":"DEFAULT_PATH_FOR_NON_LISTED_NODES","paths":[]}`,
	} {
		cases = append(cases, strings.TrimSuffix(emptyConfig, "]}")+","+entry+"]}")
	}
	for _, config := range cases {
		t.Run(config, func(t *testing.T) {
			input := []byte(config)
			original := bytes.Clone(input)
			got, err := MergeNodePathMap(input, "new", "/data1/ray-cache")
			if err == nil || got != nil {
				t.Fatalf("accepted unsafe config: %s", got)
			}
			if !bytes.Equal(input, original) {
				t.Fatal("mutated input on error")
			}
		})
	}
}

func TestMergeValidatesArguments(t *testing.T) {
	for _, node := range []string{"", "UPPER", "a/b", "a..b", "-node", "node-", strings.Repeat("a", 254), strings.Repeat("a", 64)} {
		if _, err := MergeNodePathMap([]byte(emptyConfig), node, "/data1/ray-cache"); err == nil {
			t.Errorf("accepted node %q", node)
		}
	}
	for _, root := range []string{"", "/", "/data1/ray-cache/", "/data1/ray-cache/../other"} {
		if _, err := MergeNodePathMap([]byte(emptyConfig), "node", root); err == nil {
			t.Errorf("accepted root %q", root)
		}
	}
	if _, err := MergeNodePathMap([]byte(emptyConfig), "node", "/data2/ray-cache"); err != nil {
		t.Fatal(err)
	}
}
