package registryauth

import (
 "net/http"
 "testing"
)

func TestPublishTransportRejectsCredentialRedirectsAndWrongRepository(t *testing.T) {
 for _,destination:=range []string{"https://evil.invalid/v2/team/model/blobs/uploads/","http://harbor.wellspiking.ai/v2/","https://harbor.wellspiking.ai/v2/other/model/manifests/v1","https://harbor.wellspiking.ai/service/token?service=evil","https://harbor.wellspiking.ai/v2/team/model/../other"} {
  guard:=&publishTransport{repository:"team/model",base:roundTripFunc(func(*http.Request)(*http.Response,error){t.Fatal("unsafe request escaped");return nil,nil})}
  req,_:=http.NewRequest("GET",destination,nil)
  if _,err:=guard.RoundTrip(req);err==nil {t.Errorf("accepted %s",destination)}
 }
}

func TestPublishTransportValidatesResponseLocationsAndRealm(t *testing.T) {
 for _,tc:=range []struct{header,value string}{
  {"Location","https://evil.invalid/upload"}, {"Location","/v2/other/model/blobs/uploads/123"},
  {"WWW-Authenticate",`Bearer realm="https://evil.invalid/token",service="harbor-registry"`},
  {"WWW-Authenticate",`Bearer realm="https://harbor.wellspiking.ai/service/token",service="evil"`},
 } {
  guard:=&publishTransport{repository:"team/model",base:roundTripFunc(func(*http.Request)(*http.Response,error){res:=reply(401,"");res.Header.Set(tc.header,tc.value);return res,nil})}
  req,_:=http.NewRequest("GET",Origin+"/v2/",nil)
  if _,err:=guard.RoundTrip(req);err==nil{t.Errorf("accepted unsafe %s",tc.header)}
 }
}

func TestPublishTransportAcceptsSameRepositoryUpload(t *testing.T) {
 guard:=&publishTransport{repository:"team/model",base:roundTripFunc(func(*http.Request)(*http.Response,error){res:=reply(202,"");res.Header.Set("Location","/v2/team/model/blobs/uploads/123?_state=opaque");return res,nil})}
 req,_:=http.NewRequest("POST",Origin+"/v2/team/model/blobs/uploads/",nil)
 if _,err:=guard.RoundTrip(req);err!=nil{t.Fatal(err)}
}
