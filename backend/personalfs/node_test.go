package personalfs

import (
 "context"
 "os"
 "path/filepath"
 "syscall"
 "testing"

 "github.com/hanwen/go-fuse/v2/fs"
 "github.com/hanwen/go-fuse/v2/fuse"
 "ray-train-platform-backend/personalstorage"
)

type localStore struct { dir string }
func (s *localStore) List(_ context.Context, rel string)([]personalstorage.Entry,error) {
 entries,err:=os.ReadDir(filepath.Join(s.dir,rel)); if err!=nil { return nil,err }
 result:=make([]personalstorage.Entry,0,len(entries))
 for _,entry:=range entries { info,err:=entry.Info();if err!=nil { return nil,err };result=append(result,entryFromInfo(info)) }
 return result,nil
}
func entryFromInfo(info os.FileInfo) personalstorage.Entry { return personalstorage.Entry{Name:info.Name(),Size:info.Size(),Mode:info.Mode(),ModTime:info.ModTime()} }
func (s *localStore) Stat(_ context.Context,rel string)(personalstorage.Entry,error) { info,err:=os.Lstat(filepath.Join(s.dir,rel));if err!=nil{return personalstorage.Entry{},err};return entryFromInfo(info),nil }
func (s *localStore) Open(_ context.Context,rel string,flags int)(personalstorage.File,error) { return os.OpenFile(filepath.Join(s.dir,rel),flags,0600) }
func (s *localStore) Mkdir(_ context.Context,rel string)error { return os.Mkdir(filepath.Join(s.dir,rel),0700) }
func (s *localStore) Remove(_ context.Context,rel string)error { return os.Remove(filepath.Join(s.dir,rel)) }
func (s *localStore) Rename(_ context.Context,old,new string)error { return os.Rename(filepath.Join(s.dir,old),filepath.Join(s.dir,new)) }
func (s *localStore) Close()error { return nil }

func TestPersonalStorageFSReadWriteRenameAndRemove(t *testing.T) {
 ctx:=context.Background()
 store:=&localStore{dir:t.TempDir()}
 root:=NewRoot(store)
 fs.NewNodeFS(root,&fs.Options{})
 directory,errno:=root.Mkdir(ctx,"work",0700,&fuse.EntryOut{})
 if errno!=0 { t.Fatal(errno) }
 root.AddChild("work",directory,false)
 work:=directory.Operations().(*Node)
 _,handle,_,errno:=work.Create(ctx,"a.txt",uint32(os.O_RDWR|os.O_EXCL),0600,&fuse.EntryOut{})
 if errno!=0 { t.Fatal(errno) }
 file:=handle.(fs.FileWriter)
 if n,errno:=file.Write(ctx,[]byte("private"),0);errno!=0 || n!=7 { t.Fatalf("write %d %v",n,errno) }
 if errno:=handle.(fs.FileFlusher).Flush(ctx);errno!=0 { t.Fatal(errno) }
 result,errno:=handle.(fs.FileReader).Read(ctx,make([]byte,16),0)
 if errno!=0 { t.Fatal(errno) }
 data,_:=result.Bytes(make([]byte,16));result.Done()
 if string(data)!="private" { t.Fatalf("read %q",data) }
 if errno:=handle.(fs.FileReleaser).Release(ctx);errno!=0 { t.Fatal(errno) }
 if errno:=work.Rename(ctx,"a.txt",root,"b.txt",0);errno!=0 { t.Fatal(errno) }
 if _,err:=os.Stat(filepath.Join(store.dir,"b.txt"));err!=nil { t.Fatal(err) }
 if errno:=root.Unlink(ctx,"b.txt");errno!=0 { t.Fatal(errno) }
 if errno:=root.Rmdir(ctx,"work");errno!=0 { t.Fatal(errno) }
}

type failingStore struct { personalstorage.Store; err error }
func (s failingStore) List(context.Context,string)([]personalstorage.Entry,error) {
 if s.err!=nil { return nil,s.err }
 return []personalstorage.Entry{{Name:"mine",Mode:0600},{Name:".shared",Mode:os.ModeDir|0700},{Name:"escape",Mode:os.ModeSymlink|0777},{Name:".spiking-group-shared",Mode:os.ModeDir|0700}},nil
}
func (s failingStore) Stat(context.Context,string)(personalstorage.Entry,error) { return personalstorage.Entry{},os.ErrPermission }

func TestPersonalStorageFSHidesSharedAndLinks(t *testing.T) {
 root:=NewRoot(failingStore{})
 fs.NewNodeFS(root,&fs.Options{})
 stream,errno:=root.Readdir(context.Background())
 if errno!=0 { t.Fatal(errno) }
 defer stream.Close()
 if !stream.HasNext() { t.Fatal("missing own file") }
 entry,errno:=stream.Next()
 if errno!=0 || entry.Name!="mine" || stream.HasNext() { t.Fatalf("leaked excluded entry: %+v %v",entry,errno) }
 for _,name:=range []string{".shared",".spiking-group-shared","..","a/b"} {
  if _,errno:=root.Lookup(context.Background(),name,&fuse.EntryOut{});errno!=syscall.EACCES { t.Fatalf("lookup %q: %v",name,errno) }
 }
}
func TestPersonalStorageFSPropagatesRemoteFailures(t *testing.T) {
 root:=NewRoot(failingStore{err:os.ErrPermission})
 if _,errno:=root.Readdir(context.Background());errno!=syscall.EACCES { t.Fatalf("list: %v",errno) }
 if errno:=root.Getattr(context.Background(),nil,&fuse.AttrOut{});errno!=syscall.EACCES { t.Fatalf("getattr: %v",errno) }
}
