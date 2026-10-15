package personalcsi

import (
 "context"
 "os"
 "path/filepath"
 "testing"

 "github.com/container-storage-interface/spec/lib/go/csi"
 "google.golang.org/grpc/codes"
 "google.golang.org/grpc/status"
 "ray-train-platform-backend/personalstorage"
)

type testFactory struct { calls int }
func (f *testFactory) Open(context.Context, personalstorage.Credential) (personalstorage.Store,error) { f.calls++; return nil,nil }
type testMounted struct { closed bool }
func (m *testMounted) Close() error { m.closed=true; return nil }
type testMounter struct { mounts map[string]string; count int; unmounts int; cancelOnMount context.CancelFunc }
func (m *testMounter) Inspect(target string) (string,bool,error) { id,ok:=m.mounts[target]; return id,ok,nil }
func (m *testMounter) Mount(_ context.Context,target,id string,_ personalstorage.Store) (Mounted,error) { m.count++; m.mounts[target]=id; if m.cancelOnMount!=nil {m.cancelOnMount()};return &testMounted{},nil }
func (m *testMounter) Unmount(target string) error { m.unmounts++; delete(m.mounts,target); return nil }

func publishFixture(t *testing.T) (*Driver,*testFactory,*testMounter,*csi.NodePublishVolumeRequest) {
 t.Helper()
 if os.Geteuid()!=0 { t.Skip("CSI ancestry tests require a root-owned kubelet fixture; builder gate runs as root") }
 root:=t.TempDir()
 target:=filepath.Join(root,"pods","5c2f6ea8-2905-4f68-8ed5-0bad11111111","volumes","kubernetes.io~csi","personal-pv","mount")
 if err:=os.MkdirAll(target,0700); err!=nil { t.Fatal(err) }
 factory:=&testFactory{}
 mounter:=&testMounter{mounts:map[string]string{}}
 driver:=NewDriver("node-a",root,factory,mounter)
 req:=&csi.NodePublishVolumeRequest{VolumeId:VolumeID("owner-a","binding-a","tenant-a",1),TargetPath:target,
  VolumeCapability:&csi.VolumeCapability{AccessType:&csi.VolumeCapability_Mount{Mount:&csi.VolumeCapability_MountVolume{}},AccessMode:&csi.VolumeCapability_AccessMode{Mode:csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}},
  VolumeContext:map[string]string{"ownerID":"owner-a","bindingID":"binding-a","username":"alice","generation":"1","namespace":"tenant-a"},
  Secrets:map[string]string{"ownerID":"owner-a","bindingID":"binding-a","username":"alice","generation":"1","privateKey":"private-fixture"},
 }
 return driver,factory,mounter,req
}

func TestPersonalStorageRestartRefusesUnownedExistingMount(t *testing.T) {
 d,_,m,r:=publishFixture(t)
 m.mounts[r.TargetPath]=r.VolumeId
 if _,err:=d.NodePublishVolume(context.Background(),r);status.Code(err)!=codes.FailedPrecondition { t.Fatalf("restart adopted unknown mount: %v",err) }
 if m.count!=0 || m.unmounts!=0 { t.Fatal("unknown mount changed") }
}

func TestPersonalStoragePublishIdentityAndIdempotency(t *testing.T) {
 d,f,m,r:=publishFixture(t)
 if _,err:=d.NodePublishVolume(context.Background(),r);err!=nil { t.Fatal(err) }
 if _,err:=d.NodePublishVolume(context.Background(),r);err!=nil { t.Fatal(err) }
 if f.calls!=1 || m.count!=1 { t.Fatalf("idempotency: calls %d mounts %d",f.calls,m.count) }
 r.Secrets["privateKey"]="different-key"
 if _,err:=d.NodePublishVolume(context.Background(),r);status.Code(err)!=codes.PermissionDenied { t.Fatalf("changed credential adopted: %v",err) }
 r.Secrets["privateKey"]="private-fixture"
 r.Secrets["ownerID"]="owner-b"
 if _,err:=d.NodePublishVolume(context.Background(),r);status.Code(err)!=codes.PermissionDenied { t.Fatalf("foreign secret: %v",err) }
 r.Secrets["ownerID"]="owner-a"
 r.VolumeContext["generation"]="2"
 if _,err:=d.NodePublishVolume(context.Background(),r);status.Code(err)!=codes.PermissionDenied { t.Fatalf("foreign generation: %v",err) }
}

