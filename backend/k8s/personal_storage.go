package k8s

import (
 "context"
 "fmt"
 "strconv"

 corev1 "k8s.io/api/core/v1"
 metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
 "k8s.io/apimachinery/pkg/api/resource"
 "ray-train-platform-backend/domain"
 "ray-train-platform-backend/personalcsi"
 "ray-train-platform-backend/personalstorage"
)

func (c *Client) PersonalStorageVault(namespace string) (*personalstorage.KubernetesVault,error) {
 if c==nil || c.kubernetes==nil || !isDNSLabel(namespace) { return nil,fmt.Errorf("personal storage Kubernetes vault is unavailable") }
 return personalstorage.NewKubernetesVault(c.kubernetes,namespace),nil
}

// BuildPersonalStorageMountResources reserves one namespace-local claim per
// stable personal identity and generation. No workload receives the Secret.
func BuildPersonalStorageMountResources(reference personalstorage.MountReference,namespace,secretNamespace,capacity string) (*corev1.PersistentVolume,*corev1.PersistentVolumeClaim,error) {
 if personalstorage.ValidateOwner(personalstorage.Owner{ID:reference.OwnerID,Username:reference.Username})!=nil || !isDNSLabel(reference.BindingID) || reference.Generation<1 || !isDNSLabel(reference.SecretName) || !isDNSLabel(namespace) || !isDNSLabel(secretNamespace) { return nil,nil,fmt.Errorf("invalid personal mount identity") }
 quantity,err:=resource.ParseQuantity(capacity)
 if err!=nil || quantity.Sign()<=0 { return nil,nil,fmt.Errorf("invalid personal mount capacity") }
 name:=personalcsi.VolumeID(reference.OwnerID,reference.BindingID,namespace,reference.Generation)
 labels:=func()map[string]string{return map[string]string{"app.kubernetes.io/part-of":"ray-train-platform","app.kubernetes.io/managed-by":"ray-train-platform",managedDataMountLabel:name}}
 attrs:=map[string]string{"ownerID":reference.OwnerID,"bindingID":reference.BindingID,"username":reference.Username,"generation":strconv.FormatInt(reference.Generation,10),"namespace":namespace}
 pv:=&corev1.PersistentVolume{ObjectMeta:metav1.ObjectMeta{Name:name,Labels:labels()},Spec:corev1.PersistentVolumeSpec{
  Capacity:corev1.ResourceList{corev1.ResourceStorage:quantity},AccessModes:[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},PersistentVolumeReclaimPolicy:corev1.PersistentVolumeReclaimRetain,
  ClaimRef:&corev1.ObjectReference{APIVersion:"v1",Kind:"PersistentVolumeClaim",Name:name,Namespace:namespace},
  PersistentVolumeSource:corev1.PersistentVolumeSource{CSI:&corev1.CSIPersistentVolumeSource{Driver:personalcsi.DriverName,VolumeHandle:name,VolumeAttributes:attrs,NodePublishSecretRef:&corev1.SecretReference{Name:reference.SecretName,Namespace:secretNamespace}}},
 }}
 pvc:=&corev1.PersistentVolumeClaim{ObjectMeta:metav1.ObjectMeta{Name:name,Namespace:namespace,Labels:labels()},Spec:corev1.PersistentVolumeClaimSpec{VolumeName:name,StorageClassName:noStorageClass(),AccessModes:[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},Resources:corev1.VolumeResourceRequirements{Requests:corev1.ResourceList{corev1.ResourceStorage:quantity}}}}
 return pv,pvc,nil
}

