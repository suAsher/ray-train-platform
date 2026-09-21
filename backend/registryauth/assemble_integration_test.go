package registryauth

import (
 "archive/tar"
 "context"
 "crypto/sha256"
 "encoding/base64"
 "encoding/hex"
 "encoding/json"
 "errors"
 "io"
 "log"
 "net/http"
 "net/http/httptest"
 "os"
 "path/filepath"
 "strings"
 "sync/atomic"
 "testing"

 "github.com/google/go-containerregistry/pkg/name"
 "github.com/google/go-containerregistry/pkg/registry"
 v1 "github.com/google/go-containerregistry/pkg/v1"
 "github.com/google/go-containerregistry/pkg/v1/empty"
 "github.com/google/go-containerregistry/pkg/v1/mutate"
 "github.com/google/go-containerregistry/pkg/v1/remote"
)

func serveRegistryRequest(handler http.Handler,request *http.Request)*http.Response{
 body:=request.Body;if body==nil{body=http.NoBody}
 serverRequest:=httptest.NewRequestWithContext(request.Context(),request.Method,request.URL.String(),body)
 serverRequest.Header=request.Header.Clone();serverRequest.Host=request.Host;serverRequest.ContentLength=request.ContentLength
 serverRequest.TransferEncoding=append([]string(nil),request.TransferEncoding...)
 recorder:=httptest.NewRecorder();handler.ServeHTTP(recorder,serverRequest)
 response:=recorder.Result();response.Request=request;return response
}

func testBaseImage(t *testing.T,user,architecture string)v1.Image{
 t.Helper();config,err:=empty.Image.ConfigFile();if err!=nil{t.Fatal(err)}
 config.Architecture=architecture;config.OS="linux";config.Config.User=user;config.Config.WorkingDir="/workspace"
 config.Config.Entrypoint=[]string{"raytrain-launch"};config.Config.Env=[]string{"PATH=/base/bin","BASE_ENV=preserved","VIRTUAL_ENV=/old","PYTHONNOUSERSITE=0","BASH_ENV=/old-shell"}
 image,err:=mutate.ConfigFile(empty.Image,config);if err!=nil{t.Fatal(err)};return image
}

func preparedAssemblyRequest(t *testing.T)AssembleRequest{
 t.Helper();directory:=t.TempDir();filename:=filepath.Join(directory,"layer.tar")
 file,err:=os.Create(filename);if err!=nil{t.Fatal(err)};writer:=tar.NewWriter(file)
 // Executable payload is intentionally present. Assembly must only copy its
 // archive bytes and must never run it or extract it into the test filesystem.
 sentinel:=filepath.Join(directory,"executed")
 content:=[]byte("#!/bin/sh\ntouch "+sentinel+"\nexit 77\n")
 headers:=[]*tar.Header{{Name:environmentRoot,Typeflag:tar.TypeDir,Mode:0755},{Name:environmentRoot+"/bin",Typeflag:tar.TypeDir,Mode:0755},{Name:environmentRoot+"/bin/hostile",Typeflag:tar.TypeReg,Mode:0755,Size:int64(len(content))}}
 for _,header:=range headers{if err:=writer.WriteHeader(header);err!=nil{t.Fatal(err)};if header.Size>0{if _,err:=writer.Write(content);err!=nil{t.Fatal(err)}}}
 if err:=writer.Close();err!=nil{t.Fatal(err)};if err:=file.Close();err!=nil{t.Fatal(err)}
 bytes,err:=os.ReadFile(filename);if err!=nil{t.Fatal(err)};hash:=sha256.Sum256(bytes);digest:="sha256:"+hex.EncodeToString(hash[:])
 metadata,_:=json.Marshal(map[string]any{"schemaVersion":1,"layerSha256":digest,"layerSizeBytes":len(bytes)})
 if err:=os.WriteFile(filepath.Join(directory,"build-result.json"),metadata,0600);err!=nil{t.Fatal(err)}
 credentialsFile:=filepath.Join(directory,"pull.json")
 encoded:=base64.StdEncoding.EncodeToString([]byte("robot$fixture:fixture-only-password"))
 configuration,_:=json.Marshal(map[string]any{"auths":map[string]any{Host:map[string]string{"auth":encoded}}})
 if err:=os.WriteFile(credentialsFile,configuration,0600);err!=nil{t.Fatal(err)}
 return AssembleRequest{LayerPath:filename,LayerDigest:digest,LayoutPath:filepath.Join(directory,"oci"),PullConfig:credentialsFile}
}

