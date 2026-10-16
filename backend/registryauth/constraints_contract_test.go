package registryauth

import (
 "archive/tar"
 "context"
 "os"
 "path/filepath"
 "testing"
)

func writeConstraintsContractTar(t *testing.T, headers ...*tar.Header) string {
 t.Helper()
 file,err:=os.Create(filepath.Join(t.TempDir(),"layer.tar"))
 if err!=nil { t.Fatal(err) }
 writer:=tar.NewWriter(file)
 for _,header:=range headers {
  if err:=writer.WriteHeader(header);err!=nil { t.Fatal(err) }
  if header.Size>0 { if _,err:=writer.Write(make([]byte,header.Size));err!=nil { t.Fatal(err) } }
 }
 if err:=writer.Close();err!=nil { t.Fatal(err) }
 if err:=file.Close();err!=nil { t.Fatal(err) }
 return file.Name()
}
func TestEnvironmentConstraintsRequireReadOnlyRootFileAndSameLayerProfile(t *testing.T) {
 constraints:=&tar.Header{Name:"opt/raytrain/environment-constraints.txt",Typeflag:tar.TypeReg,Mode:0444,Size:65536}
 profile:=&tar.Header{Name:environmentProfilePath,Typeflag:tar.TypeReg,Mode:0444}
 for _,headers:=range [][]*tar.Header{{profile,constraints},{constraints,profile}} {
  if err:=validateEnvironmentTar(context.Background(),writeConstraintsContractTar(t,headers...));err!=nil { t.Fatalf("paired read-only bounded constraints rejected: %v",err) }
 }
 for _,scenario:=range []struct{name string;change func(*tar.Header)}{
  {"writable",func(h *tar.Header){h.Mode=0644}},
  {"executable",func(h *tar.Header){h.Mode=0555}},
  {"foreign-owner",func(h *tar.Header){h.Uid=1000}},
  {"foreign-group",func(h *tar.Header){h.Gid=1000}},
  {"too-large",func(h *tar.Header){h.Size=65537}},
  {"symlink",func(h *tar.Header){h.Typeflag=tar.TypeSymlink;h.Mode=0777;h.Size=0;h.Linkname="environment-baseline.json"}},
  {"hardlink",func(h *tar.Header){h.Typeflag=tar.TypeLink;h.Size=0;h.Linkname=environmentProfilePath}},
  {"directory",func(h *tar.Header){h.Typeflag=tar.TypeDir;h.Mode=0755;h.Size=0}},
  {"sibling",func(h *tar.Header){h.Name+=".bak"}},
 } {
  t.Run(scenario.name,func(t *testing.T){
   header:=*constraints
   scenario.change(&header)
   if err:=validateEnvironmentTar(context.Background(),writeConstraintsContractTar(t,profile,&header));err==nil { t.Fatal("unsafe constraints layer accepted") }
  })
 }
 if err:=validateEnvironmentTar(context.Background(),writeConstraintsContractTar(t,constraints));err==nil { t.Fatal("legacy layer may not replace constraints without an explicit runtime profile") }
 invalidProfile:=*profile
 invalidProfile.Mode=0644
 if err:=validateEnvironmentTar(context.Background(),writeConstraintsContractTar(t,&invalidProfile,constraints));err==nil { t.Fatal("invalid profile authorized constraints overwrite") }
 legacy:=&tar.Header{Name:environmentRoot+"/pyvenv.cfg",Typeflag:tar.TypeReg,Mode:0644}
 if err:=validateEnvironmentTar(context.Background(),writeConstraintsContractTar(t,legacy));err!=nil { t.Fatalf("legacy layer without constraints changed: %v",err) }
}
