package k8s

import (
	"reflect"
	"testing"
)

func TestCapacityTopologyCompressedSlices(t *testing.T) {
	for _, test := range []struct{
		name string
		topology map[string]any
		want map[string]int64
	}{
		{name:"multi node roots and universal count",topology:map[string]any{
			"levels":[]any{"zone","kubernetes.io/hostname"},
			"slices":[]any{map[string]any{
				"domainCount":int64(4),
				"podCounts":map[string]any{"universal":int64(1)},
				"valuesPerLevel":[]any{map[string]any{"universal":"zone-a"},map[string]any{"individual":map[string]any{"prefix":"172.28.1.","suffix":"","roots":[]any{"118","138","150","151"}}}},
			}},
		},want:map[string]int64{"172.28.1.118":1,"172.28.1.138":1,"172.28.1.150":1,"172.28.1.151":1}},
		{name:"individual counts and multiple slices",topology:map[string]any{
			"levels":[]any{"kubernetes.io/hostname"},
			"slices":[]any{
				map[string]any{"domainCount":int64(2),"podCounts":map[string]any{"individual":[]any{int64(2),int64(3)}},"valuesPerLevel":[]any{map[string]any{"individual":map[string]any{"prefix":"node-","suffix":".local","roots":[]any{"a","b"}}}}},
				map[string]any{"domainCount":int64(1),"podCounts":map[string]any{"universal":int64(1)},"valuesPerLevel":[]any{map[string]any{"universal":"node-a.local"}}},
			},
		},want:map[string]int64{"node-a.local":3,"node-b.local":3}},
	}{
		t.Run(test.name,func(t *testing.T){
			got,err:=capacityTopologyCounts(test.topology)
			if err!=nil || !reflect.DeepEqual(got,test.want){t.Fatalf("counts=%v err=%v want=%v",got,err,test.want)}
		})
	}
}

func TestCapacityTopologyCompressedSlicesRejectMalformedArrays(t *testing.T) {
	for _, slice:=range []map[string]any{
		{"domainCount":int64(100001),"podCounts":map[string]any{"universal":int64(1)},"valuesPerLevel":[]any{map[string]any{"universal":"node"}}},
		{"domainCount":int64(2),"podCounts":map[string]any{"individual":[]any{int64(1)}},"valuesPerLevel":[]any{map[string]any{"individual":map[string]any{"roots":[]any{"a","b"}}}}},
		{"domainCount":int64(2),"podCounts":map[string]any{"universal":int64(1)},"valuesPerLevel":[]any{map[string]any{"individual":map[string]any{"roots":[]any{"a"}}}}},
	}{
		_,err:=capacityTopologyCounts(map[string]any{"levels":[]any{"kubernetes.io/hostname"},"slices":[]any{slice}})
		if err==nil {t.Fatalf("malformed slice accepted: %v",slice)}
	}
}
