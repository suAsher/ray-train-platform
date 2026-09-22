package main

import (
 "context"
 "testing"
 "time"

 coordv1 "k8s.io/api/coordination/v1"
 metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
 "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
 "k8s.io/apimachinery/pkg/runtime"
 "k8s.io/apimachinery/pkg/runtime/schema"
 dfake "k8s.io/client-go/dynamic/fake"
 kfake "k8s.io/client-go/kubernetes/fake"
 "ray-train-platform-backend/assistantidle"
)
func TestReaperKeepsFreshLeaseAndReclaimsAfterExpiry(t *testing.T){
 now:=time.Now().UTC();ns:="raytrain-assistant-test";name:="assistant-idle"
 service:=&unstructured.Unstructured{Object:map[string]any{"apiVersion":"ray.io/v1","kind":"RayService","metadata":map[string]any{"name":name,"namespace":ns,"uid":"owned","creationTimestamp":now.Add(-time.Minute).Format(time.RFC3339),"labels":map[string]any{"app.kubernetes.io/instance":name}}}}
 dyn:=dfake.NewSimpleDynamicClient(runtime.NewScheme(),service)
 holder:="controller";duration:=int32(30);renewed:=metav1.NewMicroTime(now)
 lease:=&coordv1.Lease{ObjectMeta:metav1.ObjectMeta{Name:name,Namespace:ns},Spec:coordv1.LeaseSpec{HolderIdentity:&holder,LeaseDurationSeconds:&duration,RenewTime:&renewed}}
 typed:=kfake.NewSimpleClientset(lease)
 backend:=assistantidle.NewKubeBackend(assistantidle.KubeAdapterConfig{Dynamic:dyn,Kubernetes:typed,Name:name,Namespace:ns,InstanceID:name})
 cfg:=assistantidle.RuntimeConfig{LeaseName:name,Render:assistantidle.RenderConfig{Name:name,Namespace:ns}}
 if err:=reap(context.Background(),typed,backend,cfg,now);err!=nil{t.Fatal(err)}
 gvr:=schema.GroupVersionResource{Group:"ray.io",Version:"v1",Resource:"rayservices"}
 if _,err:=dyn.Resource(gvr).Namespace(ns).Get(context.Background(),name,metav1.GetOptions{});err!=nil{t.Fatal("deleted fresh service",err)}
 if err:=reap(context.Background(),typed,backend,cfg,now.Add(31*time.Second));err!=nil{t.Fatal(err)}
 if _,err:=dyn.Resource(gvr).Namespace(ns).Get(context.Background(),name,metav1.GetOptions{});err==nil{t.Fatal("failed to delete expired service")}
}
