package personalcsi

import (
 "bufio"
 "context"
 "errors"
 "os"
 "strings"
 "sync"
 "syscall"

 "ray-train-platform-backend/personalfs"
 "ray-train-platform-backend/personalstorage"
)

// FuseMounter identifies mounts from kernel metadata, never local marker files.
type FuseMounter struct {mu sync.Mutex;mounts map[string]*personalfs.MountHandle}
func NewFuseMounter()*FuseMounter{return &FuseMounter{mounts:map[string]*personalfs.MountHandle{}}}
func(m *FuseMounter)Mount(ctx context.Context,target,id string,store personalstorage.Store)(Mounted,error) {
 handle,err:=personalfs.NewMount(ctx,target,id,store);if err!=nil{return nil,err}
 m.mu.Lock();m.mounts[target]=handle;m.mu.Unlock();return handle,nil
}
func unescapeMount(s string)string{return strings.NewReplacer(`\040`," ",`\011`,"\t",`\012`,"\n",`\134`,"\\").Replace(s)}
func(m *FuseMounter)Inspect(target string)(string,bool,error) {
 file,err:=os.Open("/proc/self/mountinfo");if err!=nil{return "",false,err};defer file.Close()
 scanner:=bufio.NewScanner(file);scanner.Buffer(make([]byte,4096),1024*1024)
 for scanner.Scan() {
  fields:=strings.Fields(scanner.Text());if len(fields)<10||unescapeMount(fields[4])!=target{continue}
  separator:=-1;for i,field:=range fields {if field=="-"{separator=i;break}}
  if separator<0||len(fields)<separator+4{return "",true,errors.New("malformed mount metadata")}
  if fields[separator+1]!="fuse.wellspiking"||!strings.HasPrefix(fields[separator+2],"personalstorage:personal-"){return "foreign",true,nil}
  id:=strings.TrimPrefix(fields[separator+2],"personalstorage:")
  if len(id)!=49{return "foreign",true,nil}
  return id,true,nil
 }
 return "",false,scanner.Err()
}
func(m *FuseMounter)Unmount(target string)error {
 m.mu.Lock();handle:=m.mounts[target];m.mu.Unlock()
 if handle!=nil {
  if err:=handle.Close();err!=nil{return err}
  m.mu.Lock();delete(m.mounts,target);m.mu.Unlock();return nil
 }
 // Restart cleanup is permitted only after Driver validated the exact kernel
 // filesystem source and root-owned target ancestry for this volume.
 return syscall.Unmount(target,0)
}