func TestAssembleSelectsAMD64UsesOnlyPullAuthAndNeverExecutesLayer(t *testing.T){
 request:=preparedAssemblyRequest(t)
 handler:=registry.New(registry.Logger(log.New(io.Discard,"",0)))
 seedTransport:=roundTripFunc(func(request *http.Request)(*http.Response,error){return serveRegistryRequest(handler,request),nil})
 index:=mutate.AppendManifests(empty.Index,
  mutate.IndexAddendum{Add:testBaseImage(t,"ray","amd64"),Descriptor:v1.Descriptor{Platform:&v1.Platform{OS:"linux",Architecture:"amd64"}}},
  mutate.IndexAddendum{Add:testBaseImage(t,"root","arm64"),Descriptor:v1.Descriptor{Platform:&v1.Platform{OS:"linux",Architecture:"arm64"}}})
 reference,err:=name.NewTag(Host+"/fixtures/base:v1");if err!=nil{t.Fatal(err)}
 if err:=remote.WriteIndex(reference,index,remote.WithTransport(seedTransport));err!=nil{t.Fatal(err)}
 indexDigest,err:=index.Digest();if err!=nil{t.Fatal(err)};request.Base=Host+"/fixtures/base@"+indexDigest.String()
 var authCalls,readCalls atomic.Int64
 client:=testClient(func(request *http.Request)(*http.Response,error){
  if request.Method!=http.MethodGet && request.Method!=http.MethodHead{t.Fatalf("assembler attempted registry mutation: %s",request.Method)}
  if request.URL.Host!=Host || request.URL.Scheme!="https"{t.Fatal("registry credential left fixed origin")}
  if request.URL.Path=="/service/token"{
   username,password,ok:=request.BasicAuth();if !ok || username!="robot$fixture" || password!="fixture-only-password"{t.Fatal("pull robot credentials not used correctly")}
   if scope:=request.URL.Query().Get("scope");scope!="repository:fixtures/base:pull"{t.Fatalf("assembler asked for write scope: %q",scope)}
   authCalls.Add(1);body,_:=json.Marshal(map[string]string{"token":jwt([]string{"pull"},"fixtures/base")});response:=reply(200,string(body));response.Request=request;return response,nil
  }
  if !strings.HasPrefix(request.Header.Get("Authorization"),"Bearer "){
   response:=reply(401,"");response.Header.Set("WWW-Authenticate",`Bearer realm="https://harbor.wellspiking.ai/service/token",service="harbor-registry"`);response.Request=request;return response,nil
  }
  if _,_,ok:=request.BasicAuth();ok{t.Fatal("robot password sent directly to blob endpoint")}
  readCalls.Add(1);return serveRegistryRequest(handler,request),nil
 })
 result,err:=client.Assemble(context.Background(),request);if err!=nil{t.Fatal(err)}
 if !digestPattern.MatchString(result.ArtifactDigest) || authCalls.Load()==0 || readCalls.Load()==0{t.Fatalf("incomplete assembly: %+v",result)}
 output,err:=loadPublishImage(context.Background(),request.LayoutPath,result.ArtifactDigest);if err!=nil{t.Fatal(err)}
 config,err:=output.ConfigFile();if err!=nil{t.Fatal(err)}
 if config.Architecture!="amd64" || config.Config.User!="ray" || config.Config.WorkingDir!="/workspace"{t.Fatalf("wrong base configuration: %+v",config)}
 env:=strings.Join(config.Config.Env,"\n");if !strings.Contains(env,"BASE_ENV=preserved") || strings.Contains(env,"/old"){t.Fatal("managed environment overrides were not applied")}
 if _,err:=os.Stat(filepath.Join(filepath.Dir(request.LayerPath),"executed"));!os.IsNotExist(err){t.Fatal("layer executable was executed")}
 if _,err:=os.Stat(filepath.Join(filepath.Dir(request.LayerPath),"opt"));!os.IsNotExist(err){t.Fatal("layer was extracted into the operation filesystem")}
}

func TestAssembleRejectsUntrustedBaseAndInvalidPreparationBeforeNetwork(t *testing.T){
 client:=testClient(func(*http.Request)(*http.Response,error){t.Fatal("invalid operation reached registry");return nil,nil})
 for _,base:=range []string{"evil.invalid/team/base@sha256:"+strings.Repeat("a",64),Host+"/team/base:latest",Host+"/base@sha256:"+strings.Repeat("a",64)}{
  request:=preparedAssemblyRequest(t);request.Base=base
  if _,err:=client.Assemble(context.Background(),request);!errors.Is(err,ErrInvalidTarget){t.Fatalf("bad base accepted %q: %v",base,err)}
 }
 request:=preparedAssemblyRequest(t);request.Base=Host+"/team/base@sha256:"+strings.Repeat("a",64);request.LayerDigest="sha256:"+strings.Repeat("0",64)
 if _,err:=client.Assemble(context.Background(),request);!errors.Is(err,ErrArtifact){t.Fatalf("modified layer accepted: %v",err)}
 request=preparedAssemblyRequest(t);request.Base=Host+"/team/base@sha256:"+strings.Repeat("a",64);request.PullConfig=filepath.Join(t.TempDir(),"missing")
 if _,err:=client.Assemble(context.Background(),request);!errors.Is(err,ErrCredentials){t.Fatalf("missing robot config accepted: %v",err)}
}

func TestPullAuthenticatorOnlyAcceptsBoundedMatchingRegistryConfig(t *testing.T){
 for _,key:=range []string{Host,Origin}{
  filename:=filepath.Join(t.TempDir(),"config.json");data,_:=json.Marshal(map[string]any{"auths":map[string]any{key:map[string]string{"username":"fixture","password":"fixture-password"}}});if err:=os.WriteFile(filename,data,0600);err!=nil{t.Fatal(err)}
  auth,err:=pullAuthenticator(filename);if err!=nil{t.Fatal(err)};configuration,err:=auth.Authorization();if err!=nil || configuration.Username!="fixture" || configuration.Password!="fixture-password"{t.Fatal("matching robot config not preserved")}
 }
 for _,body:=range []string{`{`,`{"auths":{"evil.invalid":{"username":"fixture","password":"fixture-password"}}}`,strings.Repeat(" ",(1<<20)+1)}{
  filename:=filepath.Join(t.TempDir(),"config.json");if err:=os.WriteFile(filename,[]byte(body),0600);err!=nil{t.Fatal(err)}
  if _,err:=pullAuthenticator(filename);!errors.Is(err,ErrCredentials){t.Fatalf("unsafe configuration accepted: %v",err)}
 }
 if _,err:=pullAuthenticator(t.TempDir());!errors.Is(err,ErrCredentials){t.Fatalf("directory treated as credential file: %v",err)}
}
