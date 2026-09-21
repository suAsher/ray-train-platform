package k8s

import (
 "context"
 "strings"
 "testing"
 "time"
 batchv1 "k8s.io/api/batch/v1"
 corev1 "k8s.io/api/core/v1"
 metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
 "k8s.io/apimachinery/pkg/runtime"
 "k8s.io/apimachinery/pkg/runtime/schema"
 "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
 "k8s.io/apimachinery/pkg/types"
 dynamicfake "k8s.io/client-go/dynamic/fake"
 "k8s.io/client-go/kubernetes/fake"
 "ray-train-platform-backend/environmentbuild"
)

func environmentTestRunner() *EnvironmentRunner {
 image:="harbor.wellspiking.ai/platform/image@sha256:"+strings.Repeat("a",64)
 return NewEnvironmentRunner(NewClientFromInterfaces(nil,fake.NewSimpleClientset()),EnvironmentRunnerConfig{Namespace:"platform",BaseImage:image,WorkspaceImage:image,PrepareImage:image,PublisherImage:image,StorageClass:"ebs-ssd",PullSecretName:"pull-robot",ImagePullSecrets:[]string{"pull-robot"},WheelIndexURL:"https://packages.internal/simple",JobTimeout:time.Hour})
}
func environmentTestBuild(r *EnvironmentRunner) environmentbuild.Build {
 return environmentbuild.Build{ID:"build-one",TenantID:"tenant-a",OwnerID:"alice",BaseImage:r.config.BaseImage,WorkspaceImage:r.config.WorkspaceImage,Project:"public",Repository:"environment",Tag:"build-one",ArtifactDigest:"sha256:"+strings.Repeat("b",64),ImageDigest:"sha256:"+strings.Repeat("b",64),SnapshotJSON:`{"schemaVersion":1}`,Attempt:1}
}
func TestEnvironmentJobsIsolateCredentialsAndResources(t *testing.T) {
 r:=environmentTestRunner();b:=environmentTestBuild(r)
 for _,phase:=range []string{environmentbuild.Building,environmentbuild.Validating,environmentbuild.Pushing,environmentbuild.VerifyingPull} {
  t.Run(phase,func(t *testing.T){b.Status=phase;job,err:=r.renderJob(b);if err!=nil {t.Fatal(err)};pod:=job.Spec.Template.Spec
   if pod.HostNetwork || pod.HostPID || pod.HostIPC || pod.AutomountServiceAccountToken==nil || *pod.AutomountServiceAccountToken {t.Fatal("Job exposed host or service account")}
   if *job.Spec.BackoffLimit!=0 || *job.Spec.ActiveDeadlineSeconds<=0 || pod.RestartPolicy!=corev1.RestartPolicyNever {t.Fatal("Job execution is not bounded")}
   container:=pod.Containers[0]
   if container.SecurityContext.Privileged!=nil && *container.SecurityContext.Privileged {t.Fatal("privileged Job")}
   if *container.SecurityContext.AllowPrivilegeEscalation || len(container.SecurityContext.Capabilities.Drop)!=1 || container.SecurityContext.Capabilities.Drop[0]!="ALL" {t.Fatal("Job capability boundary failed")}
   for _,volume:=range pod.Volumes {
    if volume.HostPath!=nil {t.Fatal("host volume")}
    if volume.Secret!=nil {
     if phase==environmentbuild.Building || phase==environmentbuild.VerifyingPull {t.Fatal("user-code phase mounts a credential")}
     if phase==environmentbuild.Pushing && volume.Secret.SecretName!=environmentPublishSecretName(b) {t.Fatal("publisher uses another authorization")}
     if phase==environmentbuild.Validating && volume.Secret.SecretName!="pull-robot" {t.Fatal("assembler sees user push credential")}
    }
   }
   if phase==environmentbuild.VerifyingPull && (!strings.HasSuffix(container.Image,"@"+b.ImageDigest) || len(pod.Volumes)!=0) {t.Fatal("pull verification is not isolated/pinned")}
  })
 }
}
func TestEnvironmentResourcesRefuseForeignObjects(t *testing.T) {
 r:=environmentTestRunner();b:=environmentTestBuild(r)
 foreign:=&corev1.PersistentVolumeClaim{ObjectMeta:metav1.ObjectMeta{Name:environmentResourceName("oci",b.ID),Namespace:r.config.Namespace}}
 if _,err:=r.client.kubernetes.CoreV1().PersistentVolumeClaims(r.config.Namespace).Create(context.Background(),foreign,metav1.CreateOptions{});err!=nil {t.Fatal(err)}
 if err:=r.ensureArtifacts(context.Background(),b);err==nil {t.Fatal("adopted foreign volume")}
}
func TestEnvironmentCleanupWaitsForPodsBeforeDeletingSecrets(t *testing.T) {
 r:=environmentTestRunner();b:=environmentTestBuild(r);ctx:=context.Background()
 secret:=&corev1.Secret{ObjectMeta:r.metadata(environmentPublishSecretName(b),b)}
 pod:=&corev1.Pod{ObjectMeta:r.metadata("running-environment",b)}
 r.client=NewClientFromInterfaces(nil,fake.NewSimpleClientset(secret,pod))
 if err:=r.Cleanup(ctx,b,false);err==nil {t.Fatal("cleanup completed with a running pod")}
 if _,err:=r.client.kubernetes.CoreV1().Secrets(r.config.Namespace).Get(ctx,secret.Name,metav1.GetOptions{});err!=nil {t.Fatal("credential deleted before execution stopped")}
 if err:=r.client.kubernetes.CoreV1().Pods(r.config.Namespace).Delete(ctx,pod.Name,metav1.DeleteOptions{});err!=nil {t.Fatal(err)}
 if err:=r.Cleanup(ctx,b,true);err!=nil {t.Fatal(err)}
}
func TestEnvironmentJobResultRequiresMatchingOwnerAndDigest(t *testing.T) {
 r:=environmentTestRunner();b:=environmentTestBuild(r);b.Status=environmentbuild.Pushing;yes:=true
 job:=&batchv1.Job{ObjectMeta:r.metadata(environmentJobName(b),b),Status:batchv1.JobStatus{Succeeded:1}};job.UID=types.UID("job-uid")
 pod:=&corev1.Pod{ObjectMeta:r.metadata("done",b),Status:corev1.PodStatus{Phase:corev1.PodSucceeded,ContainerStatuses:[]corev1.ContainerStatus{{Name:"environment",State:corev1.ContainerState{Terminated:&corev1.ContainerStateTerminated{ExitCode:0,Message:`{"digest":"`+b.ArtifactDigest+`"}`}}}}}}
 pod.Labels["batch.kubernetes.io/job-name"]=job.Name;pod.OwnerReferences=[]metav1.OwnerReference{{Kind:"Job",UID:job.UID,Controller:&yes}}
 r.client=NewClientFromInterfaces(nil,fake.NewSimpleClientset(pod))
 result,err:=r.observeJob(context.Background(),b,job);if err!=nil || !result.Done || result.ImageDigest!=b.ArtifactDigest {t.Fatalf("result %+v, %v",result,err)}
 b.OwnerID="mallory"
 if _,err:=r.observeJob(context.Background(),b,job);err==nil {t.Fatal("accepted another owner's result")}
}
func TestEnvironmentWorkspaceRequiresManagedClusterAndActualPinnedImage(t *testing.T) {
 r:=environmentTestRunner();yes:=true;ctx:=context.Background()
 workspace:=environmentbuild.Workspace{ID:"workspace-a",TenantID:"tenant-a",OwnerID:"alice",Namespace:"tenant-a",ResourceName:"workspace-a"}
 cluster:=&unstructured.Unstructured{Object:map[string]interface{}{"apiVersion":"ray.io/v1","kind":"RayCluster","metadata":map[string]interface{}{"name":workspace.ResourceName,"namespace":workspace.Namespace,"uid":"cluster-uid","labels":map[string]interface{}{"app.kubernetes.io/managed-by":"ray-train-platform","ray.io/workspace-id":workspace.ID,"ray.io/tenant-id":workspace.TenantID,"ray.io/dev-workspace":"true"}}}}
 pod:=&corev1.Pod{ObjectMeta:metav1.ObjectMeta{Name:"worker",Namespace:workspace.Namespace,UID:types.UID("pod-uid"),Labels:map[string]string{"ray.io/cluster":workspace.ResourceName,"ray.io/node-type":"worker"},OwnerReferences:[]metav1.OwnerReference{{Name:workspace.ResourceName,Kind:"RayCluster",UID:types.UID("cluster-uid"),Controller:&yes}}},Spec:corev1.PodSpec{Containers:[]corev1.Container{{Name:"ray-worker",Image:r.config.WorkspaceImage}}},Status:corev1.PodStatus{Phase:corev1.PodRunning,ContainerStatuses:[]corev1.ContainerStatus{{Name:"ray-worker",Ready:true,ImageID:r.config.WorkspaceImage}}}}
 dynamic:=dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),map[schema.GroupVersionResource]string{rayClusterGVR:"RayClusterList"},cluster)
 r.client=NewClientFromInterfaces(dynamic,fake.NewSimpleClientset(pod))
 result,err:=r.InspectWorkspace(ctx,workspace);if err!=nil || result.UID!="pod-uid" {t.Fatalf("inspect %+v %v",result,err)}
 // An index pin cannot silently authorize a different platform-manifest digest.
 pod.Status.ContainerStatuses[0].ImageID="harbor.wellspiking.ai/platform/image@sha256:"+strings.Repeat("c",64)
 if _,err:=r.client.kubernetes.CoreV1().Pods(workspace.Namespace).UpdateStatus(ctx,pod,metav1.UpdateOptions{});err!=nil {t.Fatal(err)}
 if _,err:=r.InspectWorkspace(ctx,workspace);err==nil {t.Fatal("accepted runtime digest different from configured single-platform pin")}
 pod.Status.ContainerStatuses[0].ImageID=r.config.WorkspaceImage
 if _,err:=r.client.kubernetes.CoreV1().Pods(workspace.Namespace).UpdateStatus(ctx,pod,metav1.UpdateOptions{});err!=nil {t.Fatal(err)}
 workspace.TenantID="other-tenant";if _,err:=r.InspectWorkspace(ctx,workspace);err==nil {t.Fatal("accepted another tenant cluster")}
 workspace.TenantID="tenant-a";pod.Spec.Containers[0].Image="harbor.wellspiking.ai/platform/image:latest"
 if _,err:=r.client.kubernetes.CoreV1().Pods(workspace.Namespace).Update(ctx,pod,metav1.UpdateOptions{});err!=nil {t.Fatal(err)}
 if _,err:=r.InspectWorkspace(ctx,workspace);err==nil {t.Fatal("accepted unsupported actual image")}
}

