package k8s

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"ray-train-platform-backend/domain"
)

func capacityWorkloadListCount(c *Client) *atomic.Int32 {
	count:=&atomic.Int32{}
	c.dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("list","workloads",func(ktesting.Action)(bool,runtime.Object,error){count.Add(1);return false,nil,nil})
	return count
}

func TestSchedulingCapacityCacheSharesRawObservationWithoutSharingTenantOrFit(t *testing.T) {
	p:=teamWorkerPod("small","local","job-a","a","rtx4090",corev1.PodRunning,1)
	c:=capacityTestClient([]runtime.Object{capacityTestNode("a"),capacityTestNode("b"),p})
	count:=capacityWorkloadListCount(c)
	r:=capacityTestRequest()
	r.DedicatedNodes=map[string][]string{"local":{"a"}}
	first,err:=c.SchedulingCapacity(context.Background(),r)
	if err!=nil || first.Fit.CanFit || first.Pool.FreeGPUs!=7 {t.Fatalf("first observation: %+v %v",first,err)}
	r.Resources.GPUsPerWorker=1
	second,err:=c.SchedulingCapacity(context.Background(),r)
	if err!=nil || !second.Fit.CanFit || second.Fit.WorkerSlots!=7 || second.Nodes[0].TeamGPUs!=1 {t.Fatalf("fit must be recomputed: %+v %v",second,err)}
	// Caller mutation of a derived response must not affect cached raw data.
	second.Nodes[0].FreeGPUs=999
	r.TenantID="other"
	r.Resources.GPUsPerWorker=8
	third,err:=c.SchedulingCapacity(context.Background(),r)
	if err!=nil || !third.Fit.CanFit || third.Pool.FreeGPUs!=8 || third.Nodes[0].Name!="b" || third.Nodes[0].TeamGPUs!=0 {t.Fatalf("tenant constraints must be recomputed: %+v %v",third,err)}
	if !first.ObservedAt.Equal(second.ObservedAt) || !first.ObservedAt.Equal(third.ObservedAt) {t.Errorf("shared observation timestamp changed")}
	if count.Load()!=1 {t.Fatalf("raw collection calls=%d want1",count.Load())}
	var nodeLists,podLists int
	for _,action:=range c.kubernetes.(*kubefake.Clientset).Actions(){
		if action.GetVerb()=="list" && action.GetResource().Resource=="nodes" {nodeLists++}
		if action.GetVerb()=="list" && action.GetResource().Resource=="pods" {podLists++}
	}
	if nodeLists!=1 || podLists!=1 {t.Fatalf("node/pod lists=%d/%d want1/1",nodeLists,podLists)}
}

func TestSchedulingCapacityCacheDoesNotCacheReadFailure(t *testing.T) {
	c:=capacityTestClient([]runtime.Object{capacityTestNode("a")})
	var calls atomic.Int32
	c.dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("list","workloads",func(ktesting.Action)(bool,runtime.Object,error){
		if calls.Add(1)==1 {return true,nil,errors.New("transient failure")}
		return true,&unstructured.UnstructuredList{},nil
	})
	first,err:=c.SchedulingCapacity(context.Background(),capacityTestRequest())
	if err==nil || first.Available {t.Fatalf("first collection should fail: %+v %v",first,err)}
	for index:=0;index<2;index++ {
		s,err:=c.SchedulingCapacity(context.Background(),capacityTestRequest())
		if err!=nil || !s.Available {t.Fatalf("recovered collection: %+v %v",s,err)}
	}
	if calls.Load()!=2 {t.Fatalf("failed collection must retry and recovery cache: calls=%d want2",calls.Load())}
}

func TestSchedulingCapacityCacheSingleFlight(t *testing.T) {
	c:=capacityTestClient([]runtime.Object{capacityTestNode("a")})
	started,release:=make(chan struct{}),make(chan struct{})
	var calls atomic.Int32
	c.dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("list","workloads",func(ktesting.Action)(bool,runtime.Object,error){
		if calls.Add(1)==1 {close(started);<-release}
		return true,&unstructured.UnstructuredList{},nil
	})
	var group sync.WaitGroup
	errors:=make(chan error,8)
	for index:=0;index<8;index++ {
		group.Add(1)
		go func(){defer group.Done();_,err:=c.SchedulingCapacity(context.Background(),capacityTestRequest());errors<-err}()
	}
	<-started
	close(release)
	group.Wait()
	close(errors)
	for err:=range errors {if err!=nil {t.Fatal(err)}}
	if calls.Load()!=1 {t.Fatalf("simultaneous raw collections=%d want1",calls.Load())}
}

