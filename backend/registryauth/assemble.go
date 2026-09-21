package registryauth

import (
 "archive/tar"
 "context"
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "io"
 "os"
 "path"
 "path/filepath"
 "strings"

 "github.com/google/go-containerregistry/pkg/authn"
 "github.com/google/go-containerregistry/pkg/name"
 v1 "github.com/google/go-containerregistry/pkg/v1"
 "github.com/google/go-containerregistry/pkg/v1/empty"
 "github.com/google/go-containerregistry/pkg/v1/layout"
 "github.com/google/go-containerregistry/pkg/v1/mutate"
 "github.com/google/go-containerregistry/pkg/v1/remote"
 "github.com/google/go-containerregistry/pkg/v1/tarball"
)

const environmentRoot="opt/raytrain/environment"
const environmentWrappers="/opt/raytrain/environment-wrappers"
var environmentFiles=map[string]bool{
 "opt/raytrain/environment-baseline.json":true,
 "opt/raytrain/environment-materials/capture.json":true,
 "opt/raytrain/environment-materials/requirements.lock":true,
 "opt/raytrain/environment-materials/materials.json":true,
 "usr/local/lib/raytrain-environment/environment_runtime.py":true,
 "usr/local/bin/raytrain-environment":true,
 "opt/raytrain/environment-wrappers/ray":true,
 "opt/raytrain/environment-wrappers/torchrun":true,
 "opt/raytrain/environment-shell.sh":true,
 "etc/profile.d/raytrain-environment.sh":true,
}

type AssembleRequest struct { Base,LayerPath,LayerDigest,LayoutPath,PullConfig string }
type AssembleResult struct { ArtifactDigest string `json:"artifactDigest,omitempty"`; ErrorCode string `json:"errorCode,omitempty"` }

func environmentPathAllowed(name string,directory bool)bool{
 if name==environmentRoot || strings.HasPrefix(name,environmentRoot+"/"){return true}
 if environmentFiles[name]{return !directory}
 if directory {for file:=range environmentFiles {if strings.HasPrefix(file,name+"/"){return true}}}
 return false
}

// validateEnvironmentTar inspects archive headers and consumes contents without
// executing or extracting any member. Only the managed environment and precise
// runtime helper locations may be overlaid onto the immutable base.
func validateEnvironmentTar(ctx context.Context,filename string)error{
 info,err:=os.Lstat(filename);if err!=nil || !info.Mode().IsRegular() || info.Size()>8<<30{return ErrArtifact}
 file,err:=os.Open(filename);if err!=nil{return ErrArtifact};defer file.Close()
 archive:=tar.NewReader(contextReader{ctx:ctx,reader:file})
 entries:=map[string]byte{};links:=map[string]string{};var size int64
 for {header,err:=archive.Next();if err==io.EOF{break};if err!=nil{return ErrArtifact}
  name:=strings.TrimSuffix(header.Name,"/")
  if len(entries)>1000000 || name=="" || path.IsAbs(name) || path.Clean(name)!=name || strings.Contains(name,"\\") || strings.HasPrefix(path.Base(name),".wh."){return ErrArtifact}
  if _,exists:=entries[name];exists{return ErrArtifact}
  if !environmentPathAllowed(name,header.Typeflag==tar.TypeDir) || header.Uid!=0 || header.Gid!=0 || header.Size<0 || header.Size>8<<30{return ErrArtifact}
  size+=header.Size;if size>8<<30{return ErrArtifact}
  for key:=range header.PAXRecords {if key!="path" && key!="linkpath" && key!="mtime" && key!="atime" && key!="ctime" {return ErrArtifact}}
  switch header.Typeflag {
  case tar.TypeDir:if header.Mode!=0755 || header.Size!=0{return ErrArtifact}
  case tar.TypeReg,tar.TypeRegA:if header.Mode!=0644 && header.Mode!=0755{return ErrArtifact}
  case tar.TypeSymlink:
   if header.Mode!=0777 || header.Size!=0 || !strings.HasPrefix(name,environmentRoot+"/") || path.IsAbs(header.Linkname) || header.Linkname=="" || strings.Contains(header.Linkname,"\\"){return ErrArtifact}
   target:=path.Clean(path.Join(path.Dir(name),header.Linkname));if target!=environmentRoot && !strings.HasPrefix(target,environmentRoot+"/"){return ErrArtifact};links[name]=target
  default:return ErrArtifact
  }
  entries[name]=header.Typeflag
  if _,err:=io.Copy(io.Discard,archive);err!=nil{return ErrArtifact}
 }
 if len(entries)==0{return ErrArtifact}
 for name:=range entries {for parent:=path.Dir(name);parent!=".";parent=path.Dir(parent) {if kind,exists:=entries[parent];exists && kind!=tar.TypeDir{return ErrArtifact}}}
 // Disallow symlink chains and missing targets, keeping resolution independent
// of any filesystem aliases inherited from the base image.
 for _,target:=range links {kind,exists:=entries[target];if !exists || kind==tar.TypeSymlink{return ErrArtifact}}
 return nil
}

func pullAuthenticator(filename string)(authn.Authenticator,error){
 info,err:=os.Stat(filename);if err!=nil || !info.Mode().IsRegular() || info.Size()>1<<20{return nil,ErrCredentials}
 file,err:=os.Open(filename);if err!=nil{return nil,ErrCredentials};defer file.Close()
 var config struct { Auths map[string]authn.AuthConfig `json:"auths"` }
 if json.NewDecoder(io.LimitReader(file,1<<20)).Decode(&config)!=nil{return nil,ErrCredentials}
 for _,key:=range []string{Host,Origin} {if value,ok:=config.Auths[key];ok {return authn.FromConfig(value),nil}}
 return nil,ErrCredentials
}

