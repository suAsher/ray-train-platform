// Package personalfs exposes a personal SFTP account as a FUSE filesystem.
// It never creates local data files or follows remote links.
package personalfs

import (
 "context"
 "errors"
 "io"
 "os"
 "path"
 "sort"
 "strings"
 "sync"
 "syscall"

 "github.com/hanwen/go-fuse/v2/fs"
 "github.com/hanwen/go-fuse/v2/fuse"
 "ray-train-platform-backend/personalstorage"
)

type Node struct { fs.Inode; store personalstorage.Store; root *Node }
func NewRoot(store personalstorage.Store)*Node { n:=&Node{store:store};n.root=n;return n }
func(n *Node)relative()string { if n==n.root{return ""};return n.Path(&n.root.Inode) }
func(n *Node)child(name string)(string,syscall.Errno) {
 if name==""||name=="."||name==".."||strings.ContainsAny(name,"/\\"){return "",syscall.EACCES}
 rel,err:=personalstorage.ValidatePath(path.Join(n.relative(),name));if err!=nil{return "",syscall.EACCES};return rel,0
}
func allowed(e personalstorage.Entry)bool { return e.Mode.IsDir()||e.Mode.IsRegular() }
func mode(e personalstorage.Entry)uint32 {
 // The owner PVC supplies local identity isolation. SFTP supplies the real
 // permissions; map its permissions to all local UIDs for uid1000 and sudo.
 perm:=uint32(e.Mode.Perm());bits:=((perm>>6)|(perm>>3)|perm)&7
 perm=bits|(bits<<3)|(bits<<6)
 if e.Mode.IsDir(){return syscall.S_IFDIR|perm};return syscall.S_IFREG|perm
}
func attributes(e personalstorage.Entry,out *fuse.Attr) {
 out.Mode=mode(e);out.Size=uint64(e.Size);out.Uid=1000;out.Gid=1000;out.Nlink=1
 if e.Mode.IsDir(){out.Nlink=2}
 if !e.ModTime.IsZero(){out.Mtime=uint64(e.ModTime.Unix());out.Mtimensec=uint32(e.ModTime.Nanosecond());out.Ctime=out.Mtime;out.Ctimensec=out.Mtimensec;out.Atime=out.Mtime;out.Atimensec=out.Mtimensec}
}
func errno(err error)syscall.Errno {
 switch {
 case err==nil:return 0
 case errors.Is(err,personalstorage.ErrForbidden),os.IsPermission(err):return syscall.EACCES
 case errors.Is(err,personalstorage.ErrNotFound),os.IsNotExist(err):return syscall.ENOENT
 case errors.Is(err,personalstorage.ErrInvalid):return syscall.EINVAL
 case errors.Is(err,personalstorage.ErrTooLarge):return syscall.EFBIG
 case errors.Is(err,context.Canceled):return syscall.EINTR
 }
 var e syscall.Errno;if errors.As(err,&e){return e};return syscall.EIO
}
func(n *Node)Getattr(ctx context.Context,_ fs.FileHandle,out *fuse.AttrOut)syscall.Errno {
 e,err:=n.store.Stat(ctx,n.relative());if err!=nil{return errno(err)};if !allowed(e){return syscall.EACCES};attributes(e,&out.Attr);return 0
}
func(n *Node)Access(context.Context,uint32)syscall.Errno { return syscall.ENOSYS }
func(n *Node)Lookup(ctx context.Context,name string,out *fuse.EntryOut)(*fs.Inode,syscall.Errno) {
 rel,code:=n.child(name);if code!=0{return nil,code}
 e,err:=n.store.Stat(ctx,rel);if err!=nil{return nil,errno(err)};if !allowed(e){return nil,syscall.EACCES}
 attributes(e,&out.Attr);return n.NewInode(ctx,&Node{store:n.store,root:n.root},fs.StableAttr{Mode:mode(e)&syscall.S_IFMT}),0
}
func(n *Node)Readdir(ctx context.Context)(fs.DirStream,syscall.Errno) {
 entries,err:=n.store.List(ctx,n.relative());if err!=nil{return nil,errno(err)}
 result:=make([]fuse.DirEntry,0,len(entries))
 for _,e:=range entries { if _,code:=n.child(e.Name);code!=0||!allowed(e){continue};result=append(result,fuse.DirEntry{Name:e.Name,Mode:mode(e)}) }
 sort.Slice(result,func(i,j int)bool{return result[i].Name<result[j].Name})
 return fs.NewListDirStream(result),0
}
func(n *Node)Open(ctx context.Context,flags uint32)(fs.FileHandle,uint32,syscall.Errno) {
 f,err:=n.store.Open(ctx,n.relative(),openFlags(flags));if err!=nil{return nil,0,errno(err)}
 return &FileHandle{file:f},fuse.FOPEN_DIRECT_IO,0
}
func openFlags(flags uint32)int { return int(flags)&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_EXCL|os.O_SYNC|os.O_TRUNC) }
func(n *Node)Create(ctx context.Context,name string,flags,requestedMode uint32,out *fuse.EntryOut)(*fs.Inode,fs.FileHandle,uint32,syscall.Errno) {
 if requestedMode&syscall.S_IFMT!=0&&requestedMode&syscall.S_IFMT!=syscall.S_IFREG{return nil,nil,0,syscall.EPERM}
 rel,code:=n.child(name);if code!=0{return nil,nil,0,code}
 f,err:=n.store.Open(ctx,rel,openFlags(flags)|os.O_CREATE);if err!=nil{return nil,nil,0,errno(err)}
 info,err:=f.Stat();if err!=nil{f.Close();return nil,nil,0,errno(err)}
 attributes(personalstorage.Entry{Mode:info.Mode(),Size:info.Size(),ModTime:info.ModTime()},&out.Attr)
 node:=n.NewInode(ctx,&Node{store:n.store,root:n.root},fs.StableAttr{Mode:syscall.S_IFREG})
 return node,&FileHandle{file:f},fuse.FOPEN_DIRECT_IO,0
}
func(n *Node)Mkdir(ctx context.Context,name string,_ uint32,out *fuse.EntryOut)(*fs.Inode,syscall.Errno) {
 rel,code:=n.child(name);if code!=0{return nil,code};if err:=n.store.Mkdir(ctx,rel);err!=nil{return nil,errno(err)}
 return n.Lookup(ctx,name,out)
}
func(n *Node)remove(ctx context.Context,name string,directory bool)syscall.Errno {
 rel,code:=n.child(name);if code!=0{return code};e,err:=n.store.Stat(ctx,rel);if err!=nil{return errno(err)}
 if !allowed(e){return syscall.EACCES};if directory&&!e.Mode.IsDir(){return syscall.ENOTDIR};if !directory&&e.Mode.IsDir(){return syscall.EISDIR}
 return errno(n.store.Remove(ctx,rel))
}
func(n *Node)Unlink(ctx context.Context,name string)syscall.Errno{return n.remove(ctx,name,false)}
func(n *Node)Rmdir(ctx context.Context,name string)syscall.Errno{return n.remove(ctx,name,true)}
func(n *Node)Rename(ctx context.Context,name string,parent fs.InodeEmbedder,newName string,flags uint32)syscall.Errno {
 target,ok:=parent.(*Node);if !ok||target.root!=n.root{return syscall.EXDEV};if flags!=0{return syscall.ENOTSUP}
 old,code:=n.child(name);if code!=0{return code};new,code:=target.child(newName);if code!=0{return code}
 return errno(n.store.Rename(ctx,old,new))
}
func(n *Node)Setattr(ctx context.Context,handle fs.FileHandle,in *fuse.SetAttrIn,out *fuse.AttrOut)syscall.Errno {
 // Only file truncation is supported. Never silently accept chmod/chown/time
 // changes that the remote account did not perform.
 if in.Valid & ^uint32(fuse.FATTR_SIZE|fuse.FATTR_FH|fuse.FATTR_LOCKOWNER)!=0{return syscall.ENOTSUP}
 if size,ok:=in.GetSize();ok {
  if size>uint64(^uint64(0)>>1){return syscall.EFBIG}
  if h,ok:=handle.(*FileHandle);ok {if err:=h.file.Truncate(int64(size));err!=nil{return errno(err)}} else {
   f,err:=n.store.Open(ctx,n.relative(),os.O_WRONLY);if err!=nil{return errno(err)};err=f.Truncate(int64(size));closeErr:=f.Close();if err!=nil{return errno(err)};if closeErr!=nil{return errno(closeErr)}
  }
 }
 return n.Getattr(ctx,nil,out)
}

type FileHandle struct { file personalstorage.File; once sync.Once; closeErr error }
func(f *FileHandle)Read(_ context.Context,dest []byte,off int64)(fuse.ReadResult,syscall.Errno) {
 if off<0{return nil,syscall.EINVAL};n,err:=f.file.ReadAt(dest,off);if err!=nil&&!errors.Is(err,io.EOF){return nil,errno(err)};return fuse.ReadResultData(dest[:n]),0
}
func(f *FileHandle)Write(_ context.Context,data []byte,off int64)(uint32,syscall.Errno) {
 if off<0{return 0,syscall.EINVAL};n,err:=f.file.WriteAt(data,off);if err!=nil{return uint32(n),errno(err)};return uint32(n),0
}
// SFTP WriteAt waits for the server acknowledgement; there is no local write
// buffer to flush. Do not require the optional OpenSSH fsync extension on close.
func(f *FileHandle)Flush(context.Context)syscall.Errno{return 0}
func(f *FileHandle)Fsync(context.Context,uint32)syscall.Errno{return syscall.ENOTSUP}
func(f *FileHandle)Release(context.Context)syscall.Errno{f.once.Do(func(){f.closeErr=f.file.Close()});return errno(f.closeErr)}