func (c *Client) validatePersonalStorageRuntime(ctx context.Context,reference personalstorage.MountReference,platformNamespace,expectedImage string) error {
 if c==nil || c.kubernetes==nil || expectedImage=="" { return fmt.Errorf("personal storage driver is unavailable") }
 secret,err:=c.kubernetes.CoreV1().Secrets(platformNamespace).Get(ctx,reference.SecretName,metav1.GetOptions{})
 if err!=nil { return fmt.Errorf("personal storage credential is unavailable") }
 if secret.DeletionTimestamp!=nil || secret.Immutable==nil || !*secret.Immutable || secret.Type!=corev1.SecretTypeOpaque || secret.Labels[personalstorage.LabelPersonalStorage]!="true" || secret.Labels[personalstorage.LabelBindingID]!=reference.BindingID || secret.Annotations[personalstorage.AnnotationOwnerID]!=reference.OwnerID || string(secret.Data["ownerID"])!=reference.OwnerID || string(secret.Data["bindingID"])!=reference.BindingID || string(secret.Data["username"])!=reference.Username || string(secret.Data["generation"])!=strconv.FormatInt(reference.Generation,10) || len(secret.Data["privateKey"])==0 { return fmt.Errorf("personal storage credential identity mismatch") }
 driver,err:=c.kubernetes.StorageV1().CSIDrivers().Get(ctx,personalcsi.DriverName,metav1.GetOptions{})
 if err!=nil || driver.DeletionTimestamp!=nil || driver.Spec.AttachRequired==nil || *driver.Spec.AttachRequired { return fmt.Errorf("personal storage CSI driver is not ready") }
 daemon,err:=c.kubernetes.AppsV1().DaemonSets(platformNamespace).Get(ctx,"personal-storage-csi",metav1.GetOptions{})
 if err!=nil || daemon.DeletionTimestamp!=nil || daemon.Status.ObservedGeneration<daemon.Generation || daemon.Status.DesiredNumberScheduled<1 || daemon.Status.NumberReady!=daemon.Status.DesiredNumberScheduled || daemon.Status.UpdatedNumberScheduled!=daemon.Status.DesiredNumberScheduled { return fmt.Errorf("personal storage node driver is not ready") }
 found:=false
 for _,container:=range daemon.Spec.Template.Spec.Containers { if container.Name=="personal-storage-csi" && container.Image==expectedImage { found=true } }
 if !found { return fmt.Errorf("personal storage node driver image does not match pinned configuration") }
 pods,err:=c.kubernetes.CoreV1().Pods(platformNamespace).List(ctx,metav1.ListOptions{LabelSelector:"app=personal-storage-csi"})
 if err!=nil{return fmt.Errorf("personal storage node registration is unavailable")}
 registered:=map[string]bool{}
 for _,pod:=range pods.Items {
  owned:=false
  for _,ref:=range pod.OwnerReferences{if ref.Kind=="DaemonSet" && ref.Name==daemon.Name && ref.UID==daemon.UID && ref.Controller!=nil && *ref.Controller{owned=true}}
  if !owned || pod.DeletionTimestamp!=nil || pod.Spec.NodeName==""{continue}
  ready:=false
  for _,condition:=range pod.Status.Conditions{if condition.Type==corev1.PodReady && condition.Status==corev1.ConditionTrue{ready=true}}
  if !ready{continue}
  node,err:=c.kubernetes.StorageV1().CSINodes().Get(ctx,pod.Spec.NodeName,metav1.GetOptions{})
  if err!=nil{return fmt.Errorf("personal storage node plugin is not registered")}
  for _,driver:=range node.Spec.Drivers{if driver.Name==personalcsi.DriverName && driver.NodeID==pod.Spec.NodeName{registered[pod.Spec.NodeName]=true}}
 }
 if len(registered)<int(daemon.Status.DesiredNumberScheduled){return fmt.Errorf("personal storage node plugin registration is incomplete")}
 return nil
}