func (c *Client) Assemble(ctx context.Context,request AssembleRequest)(AssembleResult,error){
 reference,err:=name.NewDigest(request.Base,name.StrictValidation)
 if err!=nil || reference.RegistryStr()!=Host || !digestPattern.MatchString(reference.DigestStr()){return AssembleResult{},ErrInvalidTarget}
 repository:=reference.RepositoryStr();project,repo,ok:=strings.Cut(repository,"/");if !ok{return AssembleResult{},ErrInvalidTarget}
 if _,err:=ValidateTarget(project,repo);err!=nil{return AssembleResult{},err}
 if err:=validateEnvironmentTar(ctx,request.LayerPath);err!=nil{return AssembleResult{},err}
 if err:=validateLayerMaterial(ctx,request.LayerPath,request.LayerDigest);err!=nil{return AssembleResult{},err}
 auth,err:=pullAuthenticator(request.PullConfig);if err!=nil{return AssembleResult{},err}
 base,err:=remote.Image(reference,remote.WithContext(ctx),remote.WithAuth(auth),remote.WithPlatform(v1.Platform{OS:"linux",Architecture:"amd64"}),remote.WithTransport(&publishTransport{base:c.http.Transport,repository:repository}))
 if err!=nil{return AssembleResult{},publishError(err)}
 image,err:=appendEnvironment(base,request.LayerPath);if err!=nil{return AssembleResult{},err}
 digest,err:=writeEnvironmentLayout(ctx,image,request.LayoutPath);if err!=nil{return AssembleResult{},err}
 return AssembleResult{ArtifactDigest:digest},nil
}

func validateLayerMaterial(ctx context.Context,filename,expected string)error{
 if !digestPattern.MatchString(expected){return ErrArtifact}
 metadataPath:=filepath.Join(filepath.Dir(filename),"build-result.json")
 info,err:=os.Lstat(metadataPath);if err!=nil || !info.Mode().IsRegular() || info.Size()>64<<10{return ErrArtifact}
 metadata,err:=os.ReadFile(metadataPath);if err!=nil{return ErrArtifact}
 var result struct{SchemaVersion int `json:"schemaVersion"`;LayerDigest string `json:"layerSha256"`;LayerSize int64 `json:"layerSizeBytes"`}
 if json.Unmarshal(metadata,&result)!=nil || result.SchemaVersion!=1 || !digestPattern.MatchString(result.LayerDigest) || result.LayerSize<0 || result.LayerSize>8<<30{return ErrArtifact}
 if expected!=result.LayerDigest{return ErrArtifact}
 file,err:=os.Open(filename);if err!=nil{return ErrArtifact};defer file.Close()
 hash:=sha256.New();size,err:=io.Copy(hash,contextReader{ctx:ctx,reader:io.LimitReader(file,(8<<30)+1)})
 if err!=nil || size!=result.LayerSize || "sha256:"+hex.EncodeToString(hash.Sum(nil))!=result.LayerDigest{return ErrArtifact};return nil
}

func appendEnvironment(base v1.Image,filename string)(v1.Image,error){
 config,err:=base.ConfigFile();if err!=nil || config.Config.User!="ray"{return nil,ErrArtifact}
 layer,err:=tarball.LayerFromFile(filename);if err!=nil{return nil,ErrArtifact}
 image,err:=mutate.AppendLayers(base,layer);if err!=nil{return nil,ErrArtifact}
 appended,err:=image.ConfigFile();if err!=nil{return nil,ErrArtifact};updated:=appended.DeepCopy()
 env:=make([]string,0,len(updated.Config.Env)+4);basePath:="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
 for _,entry:=range updated.Config.Env {key,value,_:=strings.Cut(entry,"=");switch key{case "PATH":basePath=value;case "VIRTUAL_ENV","PYTHONNOUSERSITE","BASH_ENV":default:env=append(env,entry)}}
 updated.Config.Env=append(env,"PATH="+environmentWrappers+":/"+environmentRoot+"/bin:"+basePath,"VIRTUAL_ENV=/"+environmentRoot,"PYTHONNOUSERSITE=1","BASH_ENV=/opt/raytrain/environment-shell.sh")
 result,err:=mutate.ConfigFile(image,updated);if err!=nil{return nil,ErrArtifact};return result,nil
}

func writeEnvironmentLayout(ctx context.Context,image v1.Image,directory string)(string,error){
 if ctx.Err()!=nil{return "",ErrArtifact}
 if _,err:=os.Lstat(directory);!os.IsNotExist(err){return "",ErrArtifact}
 parent:=filepath.Dir(directory);info,err:=os.Lstat(parent);if err!=nil || !info.IsDir(){return "",ErrArtifact}
 temp,err:=os.MkdirTemp(parent,".environment-oci-");if err!=nil{return "",ErrArtifact};defer os.RemoveAll(temp)
 output,err:=layout.Write(temp,empty.Index);if err!=nil{return "",ErrArtifact}
 if err:=output.AppendImage(image);err!=nil{return "",ErrArtifact}
 digest,err:=image.Digest();if err!=nil{return "",ErrArtifact}
 if err:=os.Rename(temp,directory);err!=nil{return "",ErrArtifact}
 return digest.String(),nil
}
