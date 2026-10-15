package personalcsi

import (
 "context"
 "os"
 "path/filepath"
 "strconv"
 "testing"

 "github.com/container-storage-interface/spec/lib/go/csi"
 "ray-train-platform-backend/personalstorage"
 "ray-train-platform-backend/personalstorage/testfixture"
)

func TestPersonalStorageRealNodeMountLifecycle(t *testing.T) {
 if os.Getenv("PERSONAL_STORAGE_FUSE_TEST")!="1"{t.Skip("requires explicit builder FUSE integration gate")}
 fixture,err:=testfixture.Start();if err!=nil{t.Fatal("cannot start isolated SFTP fixture")};defer fixture.Close()
 factory,err:=personalstorage.NewSFTPFactory(fixture.Endpoint);if err!=nil{t.Fatal("invalid fixture host pin")}
 driver,_,_,request:=publishFixture(t);mounter:=NewFuseMounter();driver.factory=factory;driver.mounter=mounter
 credential:=fixture.Credentials[0]
 request.VolumeId=VolumeID(credential.OwnerID,credential.BindingID,"fixture",credential.Generation)
 request.VolumeContext=map[string]string{"ownerID":credential.OwnerID,"bindingID":credential.BindingID,"username":credential.Username,"generation":strconv.FormatInt(credential.Generation,10),"namespace":"fixture"}
 request.Secrets=map[string]string{"ownerID":credential.OwnerID,"bindingID":credential.BindingID,"username":credential.Username,"generation":strconv.FormatInt(credential.Generation,10),"privateKey":string(credential.PrivateKey)}
 rpc,cancel:=context.WithCancel(context.Background());defer cancel()
 if _,err:=driver.NodePublishVolume(rpc,request);err!=nil{t.Fatal(err)};defer driver.Close()
 actual,mounted,err:=mounter.Inspect(request.TargetPath);if err!=nil||!mounted||actual!=request.VolumeId{t.Fatal("kernel mount identity does not match published volume")}
 cancel()
 filename:=filepath.Join(request.TargetPath,"own-file")
 if err:=os.WriteFile(filename,[]byte("own"),0600);err!=nil{t.Fatalf("session died with CSI RPC: %v",err)}
 if data,err:=os.ReadFile(filename);err!=nil||string(data)!="own"{t.Fatal("mounted bytes differ")}
 if _,err:=driver.NodeUnpublishVolume(context.Background(),&csi.NodeUnpublishVolumeRequest{VolumeId:request.VolumeId,TargetPath:request.TargetPath});err!=nil{t.Fatal(err)}
 if _,mounted,err:=mounter.Inspect(request.TargetPath);err!=nil||mounted{t.Fatal("published filesystem survived unpublish")}
 if err:=driver.Close();err!=nil{t.Fatal("repeated shutdown failed")}
}