func TestEnvironmentCaptureBoundsOutput(t *testing.T) {
 buffer:=&environmentBoundedBuffer{limit:8}
 if n,err:=buffer.Write([]byte("12345678"));err!=nil || n!=8 {t.Fatalf("write %d %v",n,err)}
 if _,err:=buffer.Write([]byte("9"));err==nil {t.Fatal("capture exceeded its memory bound")}
 if buffer.String()!="12345678" {t.Fatal("over-limit write changed capture")}
}

func TestEnvironmentPublisherAuthFailureRemainsRetriable(t *testing.T) {
 r:=environmentTestRunner();b:=environmentTestBuild(r);b.Status=environmentbuild.Pushing;yes:=true
 job:=&batchv1.Job{ObjectMeta:r.metadata(environmentJobName(b),b),Status:batchv1.JobStatus{Conditions:[]batchv1.JobCondition{{Type:batchv1.JobFailed,Status:corev1.ConditionTrue}}}};job.UID=types.UID("job-uid")
 pod:=&corev1.Pod{ObjectMeta:r.metadata("failed",b),Status:corev1.PodStatus{Phase:corev1.PodFailed,ContainerStatuses:[]corev1.ContainerStatus{{Name:"environment",State:corev1.ContainerState{Terminated:&corev1.ContainerStateTerminated{ExitCode:1,Message:`{"errorCode":"REGISTRY_PUSH_DENIED"}`}}}}}}
 pod.Labels["batch.kubernetes.io/job-name"]=job.Name;pod.OwnerReferences=[]metav1.OwnerReference{{Kind:"Job",UID:job.UID,Controller:&yes}}
 r.client=NewClientFromInterfaces(nil,fake.NewSimpleClientset(pod))
 if _,err:=r.observeJob(context.Background(),b,job);err!=environmentbuild.ErrAuthorization {t.Fatalf("expected authorization retry, got %v",err)}
}
