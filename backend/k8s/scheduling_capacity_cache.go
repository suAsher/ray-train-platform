package k8s

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const schedulingCapacityCacheTTL = 5*time.Second

// Raw observations are immutable and never contain tenant-specific fits,
// quota, exclusion decisions, or derived responses. A Client owns its cache,
// so disposing the client also releases every observation and in-flight read.
type schedulingCapacityCache struct {
	mu sync.Mutex
	entries map[string]*rawSchedulingCapacity
	flights map[string]*schedulingCapacityFlight
}

type rawSchedulingCapacity struct {
	observedAt time.Time
	completedAt time.Time
	nodes []corev1.Node
	pods []corev1.Pod
	workloads []unstructured.Unstructured
}

type schedulingCapacityFlight struct {
	done chan struct{}
	snapshot *rawSchedulingCapacity
	err error
}

func (c *Client) schedulingCapacitySnapshot(ctx context.Context, selector string) (*rawSchedulingCapacity,error) {
	if err:=ctx.Err();err!=nil {return nil,err}
	version:=c.kueueGVR.Version
	if version=="" {version=localQueueGVR.Version}
	key:=version+"\n"+selector
	cache:=&c.capacityCache
	cache.mu.Lock()
	now:=time.Now()
	for entryKey,entry:=range cache.entries {
		if now.Sub(entry.completedAt)>=schedulingCapacityCacheTTL {delete(cache.entries,entryKey)}
	}
	if snapshot:=cache.entries[key];snapshot!=nil {
		cache.mu.Unlock()
		return snapshot,nil
	}
	flight:=cache.flights[key]
	if flight==nil {
		flight=&schedulingCapacityFlight{done:make(chan struct{})}
		if cache.flights==nil {cache.flights=map[string]*schedulingCapacityFlight{}}
		cache.flights[key]=flight
		// A caller leaving must not cancel other callers' shared observation.
		// The producer always has its own bounded lifetime, even with no waiters.
		readCtx,cancel:=context.WithTimeout(context.WithoutCancel(ctx),5*time.Second)
		go c.collectSchedulingCapacityFlight(readCtx,cancel,selector,key,flight)
	}
	cache.mu.Unlock()
	select {
	case <-ctx.Done(): return nil,ctx.Err()
	case <-flight.done: return flight.snapshot,flight.err
	}
}

func (c *Client) collectSchedulingCapacityFlight(ctx context.Context,cancel context.CancelFunc,selector,key string,flight *schedulingCapacityFlight) {
	defer cancel()
	snapshot,err:=c.collectSchedulingCapacitySnapshot(ctx,selector)
	cache:=&c.capacityCache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if err==nil {
		if cache.entries==nil {cache.entries=map[string]*rawSchedulingCapacity{}}
		cache.entries[key]=snapshot
	} else {
		// Failed refreshes never fall back to an expired observation.
		delete(cache.entries,key)
	}
	flight.snapshot,flight.err=snapshot,err
	delete(cache.flights,key)
	close(flight.done)
}

func (c *Client) collectSchedulingCapacitySnapshot(ctx context.Context,selector string)(*rawSchedulingCapacity,error) {
	snapshot:=&rawSchedulingCapacity{observedAt:time.Now().UTC()}
	nodes,err:=c.kubernetes.CoreV1().Nodes().List(ctx,metav1.ListOptions{LabelSelector:selector})
	if err!=nil {return nil,fmt.Errorf("list capacity nodes: %w",err)}
	if err:=ctx.Err();err!=nil {return nil,err}
	snapshot.nodes=nodes.Items
	if !capacityRawPoolHasCandidates(snapshot.nodes) {
		snapshot.completedAt=time.Now()
		return snapshot,nil
	}
	pods,err:=c.kubernetes.CoreV1().Pods("").List(ctx,metav1.ListOptions{})
	if err!=nil {return nil,fmt.Errorf("list capacity pods: %w",err)}
	snapshot.pods=pods.Items
	gvr:=c.kueueGVR
	if gvr.Version=="" {gvr=localQueueGVR}
	gvr.Resource="workloads"
	continuation:=""
	for {
		if err:=ctx.Err();err!=nil {return nil,err}
		workloads,err:=c.dynamic.Resource(gvr).List(ctx,metav1.ListOptions{Limit:500,Continue:continuation})
		if err!=nil {return nil,fmt.Errorf("list admission reservations: %w",err)}
		for _,workload:=range workloads.Items {
			// Historical finished/released objects can never contribute a
			// reservation, so do not retain them in every pool's cache.
			if !capacityWorkloadFinished(workload) {snapshot.workloads=append(snapshot.workloads,workload)}
		}
		continuation=workloads.GetContinue()
		if continuation=="" {break}
	}
	if err:=ctx.Err();err!=nil {return nil,err}
	snapshot.completedAt=time.Now()
	return snapshot,nil
}

// This predicate is deliberately independent of tenant assignments: another
// tenant may tolerate its dedicated taint and use the same raw observation.
func capacityRawPoolHasCandidates(nodes []corev1.Node) bool {
	for _,node:=range nodes {
		if isVirtualNode(node) || node.Spec.Unschedulable || !isNodeReady(node) || capacityResourceValue(node.Status.Allocatable,"nvidia.com/gpu")<=0 {continue}
		blocked:=false
		for _,taint:=range node.Spec.Taints {
			if taint.Effect!=corev1.TaintEffectNoSchedule && taint.Effect!=corev1.TaintEffectNoExecute {continue}
			if taint.Key==DedicatedTenantTaintKey && taint.Effect==corev1.TaintEffectNoSchedule {continue}
			blocked=true
		}
		if !blocked {return true}
	}
	return false
}
