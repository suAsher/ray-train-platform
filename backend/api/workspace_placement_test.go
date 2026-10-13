package api

import (
 "context"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"

 "github.com/gin-gonic/gin"
 corev1 "k8s.io/api/core/v1"
 "k8s.io/apimachinery/pkg/api/resource"
 metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
 "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
 "k8s.io/apimachinery/pkg/runtime"
 "k8s.io/apimachinery/pkg/runtime/schema"
 dynamicfake "k8s.io/client-go/dynamic/fake"
 k8sfake "k8s.io/client-go/kubernetes/fake"
 "ray-train-platform-backend/auth"
 "ray-train-platform-backend/k8s"
)

func workspacePlacementNode(name string) *corev1.Node {
 return &corev1.Node{
  ObjectMeta:metav1.ObjectMeta{Name:name,Labels:map[string]string{"accelerator":"nvidia-rtx-4090","platform.wellspiking.ai/cache-ready":"true","kubernetes.io/hostname":name}},
  Status:corev1.NodeStatus{Conditions:[]corev1.NodeCondition{{Type:corev1.NodeReady,Status:corev1.ConditionTrue}},Allocatable:corev1.ResourceList{
   "nvidia.com/gpu":resource.MustParse("8"),corev1.ResourceCPU:resource.MustParse("64"),corev1.ResourceMemory:resource.MustParse("512Gi"),
  }},
 }
}

func TestLaunchWorkspacePrefersGloballyOccupiedNode(t *testing.T) {
 gin.SetMode(gin.TestMode)
 dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),map[schema.GroupVersionResource]string{
  {Group:"kueue.x-k8s.io",Version:"v1beta2",Resource:"workloads"}:"WorkloadList",
 })
 core := k8sfake.NewSimpleClientset(workspacePlacementNode("node-empty"),workspacePlacementNode("node-occupied"),&corev1.Pod{
  ObjectMeta:metav1.ObjectMeta{Name:"other-team-worker",Namespace:"tenant-team-b",Labels:map[string]string{"ray.io/node-type":"worker","ray.io/cluster":"dev-other"}},
  Spec:corev1.PodSpec{NodeName:"node-occupied",Containers:[]corev1.Container{{Name:"ray-worker",Resources:corev1.ResourceRequirements{Requests:corev1.ResourceList{"nvidia.com/gpu":resource.MustParse("5")}}}}},
  Status:corev1.PodStatus{Phase:corev1.PodRunning},
 })
 handler := NewHandler(&fakeJobRepository{},Options{
  Workspaces:&fakeWorkspaceStore{getErr:context.Canceled},Kubernetes:k8s.NewClientFromInterfaces(dynamic,core),
  WorkspaceImage:"registry.example/workspace@sha256:"+strings.Repeat("a",64),RayVersion:"2.35.0",
 })
 handler.newID = func()(string,error) { return "job-compact-debug",nil }
 router := gin.New()
 router.Use(func(c *gin.Context){ c.Set("ray-platform-principal",auth.Principal{Subject:"user-a",TenantID:"team-a",Roles:[]string{"Engineer"},AuthType:auth.AuthTypeOIDC});c.Next() })
 handler.RegisterWorkspaceRoutes(router.Group("/api/v1"))
 request := httptest.NewRequest(http.MethodPost,"/api/v1/dev-workspaces",strings.NewReader(`{"gpuCount":1}`))
 request.Header.Set("Content-Type","application/json")
 response := httptest.NewRecorder()
 router.ServeHTTP(response,request)
 if response.Code != http.StatusAccepted { t.Fatalf("status=%d body=%s",response.Code,response.Body.String()) }
 cluster,err := dynamic.Resource(schema.GroupVersionResource{Group:"ray.io",Version:"v1",Resource:"rayclusters"}).Namespace("tenant-team-a").Get(context.Background(),"dev-compact-debug",metav1.GetOptions{})
 if err != nil { t.Fatal(err) }
 groups,_,_ := unstructured.NestedSlice(cluster.Object,"spec","workerGroupSpecs")
 preferred,found,_ := unstructured.NestedSlice(groups[0].(map[string]any),"template","spec","affinity","nodeAffinity","preferredDuringSchedulingIgnoredDuringExecution")
 if !found || len(preferred) != 2 { t.Fatalf("launch did not apply shared capacity placement: preferences=%v",preferred) }
 values,_,_ := unstructured.NestedSlice(preferred[0].(map[string]any),"preference","matchFields")
 nodes,_,_ := unstructured.NestedStringSlice(values[0].(map[string]any),"values")
 if len(nodes) != 1 || nodes[0] != "node-occupied" { t.Fatalf("debug must fill global GPU gaps first, got %v",nodes) }
 // Launch is idempotent; an existing live workspace must not be re-placed.
 before := len(dynamic.Actions())
 response = httptest.NewRecorder()
 router.ServeHTTP(response,httptest.NewRequest(http.MethodPost,"/api/v1/dev-workspaces",strings.NewReader(`{"gpuCount":1}`)))
 if response.Code != http.StatusOK || len(dynamic.Actions()) != before { t.Fatalf("existing workspace was re-queried/re-rendered: status=%d",response.Code) }
}
