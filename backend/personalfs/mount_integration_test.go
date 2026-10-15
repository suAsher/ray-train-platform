package personalfs

import (
 "context"
 "encoding/json"
 "os"
 "path/filepath"
 "testing"

 "github.com/google/uuid"
 "ray-train-platform-backend/personalstorage"
)

// This fixture is created on the builder only: two independently chrooted SFTP
// accounts, a pinned host key, and private keys in a mode-0600 transient file.
// No credentials appear in test output or process arguments. Skipping this test
// is not evidence that real FUSE/SFTP isolation was verified.
func TestPersonalStorageRealFUSEIsolation(t *testing.T) {
 fixturePath:=os.Getenv("PERSONAL_STORAGE_FIXTURE_FILE")
 if fixturePath=="" { t.Skip("requires builder /dev/fuse and two isolated SFTP fixture accounts") }
 raw,err:=os.ReadFile(fixturePath);if err!=nil { t.Fatal("cannot read FUSE fixture") }
 var fixture struct { Endpoint personalstorage.Endpoint; Credentials []personalstorage.Credential }
 if json.Unmarshal(raw,&fixture)!=nil || len(fixture.Credentials)!=2 { t.Fatal("invalid two-account FUSE fixture") }
 if fixture.Endpoint.Host!="127.0.0.1" && fixture.Endpoint.Host!="localhost" { t.Fatal("FUSE fixture must be builder loopback") }
 factory,err:=personalstorage.NewSFTPFactory(fixture.Endpoint);if err!=nil { t.Fatal("invalid pinned fixture endpoint") }
 ctx,cancel:=context.WithCancel(context.Background());defer cancel()
 stores:=make([]personalstorage.Store,2)
 mounts:=make([]*MountHandle,2)
 targets:=make([]string,2)
 for i:=range stores {
  stores[i],err=factory.Open(ctx,fixture.Credentials[i]);if err!=nil { t.Fatal("fixture connection failed") }
  targets[i]=t.TempDir()
  mounts[i],err=NewMount(ctx,targets[i],"fixture-"+uuid.NewString(),stores[i]);if err!=nil { stores[i].Close();t.Fatal("fixture FUSE mount failed") }
  defer mounts[i].Close()
 }
 names:=[]string{"rt-a-"+uuid.NewString(),"rt-b-"+uuid.NewString()}
 t.Cleanup(func(){
  for i,name:=range names {
   store,err:=factory.Open(context.Background(),fixture.Credentials[i]);if err!=nil { t.Error("fixture cleanup connection failed");continue }
   for _,marker:=range []string{name+"-dir/old",name+"-dir/new",name+"-dir",name+"-link",name+"-hardlink",name} {
    if err:=store.Remove(context.Background(),marker);err!=nil && !os.IsNotExist(err) { t.Error("fixture marker cleanup failed") }
   }
   store.Close()
  }
 })
 for i:=range stores {
  if err=os.WriteFile(filepath.Join(targets[i],names[i]),[]byte("owner-private"),0600);err!=nil { t.Fatalf("write owner %d: %v",i,err) }
  if _,err=os.Stat(filepath.Join(targets[1-i],names[i]));!os.IsNotExist(err) { t.Fatalf("foreign owner file visible: %v",err) }
  entries,err:=os.ReadDir(targets[i]);if err!=nil { t.Fatal("ls failed") }
  for _,entry:=range entries { if entry.Name()==".shared" || entry.Name()==".spiking-group-shared" { t.Fatal("shared root exposed") } }
  for _,name:=range []string{".shared",".spiking-group-shared","escape"} {
   if _,err:=os.ReadDir(filepath.Join(targets[i],name));err==nil { t.Fatalf("excluded entry %q accessible",name) }
  }
  work:=filepath.Join(targets[i],names[i]+"-dir")
  if err:=os.Mkdir(work,0700);err!=nil { t.Fatal("kernel mkdir failed") }
  if err:=os.WriteFile(filepath.Join(work,"old"),[]byte("nested"),0600);err!=nil { t.Fatal("nested create failed") }
  if err:=os.Rename(filepath.Join(work,"old"),filepath.Join(work,"new"));err!=nil { t.Fatal("kernel rename failed") }
  if err:=os.Remove(filepath.Join(work,"new"));err!=nil { t.Fatal("kernel unlink failed") }
  if err:=os.Remove(work);err!=nil { t.Fatal("kernel rmdir failed") }
  if err:=os.Symlink("/",filepath.Join(targets[i],names[i]+"-link"));err==nil { t.Fatal("symlink creation allowed") }
  if err:=os.Link(filepath.Join(targets[i],names[i]),filepath.Join(targets[i],names[i]+"-hardlink"));err==nil { t.Fatal("hardlink creation allowed") }
  f,err:=stores[i].Open(ctx,names[i],os.O_RDONLY);if err!=nil { t.Fatal("write did not reach SFTP") }
  data:=make([]byte,13);n,err:=f.ReadAt(data,0);f.Close()
  if n!=13 || (err!=nil && n==0) || string(data)!="owner-private" { t.Fatal("remote bytes differ") }
 }
 if err=mounts[0].Close();err!=nil { t.Fatal("unmount failed") }
 stores[0],err=factory.Open(ctx,fixture.Credentials[0]);if err!=nil { t.Fatal("reconnect failed") }
 mounts[0],err=NewMount(ctx,targets[0],"fixture-remount-"+uuid.NewString(),stores[0]);if err!=nil { t.Fatal("remount failed") }
 defer mounts[0].Close()
 if data,err:=os.ReadFile(filepath.Join(targets[0],names[0]));err!=nil || string(data)!="owner-private" { t.Fatal("remote file lost after remount") }
 if err:=stores[1].Close();err!=nil { t.Fatal("fixture disconnect failed") }
 if _,err:=os.ReadFile(filepath.Join(targets[1],names[1]));err==nil { t.Fatal("disconnected mount served local fallback") }
}
