// Package personalcsi implements a node-only CSI driver. It has no provisioning
// authority and accepts credentials only through kubelet's Unix CSI socket.
package personalcsi

import (
 "context"
 "crypto/sha256"
 "fmt"
 "os"
 "path/filepath"
 "regexp"
 "strconv"
 "strings"
 "sync"
 "syscall"

 "github.com/container-storage-interface/spec/lib/go/csi"
 "google.golang.org/grpc/codes"
 "google.golang.org/grpc/status"
 "google.golang.org/protobuf/types/known/wrapperspb"
 "ray-train-platform-backend/personalstorage"
)

const DriverName="personalstorage.wellspiking.ai"
var token=regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
var podUID=regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var dnsName=regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
func VolumeID(owner,binding,namespace string,generation int64)string {
 sum:=sha256.Sum256([]byte(owner+"\x00"+binding+"\x00"+namespace+"\x00"+strconv.FormatInt(generation,10)))
 return fmt.Sprintf("personal-%x",sum[:20])
}
type Mounted interface{Close()error}
type Mounter interface{
 Mount(context.Context,string,string,personalstorage.Store)(Mounted,error)
 Inspect(string)(string,bool,error)
 Unmount(string)error
}
type published struct { volumeID string;key [32]byte;mount Mounted;store personalstorage.Store;cancel context.CancelFunc }
type Driver struct {
 csi.UnimplementedIdentityServer
 csi.UnimplementedNodeServer
 nodeID,kubeletRoot string
 factory personalstorage.Factory
 mounter Mounter
 mu sync.Mutex
 volumes map[string]published
}
func NewDriver(nodeID,kubeletRoot string,factory personalstorage.Factory,mounter Mounter)*Driver {
 return &Driver{nodeID:nodeID,kubeletRoot:kubeletRoot,factory:factory,mounter:mounter,volumes:map[string]published{}}
}
func(d *Driver)GetPluginInfo(context.Context,*csi.GetPluginInfoRequest)(*csi.GetPluginInfoResponse,error){return &csi.GetPluginInfoResponse{Name:DriverName,VendorVersion:"0.1.0"},nil}
func(d *Driver)GetPluginCapabilities(context.Context,*csi.GetPluginCapabilitiesRequest)(*csi.GetPluginCapabilitiesResponse,error){return &csi.GetPluginCapabilitiesResponse{},nil}
func(d *Driver)Probe(context.Context,*csi.ProbeRequest)(*csi.ProbeResponse,error){return &csi.ProbeResponse{Ready:wrapperspb.Bool(true)},nil}
func(d *Driver)NodeGetInfo(context.Context,*csi.NodeGetInfoRequest)(*csi.NodeGetInfoResponse,error){return &csi.NodeGetInfoResponse{NodeId:d.nodeID},nil}
func(d *Driver)NodeGetCapabilities(context.Context,*csi.NodeGetCapabilitiesRequest)(*csi.NodeGetCapabilitiesResponse,error){return &csi.NodeGetCapabilitiesResponse{},nil}

