package registryauth

import (
 "archive/tar"
 "context"
 "os"
 "path/filepath"
 "testing"
)

func writeEnvironmentTar(t *testing.T,headers []*tar.Header)string{
 t.Helper();file,err:=os.Create(filepath.Join(t.TempDir(),"layer.tar"));if err!=nil{t.Fatal(err)}
 writer:=tar.NewWriter(file)
 for _,header:=range headers {if err:=writer.WriteHeader(header);err!=nil{t.Fatal(err)}}
 if err:=writer.Close();err!=nil{t.Fatal(err)};if err:=file.Close();err!=nil{t.Fatal(err)};return file.Name()
}

func TestEnvironmentTarRejectsEscapeAndPrivilegedEntries(t *testing.T){
 for _,header:=range []*tar.Header{
  {Name:"../../etc/shadow",Typeflag:tar.TypeReg,Mode:0644},
  {Name:"/opt/raytrain/environment/config",Typeflag:tar.TypeReg,Mode:0644},
  {Name:"opt/raytrain/environment/../escape",Typeflag:tar.TypeReg,Mode:0644},
  {Name:"etc/ld.so.preload",Typeflag:tar.TypeReg,Mode:0644},
  {Name:"opt/raytrain/environment/setuid",Typeflag:tar.TypeReg,Mode:04755},
  {Name:"opt/raytrain/environment/device",Typeflag:tar.TypeChar,Mode:0644},
  {Name:"opt/raytrain/environment/link",Typeflag:tar.TypeLink,Linkname:"etc/shadow",Mode:0644},
  {Name:"opt/raytrain/environment/link",Typeflag:tar.TypeSymlink,Linkname:"../../etc",Mode:0777},
  {Name:"opt/raytrain/environment/.wh.bin",Typeflag:tar.TypeReg,Mode:0644},
 } {t.Run(header.Name,func(t *testing.T){header.Uid=1000;header.Gid=1000;if validateEnvironmentTar(context.Background(),writeEnvironmentTar(t,[]*tar.Header{header}))==nil{t.Fatal("unsafe layer accepted")}})}
}

func TestEnvironmentTarAcceptsInternalVenvSymlinkAndRejectsWritesThroughIt(t *testing.T){
 valid:=[]*tar.Header{
  {Name:"opt/raytrain/environment",Typeflag:tar.TypeDir,Mode:0755,Uid:1000,Gid:1000},
  {Name:"opt/raytrain/environment/lib",Typeflag:tar.TypeDir,Mode:0755,Uid:1000,Gid:1000},
  {Name:"opt/raytrain/environment/lib64",Typeflag:tar.TypeSymlink,Linkname:"lib",Mode:0777,Uid:1000,Gid:1000},
 }
 if err:=validateEnvironmentTar(context.Background(),writeEnvironmentTar(t,valid));err!=nil{t.Fatal(err)}
 invalid:=append(append([]*tar.Header{},valid...),&tar.Header{Name:"opt/raytrain/environment/lib64/write",Typeflag:tar.TypeReg,Mode:0644,Uid:1000,Gid:1000})
 if validateEnvironmentTar(context.Background(),writeEnvironmentTar(t,invalid))==nil{t.Fatal("write through symlink accepted")}
}
