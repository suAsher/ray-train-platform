package k8s

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestSchedulingCapacityCPUOnlyPodCanPreventGPUWorkerFit(t *testing.T) {
	p:=&corev1.Pod{ObjectMeta:metav1.ObjectMeta{Name:"cpu-service",Namespace:"system"},Spec:corev1.PodSpec{NodeName:"a",Containers:[]corev1.Container{{Resources:corev1.ResourceRequirements{Requests:corev1.ResourceList{corev1.ResourceCPU:resource.MustParse("60")}}}}},Status:corev1.PodStatus{Phase:corev1.PodRunning}}
	c:=capacityTestClient([]runtime.Object{capacityTestNode("a"),p})
	r:=capacityTestRequest()
	r.Resources.GPUsPerWorker=1
	r.Resources.WorkerReplicas=2
	s,err:=c.SchedulingCapacity(context.Background(),r)
	if err!=nil || !s.Available || s.Pool.FreeGPUs!=8 || s.Fit.WorkerSlots!=1 || s.Fit.Reason!="insufficient_cpu_or_memory" {t.Fatalf("GPU free must not imply worker fits: %+v %v",s,err)}
}

func TestSchedulingCapacityTerminatingCountsAndCompletedDoesNot(t *testing.T) {
	p:=teamWorkerPod("terminating","local","job-a","a","rtx4090",corev1.PodRunning,2)
	now:=metav1.Now()
	p.DeletionTimestamp=&now
	done:=teamWorkerPod("finished","local","job-b","a","rtx4090",corev1.PodSucceeded,8)
	w:=capacityTestWorkload("wl-a","a","workers",1,"8",true)
	_ = unstructured.SetNestedSlice(w.Object,[]any{map[string]any{"type":"Finished","status":"True"}},"status","conditions")
	c:=capacityTestClient([]runtime.Object{capacityTestNode("a"),p,done},w)
	s,err:=c.SchedulingCapacity(context.Background(),capacityTestRequest())
	if err!=nil || !s.Available || s.Pool.FreeGPUs!=6 || s.Pool.AllocatedGPUs!=2 || s.Pool.ReservedGPUs!=0 {t.Fatalf("capacity=%+v err=%v",s,err)}
}

func TestSchedulingCapacityAnnotationReservationDedupAndExcludedJob(t *testing.T) {
	p:=teamWorkerPod("worker","local","job-a","a","rtx4090",corev1.PodRunning,2)
	p.Annotations=map[string]string{"kueue.x-k8s.io/workload":"wl-a"}
	p.Labels["kueue.x-k8s.io/podset"]="workers"
	w:=capacityTestWorkload("wl-a","a","workers",2,"4",true)
	w.SetOwnerReferences([]metav1.OwnerReference{{APIVersion:"ray.io/v1",Kind:"RayJob",Name:"job-a"}})
	c:=capacityTestClient([]runtime.Object{capacityTestNode("a"),p},w)
	r:=capacityTestRequest()
	s,err:=c.SchedulingCapacity(context.Background(),r)
	if err!=nil || !s.Available || s.Pool.FreeGPUs!=4 || s.Pool.ReservedGPUs!=2 {t.Fatalf("actual annotation dedup: %+v %v",s,err)}
	r.ExcludeJobID="job-a"
	s,err=c.SchedulingCapacity(context.Background(),r)
	if err!=nil || !s.Available || s.Pool.FreeGPUs!=6 || s.Pool.AllocatedGPUs!=2 || s.Pool.ReservedGPUs!=0 {t.Fatalf("exclude reservation must retain bound GPU: %+v %v",s,err)}
}

func TestSchedulingCapacityDedicatedPoolExcludesForeignNodes(t *testing.T) {
	a,b:=capacityTestNode("a"),capacityTestNode("b")
	a.Spec.Taints=[]corev1.Taint{{Key:DedicatedTenantTaintKey,Value:"local",Effect:corev1.TaintEffectNoSchedule}}
	c:=capacityTestClient([]runtime.Object{a,b})
	r:=capacityTestRequest()
	r.DedicatedNodes=map[string][]string{"local":{"a"}}
	s,err:=c.SchedulingCapacity(context.Background(),r)
	if err!=nil || !s.Available || s.Pool.TotalGPUs!=8 || len(s.Nodes)!=1 || s.Nodes[0].Name!="a" {t.Fatalf("owner dedicated pool: %+v %v",s,err)}
	r.TenantID="other"
	s,err=c.SchedulingCapacity(context.Background(),r)
	if err!=nil || !s.Available || s.Pool.TotalGPUs!=8 || len(s.Nodes)!=1 || s.Nodes[0].Name!="b" {t.Fatalf("foreign dedicated isolation: %+v %v",s,err)}
}

func TestSchedulingCapacityKueueReadFailureIsUnknown(t *testing.T) {
	c:=capacityTestClient([]runtime.Object{capacityTestNode("a")})
	c.dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("list","workloads",func(ktesting.Action)(bool,runtime.Object,error){return true,nil,errors.New("internal kube failure")})
	s,err:=c.SchedulingCapacity(context.Background(),capacityTestRequest())
	if err==nil || s.Available || s.Fit.Reason!="capacity_unknown" || s.UnavailableReason!="capacity_observation_unavailable" {t.Fatalf("read failure must not become zero availability: %+v %v",s,err)}
}