func TestSchedulingCapacityCacheWaiterCancellationDoesNotCancelSharedCollection(t *testing.T) {
	c:=capacityTestClient([]runtime.Object{capacityTestNode("a")})
	started,release:=make(chan struct{}),make(chan struct{})
	var calls atomic.Int32
	c.dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("list","workloads",func(ktesting.Action)(bool,runtime.Object,error){
		if calls.Add(1)==1 {close(started);<-release}
		return true,&unstructured.UnstructuredList{},nil
	})
	owner:=make(chan error,1)
	go func(){_,err:=c.SchedulingCapacity(context.Background(),capacityTestRequest());owner<-err}()
	<-started
	ctx,cancel:=context.WithCancel(context.Background())
	cancel()
	waiter:=make(chan error,1)
	go func(){_,err:=c.SchedulingCapacity(ctx,capacityTestRequest());waiter<-err}()
	select {
	case err:=<-waiter:
		if !errors.Is(err,context.Canceled) {t.Errorf("cancelled waiter err=%v",err)}
	case <-time.After(200*time.Millisecond):
		t.Error("cancelled waiter blocked behind raw collection")
	}
	close(release)
	if err:=<-owner;err!=nil {t.Fatalf("owner collection was poisoned: %v",err)}
	if calls.Load()!=1 {t.Fatalf("waiter started another collection: %d",calls.Load())}
}

func TestSchedulingCapacityCacheEmptyPoolSkipsReservationRead(t *testing.T) {
	c:=capacityTestClient(nil)
	count:=capacityWorkloadListCount(c)
	for index:=0;index<2;index++ {
		s,err:=c.SchedulingCapacity(context.Background(),capacityTestRequest())
		if err!=nil || !s.Available || s.Fit.Reason!="no_eligible_nodes" {t.Fatalf("empty pool: %+v %v",s,err)}
	}
	if count.Load()!=0 {t.Fatalf("empty pool queried Workloads %d times",count.Load())}
}

func TestSchedulingCapacityCacheSeparatesAcceleratorPools(t *testing.T) {
	a100:=capacityTestNode("a100")
	a100.Labels["accelerator"]="nvidia-a100"
	c:=capacityTestClient([]runtime.Object{capacityTestNode("4090"),a100})
	count:=capacityWorkloadListCount(c)
	for _,accelerator:=range []domain.AcceleratorClass{domain.AcceleratorRTX4090,domain.AcceleratorA100,domain.AcceleratorRTX4090,domain.AcceleratorA100} {
		r:=capacityTestRequest()
		r.AcceleratorClass=accelerator
		s,err:=c.SchedulingCapacity(context.Background(),r)
		if err!=nil || !s.Available || len(s.Nodes)!=1 || s.AcceleratorClass!=string(accelerator) {t.Fatalf("pool observation: %+v %v",s,err)}
	}
	if count.Load()!=2 {t.Fatalf("two pool collections=%d want2",count.Load())}
}

func TestSchedulingCapacityCacheExpiresWithoutServingStaleOnError(t *testing.T) {
	c:=capacityTestClient([]runtime.Object{capacityTestNode("a")})
	var calls atomic.Int32
	var fail atomic.Bool
	c.dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("list","workloads",func(ktesting.Action)(bool,runtime.Object,error){
		calls.Add(1)
		if fail.Load() {return true,nil,errors.New("expired observation cannot refresh")}
		return true,&unstructured.UnstructuredList{},nil
	})
	first,err:=c.SchedulingCapacity(context.Background(),capacityTestRequest())
	if err!=nil || !first.Available {t.Fatalf("initial snapshot: %+v %v",first,err)}
	time.Sleep(5*time.Second+50*time.Millisecond)
	fail.Store(true)
	s,err:=c.SchedulingCapacity(context.Background(),capacityTestRequest())
	if err==nil || s.Available {t.Fatalf("expired snapshot must not hide refresh error: %+v %v",s,err)}
	fail.Store(false)
	s,err=c.SchedulingCapacity(context.Background(),capacityTestRequest())
	if err!=nil || !s.Available || !s.ObservedAt.After(first.ObservedAt) {t.Fatalf("failed refresh must remain retryable: %+v %v",s,err)}
	if calls.Load()!=3 {t.Fatalf("collection count=%d want3",calls.Load())}
}
