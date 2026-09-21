package registryauth

import (
 "context"
 "encoding/json"
 "io"
 "log"
 "net"
 "net/http"
 "net/http/httptest"
 "testing"
 "time"

 "github.com/google/go-containerregistry/pkg/registry"
)

// The caller's short identity-query header timeout must not also bound a
// registry's acknowledgement after it receives a layer upload.
func TestPublishSeparatesUploadConfirmationFromIdentityTimeout(t *testing.T) {
 handler:=registry.New(registry.Logger(log.New(io.Discard,"",0)))
 server:=httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  if r.URL.Path=="/service/token" {
   w.Header().Set("Content-Type","application/json")
   _=json.NewEncoder(w).Encode(map[string]string{"token":jwt([]string{"pull","push"},"team/model")})
   return
  }
  if r.Method==http.MethodPatch {time.Sleep(200*time.Millisecond)}
  handler.ServeHTTP(w,r)
 }))
 defer server.Close()
 client:=NewClient()
 base:=server.Client().Transport.(*http.Transport).Clone()
 // Route only this in-process TLS fixture; production origin validation still
 // sees the frozen Harbor host, and production TLS settings are unchanged.
 base.Proxy=nil
 base.DialContext=func(ctx context.Context,network,address string)(net.Conn,error){return (&net.Dialer{}).DialContext(ctx,network,server.Listener.Addr().String())}
 base.ResponseHeaderTimeout=50*time.Millisecond
 client.http.Transport=base
 defer base.CloseIdleConnections()
 directory,digest:=testLayout(t)
 ctx,cancel:=context.WithTimeout(context.Background(),4*time.Second)
 defer cancel()
 result,err:=client.Publish(ctx,credentials,PublishRequest{LayoutPath:directory,Digest:digest,Project:"team",Repository:"model",Tag:"delayed"})
 if err!=nil || result.ImageDigest!=digest {t.Fatalf("delayed registry confirmation failed: %+v %v",result,err)}
 if base.ResponseHeaderTimeout!=50*time.Millisecond || client.http.Timeout!=20*time.Second {t.Fatal("publishing changed identity-query timeout")}
}
