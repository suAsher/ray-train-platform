package registryauth

import (
 "archive/tar"
 "bytes"
 "context"
 "encoding/json"
 "io"
 "log"
 "net"
 "net/http"
 "net/http/httptest"
 "sync/atomic"
 "testing"
 "time"

 "github.com/google/go-containerregistry/pkg/registry"
 "github.com/google/go-containerregistry/pkg/v1/empty"
 "github.com/google/go-containerregistry/pkg/v1/layout"
 "github.com/google/go-containerregistry/pkg/v1/mutate"
 "github.com/google/go-containerregistry/pkg/v1/static"
 "github.com/google/go-containerregistry/pkg/v1/types"
)

// The caller's short identity-query header timeout must not also bound a
// registry's acknowledgement after it receives a layer upload.
func TestPublishSeparatesUploadConfirmationFromIdentityTimeout(t *testing.T) {
 directory,digest,layerSize:=nonEmptyTimeoutLayout(t)
 var layerPatches atomic.Int64
 handler:=registry.New(registry.Logger(log.New(io.Discard,"",0)))
 server:=httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  if r.URL.Path=="/service/token" {
   w.Header().Set("Content-Type","application/json")
   _=json.NewEncoder(w).Encode(map[string]string{"token":jwt([]string{"pull","push"},"team/model")})
   return
  }
  if r.Method==http.MethodPatch && r.ContentLength==layerSize {layerPatches.Add(1);time.Sleep(200*time.Millisecond)}
  handler.ServeHTTP(w,r)
 }))
 defer server.Close()
 client:=NewClient()
 base:=server.Client().Transport.(*http.Transport).Clone()
 // Route only this in-process TLS fixture; production origin validation still
 // sees the frozen Harbor host, and production TLS settings are unchanged.
 base.TLSClientConfig=base.TLSClientConfig.Clone()
 base.TLSClientConfig.ServerName="127.0.0.1"
 base.TLSClientConfig.InsecureSkipVerify=false
 base.Proxy=nil
 base.DialContext=func(ctx context.Context,network,address string)(net.Conn,error){return (&net.Dialer{}).DialContext(ctx,network,server.Listener.Addr().String())}
 base.ResponseHeaderTimeout=50*time.Millisecond
 client.http.Transport=base
 defer base.CloseIdleConnections()
 ctx,cancel:=context.WithTimeout(context.Background(),4*time.Second)
 defer cancel()
 result,err:=client.Publish(ctx,credentials,PublishRequest{LayoutPath:directory,Digest:digest,Project:"team",Repository:"model",Tag:"delayed"})
 if err!=nil || result.ImageDigest!=digest {t.Fatalf("delayed registry confirmation failed: %+v %v layerPatches=%d",result,err,layerPatches.Load())}
 if layerPatches.Load()<1 {t.Fatal("non-empty layer PATCH was not exercised")}
 if base.ResponseHeaderTimeout!=50*time.Millisecond || client.http.Timeout!=20*time.Second {t.Fatal("publishing changed identity-query timeout")}
}

func nonEmptyTimeoutLayout(t *testing.T) (string,string,int64) {
 t.Helper()
 var archive bytes.Buffer
 writer:=tar.NewWriter(&archive)
 contents:=bytes.Repeat([]byte("layer-confirmation-fixture"),2048)
 if err:=writer.WriteHeader(&tar.Header{Name:"fixture.txt",Mode:0644,Size:int64(len(contents))});err!=nil{t.Fatal(err)}
 if _,err:=writer.Write(contents);err!=nil{t.Fatal(err)}
 if err:=writer.Close();err!=nil{t.Fatal(err)}
 image,err:=mutate.AppendLayers(empty.Image,static.NewLayer(archive.Bytes(),types.OCIUncompressedLayer));if err!=nil{t.Fatal(err)}
 directory:=t.TempDir()
 output,err:=layout.Write(directory,empty.Index);if err!=nil{t.Fatal(err)}
 if err:=output.AppendImage(image);err!=nil{t.Fatal(err)}
 digest,err:=image.Digest();if err!=nil{t.Fatal(err)}
 return directory,digest.String(),int64(archive.Len())
}
