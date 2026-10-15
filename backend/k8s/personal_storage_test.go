package k8s
import (
 "context"
 "testing"
 appsv1 "k8s.io/api/apps/v1"
 storagev1 "k8s.io/api/storage/v1"
 metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
 "k8s.io/apimachinery/pkg/types"
 kfake "k8s.io/client-go/kubernetes/fake"
 corev1 "k8s.io/api/core/v1"
 "ray-train-platform-backend/personalcsi"
 "ray-train-platform-backend/personalstorage"
 "strings"
 "ray-train-platform-backend/domain"
)
func TestPersonalStorageOnlyEditorWorkerMountsClaim(t *testing.T) {
 workspace := domain.DevWorkspace{ID:"ws-1",TenantID:"team-a",UserID:"alice",Name:"debug-a",Namespace:"tenant-a",GPUCount:0}
 manifest, err := RenderDevRayCluster(workspace,WorkspaceRenderOptions{Image:"registry.example/dev@sha256:"+strings.Repeat("a",64),PersonalStorageClaim:"personal-alice"})
 if err != nil { t.Fatal(err) }
 head,_,_ := nestedMap(manifest.Object,"spec","headGroupSpec")
 if strings.Contains(string(mustJSON(head)),"personal-alice") || strings.Contains(string(mustJSON(head)),"/mnt/storage/wellspiking") { t.Fatal("Ray head must not mount personal SFTP") }
 workers,_,_ := nestedSlice(manifest.Object,"spec","workerGroupSpecs")
 body := string(mustJSON(workers))
 if !strings.Contains(body,"personal-alice") || !strings.Contains(body,"/mnt/storage/wellspiking") { t.Fatalf("editor worker lacks personal mount: %s",body) }
 if strings.Contains(body,"nodePublishSecretRef") || strings.Contains(body,"/dev/fuse") || strings.Contains(body,"SYS_ADMIN") { t.Fatal("workload must receive only PVC, never credentials or FUSE privilege") }
}

func TestPersonalStorageMountIdentityIncludesOwnerGenerationNamespace(t *testing.T){
 reference:=personalstorage.MountReference{OwnerID:"stable-alice",BindingID:"binding-a",Username:"alice",Generation:1,SecretName:"personal-binding-a"}
 pv,pvc,err:=BuildPersonalStorageMountResources(reference,"tenant-a","platform","1Ti")
 if err!=nil{t.Fatal(err)}
 if pv.Spec.CSI.Driver!=personalcsi.DriverName||pv.Spec.PersistentVolumeReclaimPolicy!=corev1.PersistentVolumeReclaimRetain||pv.Spec.ClaimRef.Namespace!="tenant-a"||pv.Spec.ClaimRef.Name!=pvc.Name||pv.Spec.CSI.NodePublishSecretRef.Namespace!="platform"{t.Fatalf("unsafe personal mount %#v",pv.Spec)}
 if _,ok:=pv.Spec.CSI.VolumeAttributes["privateKey"];ok{t.Fatal("private key must not be in PV attributes")}
 for _,variant:=range []struct{owner string;generation int64;namespace string}{{"stable-bob",1,"tenant-a"},{"stable-alice",2,"tenant-a"},{"stable-alice",1,"tenant-b"}}{
  next:=reference;next.OwnerID=variant.owner;next.Generation=variant.generation
  other,_,err:=BuildPersonalStorageMountResources(next,variant.namespace,"platform","1Ti")
  if err!=nil||other.Name==pv.Name{t.Fatalf("owner/generation/namespace collision %#v %v",variant,err)}
 }
 foreign:=pv.DeepCopy();foreign.Spec.CSI.VolumeAttributes["ownerID"]="bob"
 if err:=verifyWorkspacePersistentVolume(foreign,pv);err==nil{t.Fatal("must refuse foreign personal CSI PV adoption")}
 foreign=pv.DeepCopy();foreign.Spec.CSI.NodePublishSecretRef.Name="another-user"
 if err:=verifyWorkspacePersistentVolume(foreign,pv);err==nil{t.Fatal("must refuse foreign credential reference")}
 foreign=pv.DeepCopy();foreign.Spec.ClaimRef.Namespace="tenant-b"
 if err:=verifyWorkspacePersistentVolume(foreign,pv);err==nil{t.Fatal("must refuse foreign namespace claim")}
}