func TestPersonalStoragePublishRejectsWritableTargetParent(t *testing.T) {
 d,_,_,r:=publishFixture(t)
 if err:=os.Chmod(filepath.Dir(r.TargetPath),0777);err!=nil { t.Fatal(err) }
 if _,err:=d.NodePublishVolume(context.Background(),r);status.Code(err)!=codes.InvalidArgument { t.Fatalf("untrusted writable parent: %v",err) }
}

func TestPersonalStoragePublishRejectsUnsafeTargetAndForeignMount(t *testing.T) {
 d,_,m,r:=publishFixture(t)
 original:=r.TargetPath
 r.TargetPath=filepath.Join(filepath.Dir(original),"..","escape")
 if _,err:=d.NodePublishVolume(context.Background(),r);status.Code(err)!=codes.InvalidArgument { t.Fatalf("target: %v",err) }
 r.TargetPath=original
 m.mounts[original]="foreign-volume"
 if _,err:=d.NodePublishVolume(context.Background(),r);status.Code(err)!=codes.FailedPrecondition { t.Fatalf("foreign mount: %v",err) }
 if m.unmounts!=0 { t.Fatal("unmounted foreign filesystem") }
 delete(m.mounts,original)
 parent:=filepath.Dir(original)
 if err:=os.RemoveAll(parent);err!=nil { t.Fatal(err) }
 if err:=os.Symlink(t.TempDir(),parent);err!=nil { t.Fatal(err) }
 if _,err:=d.NodePublishVolume(context.Background(),r);status.Code(err)!=codes.InvalidArgument { t.Fatalf("symlink ancestry: %v",err) }
}

func TestPersonalStorageUnpublishOwnsTarget(t *testing.T) {
 d,_,m,r:=publishFixture(t)
 if _,err:=d.NodePublishVolume(context.Background(),r);err!=nil { t.Fatal(err) }
 _,err:=d.NodeUnpublishVolume(context.Background(),&csi.NodeUnpublishVolumeRequest{VolumeId:VolumeID("owner-b","binding-b","tenant-b",1),TargetPath:r.TargetPath})
 if status.Code(err)!=codes.FailedPrecondition || m.unmounts!=0 { t.Fatalf("foreign unpublish: %v",err) }
 if _,err=d.NodeUnpublishVolume(context.Background(),&csi.NodeUnpublishVolumeRequest{VolumeId:r.VolumeId,TargetPath:r.TargetPath});err!=nil { t.Fatal(err) }
 if _,err=d.NodeUnpublishVolume(context.Background(),&csi.NodeUnpublishVolumeRequest{VolumeId:r.VolumeId,TargetPath:r.TargetPath});err!=nil { t.Fatal(err) }
 if m.unmounts!=1 { t.Fatalf("unmount calls %d",m.unmounts) }
}

func TestPersonalStorageUnpublishAfterKubeletRemovedDirectory(t *testing.T) {
 d,_,_,r:=publishFixture(t)
 if err:=os.RemoveAll(filepath.Dir(r.TargetPath));err!=nil { t.Fatal(err) }
 if _,err:=d.NodeUnpublishVolume(context.Background(),&csi.NodeUnpublishVolumeRequest{VolumeId:r.VolumeId,TargetPath:r.TargetPath});err!=nil { t.Fatalf("missing target not idempotent: %v",err) }
}
func TestPersonalStorageCanceledPublishRemovesMount(t *testing.T) {
 d,_,m,r:=publishFixture(t)
 ctx,cancel:=context.WithCancel(context.Background());defer cancel();m.cancelOnMount=cancel
 if _,err:=d.NodePublishVolume(ctx,r);status.Code(err)!=codes.Canceled { t.Fatalf("canceled publish: %v",err) }
 if len(m.mounts)!=0 { t.Fatal("canceled mount survived") }
}
func TestPersonalStorageUnpublishRejectsForeignSentinel(t *testing.T) {
 d,_,m,r:=publishFixture(t)
 m.mounts[r.TargetPath]="foreign"
 if _,err:=d.NodeUnpublishVolume(context.Background(),&csi.NodeUnpublishVolumeRequest{VolumeId:"foreign",TargetPath:r.TargetPath});status.Code(err)!=codes.InvalidArgument { t.Fatalf("invalid volume handle: %v",err) }
 if m.unmounts!=0 { t.Fatal("foreign filesystem unmounted") }
}
