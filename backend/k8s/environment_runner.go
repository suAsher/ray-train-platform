package k8s

import (
 "context"
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "fmt"
 "io"
 "maps"
 "strconv"
 "time"
 batchv1 "k8s.io/api/batch/v1"
 corev1 "k8s.io/api/core/v1"
 apierrors "k8s.io/apimachinery/pkg/api/errors"
 "k8s.io/apimachinery/pkg/api/resource"
 metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
 "k8s.io/apimachinery/pkg/labels"
 "ray-train-platform-backend/environmentbuild"
)

type EnvironmentRunnerConfig struct {
 Namespace string
 BaseImage, WorkspaceImage, PrepareImage, PublisherImage string
 StorageClass, WheelIndexURL, PullSecretName string
 StorageGiB int
 ImagePullSecrets []string
 NodeSelector map[string]string
 JobTimeout time.Duration
}
type EnvironmentRunner struct {
 client *Client
 config EnvironmentRunnerConfig
 captureExec func(context.Context,string,string,io.Writer,io.Writer) error
}
var _ environmentbuild.Runner = (*EnvironmentRunner)(nil)
func NewEnvironmentRunner(client *Client,cfg EnvironmentRunnerConfig) *EnvironmentRunner {
 cfg.ImagePullSecrets=append([]string(nil),cfg.ImagePullSecrets...)
 cfg.NodeSelector=maps.Clone(cfg.NodeSelector)
 if cfg.StorageClass=="" {cfg.StorageClass="ebs-ssd"};if cfg.StorageGiB==0 {cfg.StorageGiB=60}
 if cfg.JobTimeout==0 {cfg.JobTimeout=30*time.Minute}
 return &EnvironmentRunner{client:client,config:cfg}
}
func (r *EnvironmentRunner) Step(ctx context.Context,b environmentbuild.Build,credentials *environmentbuild.Credentials) (environmentbuild.StepResult,error) {
 if r.client==nil || r.client.kubernetes==nil || !isDNSLabel(r.config.Namespace) || b.ID=="" || b.TenantID=="" || b.OwnerID=="" || b.Attempt<0 || b.BaseImage!=r.config.BaseImage {return environmentbuild.StepResult{},environmentbuild.ErrInvalid}
 if b.Status==environmentbuild.Capturing {return r.capture(ctx,b)}
 if b.Status!=environmentbuild.Building && b.Status!=environmentbuild.Validating && b.Status!=environmentbuild.Pushing && b.Status!=environmentbuild.VerifyingPull {return environmentbuild.StepResult{},environmentbuild.ErrInvalid}
 if b.Status==environmentbuild.Building {if err:=r.ensureArtifacts(ctx,b);err!=nil {return environmentbuild.StepResult{},err}}
 if b.Status==environmentbuild.Pushing {if credentials==nil {return environmentbuild.StepResult{},environmentbuild.ErrAuthorization};if err:=r.ensurePublishSecret(ctx,b,*credentials);err!=nil {return environmentbuild.StepResult{},err}}
 desired,err:=r.renderJob(b);if err!=nil {return environmentbuild.StepResult{},err}
 jobs:=r.client.kubernetes.BatchV1().Jobs(r.config.Namespace)
 job,err:=jobs.Get(ctx,desired.Name,metav1.GetOptions{})
 if apierrors.IsNotFound(err) {job,err=jobs.Create(ctx,desired,metav1.CreateOptions{});if apierrors.IsAlreadyExists(err) {job,err=jobs.Get(ctx,desired.Name,metav1.GetOptions{})}}
 if err!=nil {return environmentbuild.StepResult{},err}
 if !r.owned(job,b) {return environmentbuild.StepResult{},fmt.Errorf("refusing unmanaged environment Job")}
 return r.observeJob(ctx,b,job)
}
func (r *EnvironmentRunner) metadata(name string,b environmentbuild.Build) metav1.ObjectMeta {
 ls:=environmentLabels(b.ID);ls[environmentOwnerLabel]=environmentHash(b.TenantID+"\x00"+b.OwnerID)
 return metav1.ObjectMeta{Name:name,Namespace:r.config.Namespace,Labels:ls,Annotations:map[string]string{environmentIdentityAnnotation:b.ID}}
}
func (r *EnvironmentRunner) owned(meta metav1.Object,b environmentbuild.Build) bool {return environmentOwned(meta,b.ID) && meta.GetLabels()[environmentOwnerLabel]==environmentHash(b.TenantID+"\x00"+b.OwnerID)}
func environmentJobName(b environmentbuild.Build) string {return environmentResourceName("job",b.ID+"/"+b.Status+"/"+strconv.Itoa(b.Attempt))}
func environmentPublishSecretName(b environmentbuild.Build) string {return environmentResourceName("push",b.ID+"/"+strconv.Itoa(b.Attempt))}
func (r *EnvironmentRunner) ensureArtifacts(ctx context.Context,b environmentbuild.Build) error {
 if r.config.StorageGiB<40 || r.config.StorageGiB>80 || len(b.SnapshotJSON)==0 || len(b.SnapshotJSON)>1024*1024 || !json.Valid([]byte(b.SnapshotJSON)) {return environmentbuild.ErrInvalid}
 pvcs:=r.client.kubernetes.CoreV1().PersistentVolumeClaims(r.config.Namespace)
 name:=environmentResourceName("oci",b.ID)
 pvc,err:=pvcs.Get(ctx,name,metav1.GetOptions{})
 if apierrors.IsNotFound(err) {
  pvc,err=pvcs.Create(ctx,&corev1.PersistentVolumeClaim{ObjectMeta:r.metadata(name,b),Spec:corev1.PersistentVolumeClaimSpec{AccessModes:[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},StorageClassName:&r.config.StorageClass,Resources:corev1.VolumeResourceRequirements{Requests:corev1.ResourceList{corev1.ResourceStorage:resource.MustParse(strconv.Itoa(r.config.StorageGiB)+"Gi")}}}},metav1.CreateOptions{})
  if apierrors.IsAlreadyExists(err) {pvc,err=pvcs.Get(ctx,name,metav1.GetOptions{})}
 }
 if err!=nil {return err};if !r.owned(pvc,b) {return fmt.Errorf("refusing unmanaged environment volume")}
 maps:=r.client.kubernetes.CoreV1().ConfigMaps(r.config.Namespace);name=environmentResourceName("snapshot",b.ID)
 cm,err:=maps.Get(ctx,name,metav1.GetOptions{})
 if apierrors.IsNotFound(err) {immutable:=true;cm,err=maps.Create(ctx,&corev1.ConfigMap{ObjectMeta:r.metadata(name,b),Immutable:&immutable,Data:map[string]string{"capture.json":b.SnapshotJSON}},metav1.CreateOptions{});if apierrors.IsAlreadyExists(err) {cm,err=maps.Get(ctx,name,metav1.GetOptions{})}}
 if err!=nil {return err};if !r.owned(cm,b) || cm.Data["capture.json"]!=b.SnapshotJSON {return fmt.Errorf("environment snapshot conflict")};return nil
}
func (r *EnvironmentRunner) ensurePublishSecret(ctx context.Context,b environmentbuild.Build,credentials environmentbuild.Credentials) error {
 if credentials.Username=="" || credentials.Secret=="" || len(credentials.Username)>512 || len(credentials.Secret)>32768 {return environmentbuild.ErrAuthorization}
 secrets:=r.client.kubernetes.CoreV1().Secrets(r.config.Namespace);name:=environmentPublishSecretName(b)
 secret,err:=secrets.Get(ctx,name,metav1.GetOptions{})
 if apierrors.IsNotFound(err) {immutable:=true;secret,err=secrets.Create(ctx,&corev1.Secret{ObjectMeta:r.metadata(name,b),Type:corev1.SecretTypeOpaque,Immutable:&immutable,Data:map[string][]byte{"username":[]byte(credentials.Username),"secret":[]byte(credentials.Secret)}},metav1.CreateOptions{});if apierrors.IsAlreadyExists(err) {secret,err=secrets.Get(ctx,name,metav1.GetOptions{})}}
 if err!=nil {return fmt.Errorf("create scoped publishing credential failed")};if !r.owned(secret,b) {return fmt.Errorf("refusing unmanaged publishing credential")};return nil
}
func (r *EnvironmentRunner) observeJob(ctx context.Context,b environmentbuild.Build,job *batchv1.Job) (environmentbuild.StepResult,error) {
 failed:=false
 for _,condition:=range job.Status.Conditions {if condition.Type==batchv1.JobFailed && condition.Status==corev1.ConditionTrue {
  failed=true
  if condition.Reason=="DeadlineExceeded" {code:="BUILD_TIMEOUT";if b.Status==environmentbuild.VerifyingPull {code="PULL_FAILED"};return environmentbuild.StepResult{},&environmentbuild.PhaseError{Code:code}}
 }}
 if job.Status.Succeeded<1 && !failed {return environmentbuild.StepResult{},nil}
 pods,err:=r.client.kubernetes.CoreV1().Pods(r.config.Namespace).List(ctx,metav1.ListOptions{LabelSelector:labels.Set{"batch.kubernetes.io/job-name":job.Name}.AsSelector().String()});if err!=nil {return environmentbuild.StepResult{},err}
 for _,pod:=range pods.Items {
  if !r.owned(&pod,b) || (!failed && pod.Status.Phase!=corev1.PodSucceeded) {continue}
  owner:=false;for _,ref:=range pod.OwnerReferences {if ref.Kind=="Job" && ref.UID==job.UID && ref.Controller!=nil && *ref.Controller {owner=true}}
  if !owner {continue}
  for _,status:=range pod.Status.ContainerStatuses {
   if status.Name!="environment" || status.State.Terminated==nil {continue}
   if failed {
    var failure struct {ErrorCode string `json:"errorCode"`}
    if b.Status==environmentbuild.Pushing && len(status.State.Terminated.Message)<=4096 && json.Unmarshal([]byte(status.State.Terminated.Message),&failure)==nil && (failure.ErrorCode=="REGISTRY_AUTH_REQUIRED" || failure.ErrorCode=="REGISTRY_PUSH_DENIED") {return environmentbuild.StepResult{},environmentbuild.ErrAuthorization}
    if safe:=environmentSafeFailure([]byte(status.State.Terminated.Message));safe!=nil {return environmentbuild.StepResult{},safe}
    if b.Status==environmentbuild.VerifyingPull {return environmentbuild.StepResult{},&environmentbuild.PhaseError{Code:"PULL_FAILED"}}
    return environmentbuild.StepResult{},fmt.Errorf("environment Job failed")
   }
   if status.State.Terminated.ExitCode!=0 {continue}
   if b.Status==environmentbuild.VerifyingPull {return environmentbuild.StepResult{Done:true,ChecksJSON:`{"pullVerified":true,"cpuImportCheck":true,"gpuValidation":"not_run"}`},nil}
   message:=status.State.Terminated.Message
   if len(message)>4096 {return environmentbuild.StepResult{},fmt.Errorf("environment result exceeds limit")}
   var result struct {ArtifactDigest string `json:"artifactDigest"`;LayerSHA256 string `json:"layerSha256"`;CaptureSHA256 string `json:"captureSha256"`;Checks map[string]any `json:"checks"`;ImageDigest string `json:"imageDigest"`;Digest string `json:"digest"`}
   if json.Unmarshal([]byte(message),&result)!=nil {return environmentbuild.StepResult{},fmt.Errorf("environment Job returned invalid result")}
   if b.Status==environmentbuild.Building && environmentDigestPattern.MatchString(result.LayerSHA256) {
    expected:=sha256.Sum256([]byte(b.SnapshotJSON))
    if result.CaptureSHA256!=hex.EncodeToString(expected[:]) {return environmentbuild.StepResult{},fmt.Errorf("environment build capture provenance mismatch")}
    for _,check:=range []string{"wheelHashesVerified","rebuiltFilesVerified","pipCheck","platformCPU"} {if result.Checks[check]!=true {return environmentbuild.StepResult{},fmt.Errorf("environment build validation incomplete")}}
    return environmentbuild.StepResult{Done:true,ArtifactDigest:result.LayerSHA256,ChecksJSON:`{"layerDigest":"`+result.LayerSHA256+`"}`},nil
   }
   if b.Status==environmentbuild.Validating && environmentDigestPattern.MatchString(result.ArtifactDigest) {return environmentbuild.StepResult{Done:true,ArtifactDigest:result.ArtifactDigest,ChecksJSON:`{"ociBuilt":true,"dependencyCheck":true,"cpuImportCheck":true,"gpuValidation":"not_run"}`},nil}
   if b.Status==environmentbuild.Pushing {digest:=result.ImageDigest;if digest=="" {digest=result.Digest};if environmentDigestPattern.MatchString(digest) && digest==b.ArtifactDigest {return environmentbuild.StepResult{Done:true,ImageDigest:digest},nil}}
   return environmentbuild.StepResult{},fmt.Errorf("environment Job did not return the expected image digest")
  }
 }
 return environmentbuild.StepResult{},fmt.Errorf("environment Job result is unavailable")
}

// Only these structured codes cross the command-output boundary. Unstructured
// stderr, Kubernetes messages, URLs and unknown codes are never surfaced.
func environmentSafeFailure(data []byte) error {
 if len(data)>4096 {return nil}
 var failure struct {ErrorCode string `json:"errorCode"`}
 if json.Unmarshal(data,&failure)!=nil {return nil}
 switch failure.ErrorCode {
 case "UNSUPPORTED_WORKSPACE","ENVIRONMENT_CHANGED","WHEEL_UNAVAILABLE","PACKAGE_MODIFIED","BUILD_TIMEOUT","PULL_FAILED","TEMP_STORAGE_FULL":
  return &environmentbuild.PhaseError{Code:failure.ErrorCode}
 default:return nil
 }
}
