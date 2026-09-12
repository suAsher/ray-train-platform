package auth

import (
 "net/http"
 "regexp"
 "strconv"
 "strings"
 "github.com/gin-gonic/gin"
 "ray-train-platform-backend/httpapi"
)
var integrationPathID=regexp.MustCompile(`^[0-9a-f]{32}$`)
// IntegrationRouteGuard is deliberately closed: a new platform API is denied
// until it has an explicit integration authorization contract and is added here.
func IntegrationRouteGuard() gin.HandlerFunc {
 return func(c *gin.Context){
  p,ok:=PrincipalFromGin(c);if !ok||p.IntegrationID==""{c.Next();return}
  if integrationRouteAllowed(c.Request.Method,c.Request.URL.Path){c.Next();return}
  c.Header("Cache-Control","no-store")
  c.AbortWithStatusJSON(http.StatusForbidden,httpapi.Failure[any](httpapi.RequestID(c.GetHeader("X-Request-ID")),"INTEGRATION_ROUTE_FORBIDDEN","integration credentials are restricted to authorized MLflow resources"))
 }
}
func integrationRouteAllowed(method,path string)bool{
 if method==http.MethodGet&&path=="/api/v1/me"{return true}
 const sdk="/api/v1/mlflow-tracking/api/2.0/mlflow/runs/"
 if strings.HasPrefix(path,sdk){suffix:=strings.TrimPrefix(path,sdk);if method==http.MethodGet{return suffix=="get"};if method==http.MethodPost{switch suffix{case "log-batch","log-metric","log-parameter","set-tag","update":return true}};return false}
 const base="/api/v1/mlflow/"
 if !strings.HasPrefix(path,base){return false};parts:=strings.Split(strings.TrimPrefix(path,base),"/")
 if len(parts)==1{return (parts[0]=="capabilities"&&method==http.MethodGet)||(parts[0]=="experiments"&&(method==http.MethodGet||method==http.MethodPost))}
 if len(parts)==3&&parts[0]=="experiments"&&integrationPathID.MatchString(parts[1])&&parts[2]=="runs"{return method==http.MethodGet||method==http.MethodPost}
 if parts[0]!="runs"||!integrationPathID.MatchString(parts[1]){return false}
 if len(parts)==2{return method==http.MethodGet}
 if len(parts)==3{switch parts[2]{case "log-batch","finish":return method==http.MethodPost;case "artifacts":return method==http.MethodGet||method==http.MethodPost};return false}
 if parts[2]!="artifacts"||!integrationPathID.MatchString(parts[3]){return false}
 if len(parts)==4{return method==http.MethodGet||method==http.MethodDelete}
 if len(parts)==5{return (parts[4]=="complete"&&method==http.MethodPost)||(parts[4]=="content"&&method==http.MethodGet)}
 if len(parts)==6&&parts[4]=="parts"&&method==http.MethodPut{n,err:=strconv.Atoi(parts[5]);return err==nil&&n>=1&&n<=2560&&strconv.Itoa(n)==parts[5]}
 return false
}