func credential(r *csi.NodePublishVolumeRequest)(personalstorage.Credential,error) {
 a,s:=r.VolumeContext,r.Secrets
 for _,key:=range []string{"ownerID","bindingID","username","generation"} {
  if a[key]==""||a[key]!=s[key]{return personalstorage.Credential{},status.Error(codes.PermissionDenied,"personal volume credential identity mismatch")}
 }
 if !token.MatchString(a["ownerID"])||!token.MatchString(a["bindingID"])||!token.MatchString(a["username"])||len(a["namespace"])>63||!dnsName.MatchString(a["namespace"]){return personalstorage.Credential{},status.Error(codes.PermissionDenied,"invalid personal volume identity")}
 generation,err:=strconv.ParseInt(a["generation"],10,64)
 if err!=nil||generation!=1||strconv.FormatInt(generation,10)!=a["generation"]||len(s["privateKey"])==0||len(s["privateKey"])>16384{return personalstorage.Credential{},status.Error(codes.PermissionDenied,"invalid personal volume credential")}
 if r.VolumeId!=VolumeID(a["ownerID"],a["bindingID"],a["namespace"],generation){return personalstorage.Credential{},status.Error(codes.PermissionDenied,"personal volume identity mismatch")}
 for key:=range a {if key!="ownerID"&&key!="bindingID"&&key!="username"&&key!="generation"&&key!="namespace" {return personalstorage.Credential{},status.Error(codes.PermissionDenied,"unexpected personal volume attribute")}}
 return personalstorage.Credential{OwnerID:a["ownerID"],BindingID:a["bindingID"],Username:a["username"],Generation:generation,PrivateKey:[]byte(s["privateKey"])},nil
}
func(d *Driver)validateTarget(target string)error {
 root:=d.kubeletRoot
 if !filepath.IsAbs(root)||filepath.Clean(root)!=root||!filepath.IsAbs(target)||filepath.Clean(target)!=target{return status.Error(codes.InvalidArgument,"invalid kubelet publish target")}
 relative,err:=filepath.Rel(root,target);if err!=nil{return status.Error(codes.InvalidArgument,"invalid kubelet publish target")}
 parts:=strings.Split(relative,string(filepath.Separator))
 if len(parts)!=6||parts[0]!="pods"||!podUID.MatchString(parts[1])||parts[2]!="volumes"||parts[3]!="kubernetes.io~csi"||parts[5]!="mount"||len(parts[4])>253||!dnsName.MatchString(parts[4]){return status.Error(codes.InvalidArgument,"target is not a kubelet CSI publish directory")}
 current:=root
 // Parent ancestry must be owned by host root and unwritable by pod users.
 // The target itself may already be the synthetic uid1000 mounted root.
 for i:=0;i<len(parts);i++ {
  info,err:=os.Lstat(current)
  if err!=nil||!info.IsDir()||info.Mode()&os.ModeSymlink!=0||info.Mode().Perm()&0022!=0{return status.Error(codes.InvalidArgument,"untrusted kubelet target ancestry")}
  stat,ok:=info.Sys().(*syscall.Stat_t);if !ok||stat.Uid!=0{return status.Error(codes.InvalidArgument,"kubelet target ancestry must be root owned")}
  current=filepath.Join(current,parts[i])
 }
 return nil
}
func validateCapability(r *csi.NodePublishVolumeRequest)error {
 capability:=r.VolumeCapability
 if r.Readonly||capability==nil||capability.GetMount()==nil||capability.GetAccessMode().GetMode()!=csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER{return status.Error(codes.InvalidArgument,"only writable single-node filesystem volumes are supported")}
 mount:=capability.GetMount()
 if mount.FsType!=""||len(mount.MountFlags)!=0||mount.VolumeMountGroup!=""{return status.Error(codes.InvalidArgument,"personal mount options are driver controlled")}
 return nil
}
func(d *Driver)NodePublishVolume(ctx context.Context,r *csi.NodePublishVolumeRequest)(*csi.NodePublishVolumeResponse,error) {
 if r==nil{return nil,status.Error(codes.InvalidArgument,"missing publish request")}
 c,err:=credential(r);if err!=nil{return nil,err};if err=validateCapability(r);err!=nil{return nil,err};if err=d.validateTarget(r.TargetPath);err!=nil{return nil,err}
 d.mu.Lock();defer d.mu.Unlock()
 actual,mounted,err:=d.mounter.Inspect(r.TargetPath);if err!=nil{return nil,status.Error(codes.Internal,"cannot inspect personal mount")}
 key:=sha256.Sum256(c.PrivateKey)
 if existing,ok:=d.volumes[r.TargetPath];ok {
  if existing.volumeID!=r.VolumeId||existing.key!=key{return nil,status.Error(codes.PermissionDenied,"target belongs to a different personal credential")}
  if !mounted||actual!=r.VolumeId{return nil,status.Error(codes.FailedPrecondition,"personal mount identity changed")}
  return &csi.NodePublishVolumeResponse{},nil
 }
 if mounted{return nil,status.Error(codes.FailedPrecondition,"existing mount is not owned by this driver process; unpublish before remount")}
 info,err:=os.Lstat(r.TargetPath)
 if os.IsNotExist(err) {if err=os.Mkdir(r.TargetPath,0700);err!=nil{return nil,status.Error(codes.Internal,"cannot create personal publish target")};info,err=os.Lstat(r.TargetPath)}
 if err!=nil||!info.IsDir()||info.Mode()&os.ModeSymlink!=0{return nil,status.Error(codes.InvalidArgument,"publish target must be an existing real directory")}
 // The session must outlive this CSI RPC. Cancel only after unpublish.
 lifetime,cancel:=context.WithCancel(context.Background())
 stop:=context.AfterFunc(ctx,cancel)
 store,err:=d.factory.Open(lifetime,c)
 if err!=nil{stop();cancel();return nil,status.Error(codes.Unavailable,"personal storage connection failed")}
 mount,err:=d.mounter.Mount(ctx,r.TargetPath,r.VolumeId,store)
 stop()
 if err==nil&&ctx.Err()!=nil{_ = d.mounter.Unmount(r.TargetPath);if store!=nil{store.Close()};cancel();return nil,status.Error(codes.Canceled,"personal publish canceled")}
 if err!=nil{if store!=nil{store.Close()};cancel();return nil,status.Error(codes.Unavailable,"personal filesystem mount failed")}
 d.volumes[r.TargetPath]=published{volumeID:r.VolumeId,key:key,mount:mount,store:store,cancel:cancel}
 return &csi.NodePublishVolumeResponse{},nil
}
func(d *Driver)NodeUnpublishVolume(_ context.Context,r *csi.NodeUnpublishVolumeRequest)(*csi.NodeUnpublishVolumeResponse,error) {
 if r==nil||r.VolumeId==""{return nil,status.Error(codes.InvalidArgument,"missing volume identity")};if err:=d.validateTarget(r.TargetPath);err!=nil{return nil,err}
 d.mu.Lock();defer d.mu.Unlock()
 actual,mounted,err:=d.mounter.Inspect(r.TargetPath);if err!=nil{return nil,status.Error(codes.Internal,"cannot inspect personal mount")}
 existing,tracked:=d.volumes[r.TargetPath]
 if (mounted&&actual!=r.VolumeId)||(tracked&&existing.volumeID!=r.VolumeId){return nil,status.Error(codes.FailedPrecondition,"refusing to unmount a foreign volume")}
 if mounted {if err=d.mounter.Unmount(r.TargetPath);err!=nil{return nil,status.Error(codes.Internal,"personal filesystem unmount failed")}}
 if tracked {existing.cancel();if existing.store!=nil{existing.store.Close()};delete(d.volumes,r.TargetPath)}
 return &csi.NodeUnpublishVolumeResponse{},nil
}
func(d *Driver)Close()error {
 d.mu.Lock();targets:=make(map[string]string,len(d.volumes));for target,v:=range d.volumes{targets[target]=v.volumeID};d.mu.Unlock()
 var result error
 for target,id:=range targets {if _,err:=d.NodeUnpublishVolume(context.Background(),&csi.NodeUnpublishVolumeRequest{TargetPath:target,VolumeId:id});err!=nil{result=fmt.Errorf("personal mount shutdown incomplete")}}
 return result
}
