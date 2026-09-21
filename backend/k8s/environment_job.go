package k8s

import (
 "fmt"
 "encoding/json"
 "maps"
 "strings"
 batchv1 "k8s.io/api/batch/v1"
 corev1 "k8s.io/api/core/v1"
 "k8s.io/apimachinery/pkg/api/resource"
 metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
 "ray-train-platform-backend/environmentbuild"
)

func (r *EnvironmentRunner) renderJob(b environmentbuild.Build) (*batchv1.Job,error) {
 if r.config.JobTimeout.Seconds()<60 || r.config.JobTimeout.Seconds()>7200 {return nil,environmentbuild.ErrInvalid}
 zero:=int32(0);one:=int32(1);seconds:=int64(r.config.JobTimeout.Seconds());uid:=int64(1000);no:=false;yes:=true;mode:=int32(0440)
 security:=&corev1.SecurityContext{RunAsNonRoot:&yes,RunAsUser:&uid,RunAsGroup:&uid,AllowPrivilegeEscalation:&no,Capabilities:&corev1.Capabilities{Drop:[]corev1.Capability{"ALL"}},SeccompProfile:&corev1.SeccompProfile{Type:corev1.SeccompProfileTypeRuntimeDefault}}
 container:=corev1.Container{Name:"environment",ImagePullPolicy:corev1.PullAlways,SecurityContext:security,TerminationMessagePath:"/dev/termination-log",TerminationMessagePolicy:corev1.TerminationMessageReadFile,Resources:corev1.ResourceRequirements{Requests:corev1.ResourceList{corev1.ResourceCPU:resource.MustParse("1"),corev1.ResourceMemory:resource.MustParse("2Gi")},Limits:corev1.ResourceList{corev1.ResourceCPU:resource.MustParse("4"),corev1.ResourceMemory:resource.MustParse("8Gi")}}}
 pod:=corev1.PodSpec{RestartPolicy:corev1.RestartPolicyNever,AutomountServiceAccountToken:&no,SecurityContext:&corev1.PodSecurityContext{RunAsNonRoot:&yes,RunAsUser:&uid,RunAsGroup:&uid,FSGroup:&uid,SeccompProfile:&corev1.SeccompProfile{Type:corev1.SeccompProfileTypeRuntimeDefault}},NodeSelector:maps.Clone(r.config.NodeSelector),EnableServiceLinks:&no}
 for _,name:=range r.config.ImagePullSecrets {if !isDNSSubdomain(name) {return nil,environmentbuild.ErrInvalid};pod.ImagePullSecrets=append(pod.ImagePullSecrets,corev1.LocalObjectReference{Name:name})}
 if b.Status!=environmentbuild.VerifyingPull {
  pod.Volumes=append(pod.Volumes,corev1.Volume{Name:"artifacts",VolumeSource:corev1.VolumeSource{PersistentVolumeClaim:&corev1.PersistentVolumeClaimVolumeSource{ClaimName:environmentResourceName("oci",b.ID)}}})
  container.VolumeMounts=append(container.VolumeMounts,corev1.VolumeMount{Name:"artifacts",MountPath:"/artifacts",ReadOnly:b.Status==environmentbuild.Pushing})
 }
 switch b.Status {
 case environmentbuild.Building:
  container.Image=r.config.PrepareImage
  container.Command=[]string{"/usr/local/bin/raytrain-environment-build"}
  container.Args=[]string{"--manifest","/snapshot/capture.json","--artifacts","/artifacts","--index-url",r.config.WheelIndexURL,"--result","/dev/termination-log"}
  pod.Volumes=append(pod.Volumes,corev1.Volume{Name:"snapshot",VolumeSource:corev1.VolumeSource{ConfigMap:&corev1.ConfigMapVolumeSource{LocalObjectReference:corev1.LocalObjectReference{Name:environmentResourceName("snapshot",b.ID)},DefaultMode:&mode}}})
  container.VolumeMounts=append(container.VolumeMounts,corev1.VolumeMount{Name:"snapshot",MountPath:"/snapshot",ReadOnly:true})
 case environmentbuild.Validating:
  var provenance struct {LayerDigest string `json:"layerDigest"`}
  if json.Unmarshal([]byte(b.ChecksJSON),&provenance)!=nil || !environmentDigestPattern.MatchString(provenance.LayerDigest) {return nil,fmt.Errorf("environment layer provenance is unavailable")}
  if !isDNSSubdomain(r.config.PullSecretName) {return nil,fmt.Errorf("environment assembler pull authorization is not configured")}
  container.Image=r.config.PublisherImage
  container.Command=[]string{"/usr/local/bin/raytrain-environment-assembler"}
  container.Args=[]string{"--base",r.config.BaseImage,"--layer","/artifacts/layer.tar","--layer-digest",provenance.LayerDigest,"--layout","/artifacts/oci","--pull-config","/pull/.dockerconfigjson","--result","/dev/termination-log"}
  pod.Volumes=append(pod.Volumes,corev1.Volume{Name:"base-pull",VolumeSource:corev1.VolumeSource{Secret:&corev1.SecretVolumeSource{SecretName:r.config.PullSecretName,DefaultMode:&mode,Items:[]corev1.KeyToPath{{Key:corev1.DockerConfigJsonKey,Path:corev1.DockerConfigJsonKey}}}}})
  container.VolumeMounts=append(container.VolumeMounts,corev1.VolumeMount{Name:"base-pull",MountPath:"/pull",ReadOnly:true})
 case environmentbuild.Pushing:
  if !environmentDigestPattern.MatchString(b.ArtifactDigest) {return nil,environmentbuild.ErrInvalid}
  container.Image=r.config.PublisherImage
  container.Command=[]string{"/usr/local/bin/raytrain-environment-publisher"}
  container.Args=[]string{"--layout","/artifacts/oci","--digest",b.ArtifactDigest,"--project",b.Project,"--repository",b.Repository,"--tag",b.Tag,"--credentials-dir","/registry-credentials","--result","/dev/termination-log"}
  pod.Volumes=append(pod.Volumes,corev1.Volume{Name:"registry-credentials",VolumeSource:corev1.VolumeSource{Secret:&corev1.SecretVolumeSource{SecretName:environmentPublishSecretName(b),DefaultMode:&mode}}})
  container.VolumeMounts=append(container.VolumeMounts,corev1.VolumeMount{Name:"registry-credentials",MountPath:"/registry-credentials",ReadOnly:true})
 case environmentbuild.VerifyingPull:
  if !environmentDigestPattern.MatchString(b.ImageDigest) {return nil,environmentbuild.ErrInvalid}
  container.Image=environmentbuild.RegistryHost+"/"+b.Project+"/"+b.Repository+"@"+b.ImageDigest
  container.Command=[]string{"/bin/sh","-c"}
  container.Args=[]string{"/usr/local/bin/raytrain-environment verify --manifest /opt/raytrain/environment-materials/capture.json && /usr/local/bin/raytrain-selfcheck"}
  container.Env=[]corev1.EnvVar{{Name:"NVIDIA_VISIBLE_DEVICES",Value:"void"},{Name:"CUDA_VISIBLE_DEVICES",Value:""}}
 default:return nil,environmentbuild.ErrInvalid
 }
 if !environmentPinnedImage(container.Image) || strings.ContainsAny(container.Image,"\r\n\t ") {return nil,fmt.Errorf("environment execution requires pinned Harbor images")}
 pod.Containers=[]corev1.Container{container}
 return &batchv1.Job{ObjectMeta:r.metadata(environmentJobName(b),b),Spec:batchv1.JobSpec{BackoffLimit:&zero,Completions:&one,Parallelism:&one,ActiveDeadlineSeconds:&seconds,Template:corev1.PodTemplateSpec{ObjectMeta:metav1.ObjectMeta{Labels:r.metadata("",b).Labels,Annotations:r.metadata("",b).Annotations},Spec:pod}}},nil
}