func (c *Client) EnsurePersonalStorageMountResources(ctx context.Context,reference personalstorage.MountReference,namespace,platformNamespace,capacity,expectedImage string) (string,bool,error) {
 if err:=c.validatePersonalStorageRuntime(ctx,reference,platformNamespace,expectedImage);err!=nil{return "",false,err}
 pv,pvc,err:=BuildPersonalStorageMountResources(reference,namespace,platformNamespace,capacity)
 if err!=nil{return "",false,err}
 owner:=pv.Labels[managedDataMountLabel]
 volumes:=c.kubernetes.CoreV1().PersistentVolumes()
 if err=ensureOwnedPersistentVolume(ctx,volumes,pv,owner);err!=nil{return "",false,err}
 existingPV,err:=volumes.Get(ctx,pv.Name,metav1.GetOptions{})
 if err!=nil{return "",false,err}
 if err=verifyWorkspacePersistentVolume(existingPV,pv);err!=nil{return "",false,err}
 existingPVC,err:=ensureOwnedPersistentVolumeClaim(ctx,c.kubernetes.CoreV1().PersistentVolumeClaims(namespace),pvc,owner)
 if err!=nil{return "",false,err}
 if err=verifyWorkspacePersistentVolumeClaim(existingPVC,pvc,existingPV.Spec.ClaimRef);err!=nil{return "",false,err}
 return pvc.Name,existingPVC.Status.Phase==corev1.ClaimBound,nil
}

// PersonalStorageWorkspaceMountStatus observes the current worker; it does not
// create volumes, alter a running workspace, or claim a fresh upstream probe.
func (c *Client) PersonalStorageWorkspaceMountStatus(ctx context.Context,workspace domain.DevWorkspace,reference personalstorage.MountReference,platformNamespace,capacity string) (string,error) {
 if c==nil || c.kubernetes==nil {return "unavailable",fmt.Errorf("Kubernetes unavailable")}
 desired,claim,err:=BuildPersonalStorageMountResources(reference,workspace.Namespace,platformNamespace,capacity)
 if err!=nil{return "unavailable",err}
 cluster,err:=c.GetRayCluster(ctx,workspace.Namespace,workspace.RayClusterName)
 if err!=nil{return "unavailable",err}
 if cluster.GetLabels()["ray.io/workspace-id"]!=workspace.ID{return "unavailable",fmt.Errorf("workspace ownership mismatch")}
 // A pre-existing workspace intentionally remains unmodified and unmounted.
 groups,_,_:=nestedSlice(cluster.Object,"spec","workerGroupSpecs")
 advertised:=false
 for _,item:=range groups { group,_:=item.(map[string]any);volumes,_,_:=nestedSlice(group,"template","spec","volumes");for _,item:=range volumes { volume,_:=item.(map[string]any);pvc,_:=volume["persistentVolumeClaim"].(map[string]any);if pvc["claimName"]==claim.Name{advertised=true} } }
 if !advertised{return "not-mounted",nil}
 existing,err:=c.kubernetes.CoreV1().PersistentVolumes().Get(ctx,desired.Name,metav1.GetOptions{})
 if err!=nil || verifyWorkspacePersistentVolume(existing,desired)!=nil{return "unavailable",fmt.Errorf("personal volume ownership is unavailable")}
 pods,err:=c.kubernetes.CoreV1().Pods(workspace.Namespace).List(ctx,metav1.ListOptions{LabelSelector:"ray.io/cluster="+workspace.RayClusterName+",ray.io/node-type=worker"})
 if err!=nil{return "unavailable",err}
 for _,pod:=range pods.Items {
  if pod.DeletionTimestamp!=nil || pod.Status.Phase==corev1.PodFailed{return "failed",nil}
  hasVolume:=false
  for _,volume:=range pod.Spec.Volumes{if volume.PersistentVolumeClaim!=nil && volume.PersistentVolumeClaim.ClaimName==claim.Name{hasVolume=true}}
  if !hasVolume{continue}
  for _,container:=range pod.Spec.Containers{
   if container.Name!="ray-worker"{continue}
   mounted:=false
   for _,mount:=range container.VolumeMounts {if mount.MountPath==domain.WellspikingPersonalMountPath && mount.SubPath=="" {for _,volume:=range pod.Spec.Volumes{if volume.Name==mount.Name && volume.PersistentVolumeClaim!=nil && volume.PersistentVolumeClaim.ClaimName==claim.Name{mounted=true}}}}
   for _,status:=range pod.Status.ContainerStatuses{if status.Name==container.Name && status.Ready && mounted && status.State.Running!=nil{return "mounted",nil}}
  }
 }
 return "pending",nil
}

