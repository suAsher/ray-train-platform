package registryauth

import (
 "net/http"
 "net/url"
 "path"
 "strings"
)

// publishTransport constrains the registry library's auth exchanges, upload
// locations and redirects to the frozen Harbor repository. In particular no
// cross-registry layer mounting or externally advertised token realm is allowed.
type publishTransport struct { base http.RoundTripper; repository string }

func (t *publishTransport) allowed(u *url.URL) bool {
 if u==nil || u.Scheme!="https" || u.Host!=Host || u.User!=nil || u.Fragment!="" || u.RawPath!="" {return false}
 if path.Clean(u.Path)!=strings.TrimSuffix(u.Path,"/") {return false}
 if u.Path=="/service/token" {
  q:=u.Query();if len(q["service"])!=1 || q.Get("service")!=registryService {return false}
  for _,scope:=range q["scope"] {if scope!="repository:"+t.repository+":pull,push" && scope!="repository:"+t.repository+":push,pull" && scope!="repository:"+t.repository+":pull" {return false}}
  return true
 }
 if u.Path=="/v2/" {return true}
 prefix:="/v2/"+t.repository+"/"
 if !strings.HasPrefix(u.Path,prefix){return false}
 suffix:=strings.TrimPrefix(u.Path,prefix)
 if !strings.HasPrefix(suffix,"blobs/") && !strings.HasPrefix(suffix,"manifests/"){return false}
 return u.Query().Get("from")=="" && u.Query().Get("mount")==""
}

func validChallenge(value string) bool {
 if value=="" {return true}
 scheme,parameters,ok:=strings.Cut(value," ");if !ok || !strings.EqualFold(scheme,"Bearer"){return false}
 values:=map[string]string{}
 for parameters!="" {
  key,rest,ok:=strings.Cut(strings.TrimSpace(parameters),"=");if !ok {return false}
  rest=strings.TrimSpace(rest);if !strings.HasPrefix(rest,`"`){return false}
  end:=strings.Index(rest[1:],`"`);if end<0{return false}
  value:=rest[1:end+1];if strings.Contains(value,`\`){return false}
  key=strings.TrimSpace(key);if _,exists:=values[key];exists{return false};values[key]=value
  parameters=strings.TrimSpace(rest[end+2:])
  if parameters!="" {if parameters[0]!=','{return false};parameters=strings.TrimSpace(parameters[1:]);if parameters==""{return false}}
 }
 return values["realm"]==Origin+"/service/token" && values["service"]==registryService
}

func (t *publishTransport) RoundTrip(request *http.Request) (*http.Response,error) {
 if !t.allowed(request.URL) || (request.Host!="" && request.Host!=Host){return nil,ErrUnavailable}
 response,err:=t.base.RoundTrip(request);if err!=nil{return nil,ErrUnavailable}
 valid:=validChallenge(response.Header.Get("WWW-Authenticate"))
 if location:=response.Header.Get("Location");location!="" {
  relative,err:=url.Parse(location)
  valid=valid && err==nil && t.allowed(request.URL.ResolveReference(relative))
 }
 if !valid {response.Body.Close();return nil,ErrUnavailable}
 return response,nil
}
