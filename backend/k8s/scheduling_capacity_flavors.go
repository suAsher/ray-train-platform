package k8s

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"ray-train-platform-backend/domain"
)

// A topology that cannot be mapped must remain unknown unless the API's
// ResourceFlavor proves that the entire PodSet belongs to another GPU pool.
// Flavor names are opaque; only server-owned node labels establish the pool.
func (c *Client) capacityAssignmentOutsidePool(ctx context.Context, assignment map[string]any, accelerator domain.AcceleratorClass) (bool,error) {
	flavors,ok:=assignment["flavors"].(map[string]any)
	if !ok || len(flavors)==0 {return false,fmt.Errorf("reservation flavor unknown")}
	gvr:=c.kueueGVR
	if gvr.Version=="" {gvr=localQueueGVR}
	gvr.Resource="resourceflavors"
	names:=map[string]bool{}
	for _,value:=range flavors {
		name,ok:=value.(string)
		if !ok || name=="" {return false,fmt.Errorf("invalid reservation flavor")}
		names[name]=true
	}
	for name:=range names {
		flavor,err:=c.dynamic.Resource(gvr).Get(ctx,name,metav1.GetOptions{})
		if err!=nil {return false,fmt.Errorf("reservation flavor unavailable")}
		pool,found,err:=unstructured.NestedString(flavor.Object,"spec","nodeLabels","accelerator")
		if err!=nil || !found || pool=="" {return false,fmt.Errorf("reservation flavor accelerator unknown")}
		if pool==accelerator.Resolved().NodeLabelValue() {return false,nil}
	}
	return true,nil
}
