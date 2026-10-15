package personalfs

import (
 "context"
 "errors"
 "sync"
 "time"

 "github.com/hanwen/go-fuse/v2/fs"
 "github.com/hanwen/go-fuse/v2/fuse"
 "ray-train-platform-backend/personalstorage"
)

type MountHandle struct { server *fuse.Server;store personalstorage.Store;mu sync.Mutex;closed bool }
func NewMount(ctx context.Context,target,volumeID string,store personalstorage.Store)(*MountHandle,error) {
 if err:=ctx.Err();err!=nil{return nil,err}
 zero:=time.Duration(0)
 server,err:=fs.Mount(target,NewRoot(store),&fs.Options{MountOptions:fuse.MountOptions{AllowOther:true,FsName:"personalstorage:"+volumeID,Name:"wellspiking",DirectMountStrict:true,DisableXAttrs:true,MaxWrite:128*1024},EntryTimeout:&zero,AttrTimeout:&zero,NegativeTimeout:&zero,NullPermissions:true})
 if err!=nil{return nil,errors.New("personal filesystem mount failed")}
 return &MountHandle{server:server,store:store},nil
}
func(m *MountHandle)Close()error {
 m.mu.Lock();defer m.mu.Unlock();if m.closed{return nil}
 if err:=m.server.Unmount();err!=nil{return errors.New("personal filesystem unmount failed")}
 m.closed=true;return m.store.Close()
}
