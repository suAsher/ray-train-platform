package k8s

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCapacityWorkloadJobIDPrefersStablePlatformIdentity(t *testing.T) {
	for _, metadataLabel:=range []bool{false,true} {
		w:=*capacityTestWorkload("wl-managed","node-a","workers",1,"8",true)
		w.SetOwnerReferences([]metav1.OwnerReference{{Kind:"RayJob",Name:"job-stable-a2"}})
		if metadataLabel {w.SetLabels(map[string]string{"platform_job_id":"job-stable"})} else {
			_ = unstructured.SetNestedSlice(w.Object,[]any{map[string]any{"name":"workers","template":map[string]any{"metadata":map[string]any{"labels":map[string]any{"platform_job_id":"job-stable"}}}}},"spec","podSets")
		}
		if got:=capacityWorkloadJobID(w);got!="job-stable" {t.Fatalf("managed retry stable ID=%q, metadataLabel=%t",got,metadataLabel)}
	}
}
