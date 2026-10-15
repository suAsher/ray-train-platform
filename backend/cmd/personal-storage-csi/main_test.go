package main

import (
 "net"
 "os"
 "path/filepath"
 "testing"

 "github.com/container-storage-interface/spec/lib/go/csi"
 "google.golang.org/grpc"
 "ray-train-platform-backend/personalcsi"
)

func TestPersonalStorageSocketRefusesFilesAndActiveServer(t *testing.T) {
 socket:=filepath.Join(t.TempDir(),"csi.sock")
 if err:=os.WriteFile(socket,[]byte("preserve"),0600);err!=nil{t.Fatal(err)}
 if err:=prepareSocket(socket);err==nil{t.Fatal("regular file removed")}
 if raw,_:=os.ReadFile(socket);string(raw)!="preserve"{t.Fatal("foreign file changed")}
 os.Remove(socket)
 listener,err:=net.Listen("unix",socket);if err!=nil{t.Fatal(err)};defer listener.Close()
 if err:=prepareSocket(socket);err==nil{t.Fatal("active socket replaced")}
}
func TestPersonalStorageSocketRemovesOnlyStaleSocket(t *testing.T) {
 socket:=filepath.Join(t.TempDir(),"csi.sock")
 listener,err:=net.ListenUnix("unix",&net.UnixAddr{Name:socket,Net:"unix"});if err!=nil{t.Fatal(err)}
 listener.SetUnlinkOnClose(false);listener.Close()
 if err:=prepareSocket(socket);err!=nil{t.Fatal(err)}
 if _,err:=os.Lstat(socket);!os.IsNotExist(err){t.Fatal("stale socket survived")}
 if err:=os.Symlink(filepath.Join(t.TempDir(),"foreign"),socket);err!=nil{t.Fatal(err)}
 if err:=prepareSocket(socket);err==nil{t.Fatal("symlink accepted as socket")}
}
func TestPersonalStorageProbeOnlyLocalUnixCSI(t *testing.T) {
 socket:=filepath.Join(t.TempDir(),"csi.sock")
 listener,err:=net.Listen("unix",socket);if err!=nil{t.Fatal(err)}
 server:=grpc.NewServer();csi.RegisterIdentityServer(server,personalcsi.NewDriver("fixture","/not-used",nil,nil))
 go server.Serve(listener);defer server.Stop()
 if err:=checkReady(socket);err!=nil{t.Fatal(err)}
}
