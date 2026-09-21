package k8s

import (
 "bytes"
 "context"
 "encoding/json"
 "fmt"
 "io"
 "regexp"
 "strings"
 "time"
 corev1 "k8s.io/api/core/v1"
 metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
 "k8s.io/apimachinery/pkg/labels"
 "k8s.io/client-go/kubernetes/scheme"
 "k8s.io/client-go/tools/remotecommand"
 "ray-train-platform-backend/environmentbuild"
)

var environmentDigestPattern=regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
func environmentPinnedImage(image string) bool {
 parts:=strings.Split(image,"@");return len(parts)==2 && strings.HasPrefix(parts[0],environmentbuild.RegistryHost+"/") && environmentDigestPattern.MatchString(parts[1])
}

// The database lookup preceding this method establishes the authenticated owner.
// KubeRay labels and owner UID then prove that this is that managed workspace.
func (r *EnvironmentRunner) InspectWorkspace(ctx context.Context,w environmentbuild.Workspace) (environmentbuild.WorkspaceSnapshot,error) {
 pod,err:=r.workspacePod(ctx,w);if err!=nil {return environmentbuild.WorkspaceSnapshot{},err}
 return environmentbuild.WorkspaceSnapshot{UID:string(pod.UID),Image:r.config.WorkspaceImage},nil
}
func (r *EnvironmentRunner) workspacePod(ctx context.Context,w environmentbuild.Workspace) (*corev1.Pod,error) {
 if r.client==nil || r.client.dynamic==nil || r.client.kubernetes==nil || w.ID=="" || w.OwnerID=="" || w.TenantID=="" || !isDNSLabel(w.Namespace) || !isDNSLabel(w.ResourceName) || !environmentPinnedImage(r.config.WorkspaceImage) {return nil,environmentbuild.ErrInvalid}
 cluster,err:=r.client.dynamic.Resource(rayClusterGVR).Namespace(w.Namespace).Get(ctx,w.ResourceName,metav1.GetOptions{})
 if err!=nil {return nil,err}
 cl:=cluster.GetLabels()
 if cl["app.kubernetes.io/managed-by"]!="ray-train-platform" || cl["ray.io/workspace-id"]!=w.ID || cl["ray.io/tenant-id"]!=w.TenantID || cl["ray.io/dev-workspace"]!="true" || cluster.GetUID()=="" || cluster.GetDeletionTimestamp()!=nil {return nil,fmt.Errorf("workspace ownership verification failed")}
 selector:=labels.Set{"ray.io/cluster":w.ResourceName,"ray.io/node-type":"worker"}.AsSelector().String()
 pods,err:=r.client.kubernetes.CoreV1().Pods(w.Namespace).List(ctx,metav1.ListOptions{LabelSelector:selector});if err!=nil {return nil,err}
 var found *corev1.Pod
 for i:=range pods.Items {
  pod:=&pods.Items[i];if pod.DeletionTimestamp!=nil || pod.Status.Phase!=corev1.PodRunning {continue}
  owned:=false
  for _,ref:=range pod.OwnerReferences {if ref.Kind=="RayCluster" && ref.Name==cluster.GetName() && ref.UID==cluster.GetUID() && ref.Controller!=nil && *ref.Controller {owned=true}}
  if !owned {continue}
  for _,container:=range pod.Spec.Containers {
   if container.Name!="ray-worker" {continue}
   if container.Image!=r.config.WorkspaceImage {return nil,fmt.Errorf("workspace does not use the supported pinned environment image")}
   ready:=false
   for _,status:=range pod.Status.ContainerStatuses {if status.Name==container.Name && status.Ready && strings.HasSuffix(status.ImageID,"@"+strings.Split(r.config.WorkspaceImage,"@")[1]) {ready=true}}
   if !ready {return nil,fmt.Errorf("workspace image has not been verified by the container runtime")}
   if found!=nil {return nil,fmt.Errorf("workspace has multiple active workers")}
   found=pod.DeepCopy()
  }
 }
 if found==nil || found.UID=="" {return nil,fmt.Errorf("workspace has no ready managed worker")};return found,nil
}

type environmentBoundedBuffer struct { bytes.Buffer; limit int }
func (b *environmentBoundedBuffer) Write(data []byte) (int,error) {
 if len(data)>b.limit-b.Len() {return 0,fmt.Errorf("environment capture exceeded output limit")};return b.Buffer.Write(data)
}
func (r *EnvironmentRunner) capture(ctx context.Context,b environmentbuild.Build) (environmentbuild.StepResult,error) {
 workspace:=environmentbuild.Workspace{ID:b.WorkspaceID,TenantID:b.TenantID,OwnerID:b.OwnerID,Namespace:b.Namespace,ResourceName:b.WorkspaceResourceName}
 pod,err:=r.workspacePod(ctx,workspace);if err!=nil {return environmentbuild.StepResult{},err}
 if string(pod.UID)!=b.WorkspaceUID || b.WorkspaceImage!=r.config.WorkspaceImage {return environmentbuild.StepResult{},fmt.Errorf("workspace changed after environment request")}
 captureCtx,cancel:=context.WithTimeout(ctx,5*time.Minute);defer cancel()
 output:=&environmentBoundedBuffer{limit:1024*1024}
 if err:=r.execCapture(captureCtx,pod.Namespace,pod.Name,output);err!=nil {return environmentbuild.StepResult{},fmt.Errorf("environment capture failed; restore supported packages and retry")}
 // Capture is untrusted workspace output. The trusted prepare container repeats
 // full schema/provenance validation before downloading or running anything.
 var envelope struct { SchemaVersion int `json:"schemaVersion"`;BaseImage string `json:"baseImage"`;Checks map[string]bool `json:"checks"` }
 if err:=json.Unmarshal(output.Bytes(),&envelope);err!=nil || envelope.SchemaVersion!=1 || envelope.BaseImage!=b.BaseImage {return environmentbuild.StepResult{},fmt.Errorf("invalid environment capture manifest")}
 for _,key:=range []string{"baseUnchanged","managedOnly","installedFilesVerified","stableCapture"} {if !envelope.Checks[key] {return environmentbuild.StepResult{},fmt.Errorf("environment capture integrity validation failed")}}
 after,err:=r.workspacePod(ctx,workspace);if err!=nil {return environmentbuild.StepResult{},err}
 if after.UID!=pod.UID {return environmentbuild.StepResult{},fmt.Errorf("workspace was replaced during capture")}
 return environmentbuild.StepResult{Done:true,SnapshotJSON:output.String()},nil
}
func (r *EnvironmentRunner) execCapture(ctx context.Context,namespace,pod string,stdout io.Writer) error {
 if r.captureExec!=nil {return r.captureExec(ctx,namespace,pod,stdout)}
 if r.client.restConfig==nil {return environmentbuild.ErrUnavailable}
 request:=r.client.kubernetes.CoreV1().RESTClient().Post().Resource("pods").Namespace(namespace).Name(pod).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container:"ray-worker",Command:[]string{"/usr/local/bin/raytrain-environment","capture"},Stdout:true,Stderr:true},scheme.ParameterCodec)
 executor,err:=remotecommand.NewSPDYExecutor(r.client.restConfig,"POST",request.URL());if err!=nil {return err}
 return executor.StreamWithContext(ctx,remotecommand.StreamOptions{Stdout:stdout,Stderr:io.Discard})
}