func TestPersonalStorageRequiresRegisteredReadyPinnedNodeDriver(t *testing.T){
 reference:=personalstorage.MountReference{OwnerID:"alice",BindingID:"binding-a",Username:"alice",Generation:1,SecretName:"personal-a"}
 immutable,attach,controller:=true,false,true
 image:="registry/csi@sha256:"+strings.Repeat("a",64)
 secret:=&corev1.Secret{ObjectMeta:metav1.ObjectMeta{Name:reference.SecretName,Namespace:"platform",Labels:map[string]string{personalstorage.LabelPersonalStorage:"true",personalstorage.LabelBindingID:reference.BindingID},Annotations:map[string]string{personalstorage.AnnotationOwnerID:reference.OwnerID}},Type:corev1.SecretTypeOpaque,Immutable:&immutable,Data:map[string][]byte{"ownerID":[]byte("alice"),"bindingID":[]byte("binding-a"),"username":[]byte("alice"),"generation":[]byte("1"),"privateKey":[]byte("fixture")}}
 driver:=&storagev1.CSIDriver{ObjectMeta:metav1.ObjectMeta{Name:personalcsi.DriverName},Spec:storagev1.CSIDriverSpec{AttachRequired:&attach}}
 daemon:=&appsv1.DaemonSet{ObjectMeta:metav1.ObjectMeta{Name:"personal-storage-csi",Namespace:"platform",UID:types.UID("daemon-id"),Generation:1},Spec:appsv1.DaemonSetSpec{Template:corev1.PodTemplateSpec{Spec:corev1.PodSpec{Containers:[]corev1.Container{{Name:"personal-storage-csi",Image:image}}}}},Status:appsv1.DaemonSetStatus{ObservedGeneration:1,DesiredNumberScheduled:1,NumberReady:1,UpdatedNumberScheduled:1}}
 pod:=&corev1.Pod{ObjectMeta:metav1.ObjectMeta{Name:"csi-node-a",Namespace:"platform",Labels:map[string]string{"app":"personal-storage-csi"},OwnerReferences:[]metav1.OwnerReference{{Kind:"DaemonSet",Name:daemon.Name,UID:daemon.UID,Controller:&controller}}},Spec:corev1.PodSpec{NodeName:"node-a"},Status:corev1.PodStatus{Conditions:[]corev1.PodCondition{{Type:corev1.PodReady,Status:corev1.ConditionTrue}}}}
 node:=&storagev1.CSINode{ObjectMeta:metav1.ObjectMeta{Name:"node-a"},Spec:storagev1.CSINodeSpec{Drivers:[]storagev1.CSINodeDriver{{Name:personalcsi.DriverName,NodeID:"node-a"}}}}
 client:=kfake.NewSimpleClientset(secret,driver,daemon,pod)
 platform:=NewClientFromInterfaces(nil,client)
 if err:=platform.validatePersonalStorageRuntime(context.Background(),reference,"platform",image);err==nil{t.Fatal("ready daemon without kubelet registration accepted")}
 if _,err:=client.StorageV1().CSINodes().Create(context.Background(),node,metav1.CreateOptions{});err!=nil{t.Fatal(err)}
 if err:=platform.validatePersonalStorageRuntime(context.Background(),reference,"platform",image);err!=nil{t.Fatalf("registered pinned driver rejected %v",err)}
 if err:=platform.validatePersonalStorageRuntime(context.Background(),reference,"platform","registry/other@sha256:"+strings.Repeat("b",64));err==nil{t.Fatal("unexpected node driver image accepted")}
 changed:=secret.DeepCopy();changed.Data["ownerID"]=[]byte("bob")
 if _,err:=client.CoreV1().Secrets("platform").Update(context.Background(),changed,metav1.UpdateOptions{});err!=nil{t.Fatal(err)}
 if err:=platform.validatePersonalStorageRuntime(context.Background(),reference,"platform",image);err==nil{t.Fatal("another personal owner's Secret accepted")}
 pv,pvc,err:=BuildPersonalStorageMountResources(reference,"tenant-a","platform","1Ti");if err!=nil{t.Fatal(err)}
 if len(pv.Spec.AccessModes)!=1||pv.Spec.AccessModes[0]!=corev1.ReadWriteOnce||len(pvc.Spec.AccessModes)!=1||pvc.Spec.AccessModes[0]!=corev1.ReadWriteOnce{t.Fatal("CSI SINGLE_NODE_WRITER requires ReadWriteOnce PV and PVC")}
}
