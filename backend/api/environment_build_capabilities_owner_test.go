package api

import (
 "context"
 "encoding/json"
 "errors"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"
 "github.com/gin-gonic/gin"
 "ray-train-platform-backend/auth"
 "ray-train-platform-backend/domain"
 eb "ray-train-platform-backend/environmentbuild"
)
type environmentCapsStore struct {
 *environmentAPIStore
 current *domain.DevWorkspace
 owner eb.Owner
 calls int
}
func(s *environmentCapsStore)GetWorkspace(_ context.Context,tenant,user string)(*domain.DevWorkspace,error){s.calls++;s.owner=eb.Owner{TenantID:tenant,UserID:user};return s.current,nil}
func(s *environmentCapsStore)EnvironmentWorkspace(_ context.Context,owner eb.Owner,id string)(eb.Workspace,error){return eb.Workspace{ID:id,TenantID:owner.TenantID,OwnerID:owner.UserID,Namespace:"tenant",ResourceName:"cluster",State:"RUNNING"},nil}
type environmentCapsRunner struct{ *environmentAPIRunner;image string;err error;calls int }
func(r *environmentCapsRunner)InspectWorkspace(context.Context,eb.Workspace)(eb.WorkspaceSnapshot,error){r.calls++;return eb.WorkspaceSnapshot{UID:"pod-uid",Image:r.image},r.err}
func TestEnvironmentCapabilitiesHintUsesAuthenticatedOwnerAndKeepsCapsAvailable(t *testing.T) {
 image:="harbor.wellspiking.ai/public/cu126-debug@sha256:"+strings.Repeat("5",64)
 for _,name:=range []string{"owned-profile","no-current","foreign-current","inspect-error","PAT","missing-owner","integration"} {
  t.Run(name,func(t *testing.T){
   store:=&environmentCapsStore{environmentAPIStore:&environmentAPIStore{},current:&domain.DevWorkspace{ID:"own-workspace",TenantID:"team",UserID:"stable-owner",Namespace:"tenant",RayClusterName:"cluster",State:domain.WorkspaceRunning}}
   runner:=&environmentCapsRunner{environmentAPIRunner:&environmentAPIRunner{},image:image}
   p:=auth.Principal{Subject:"stable-owner",Username:"untrusted-display-name",TenantID:"team",AuthType:auth.AuthTypeLocal,Roles:[]string{"Engineer"}}
   switch name {case "no-current":store.current=nil;case "foreign-current":store.current.UserID="victim";case "inspect-error":runner.err=errors.New("private cluster detail");case "PAT":p.AuthType=auth.AuthTypePAT;case "missing-owner":p.Subject="";case "integration":p.IntegrationID="integration"}
   primary:="harbor.wellspiking.ai/public/default-debug@sha256:"+strings.Repeat("2",64)
   base:="harbor.wellspiking.ai/public/default-base@sha256:"+strings.Repeat("1",64)
   service,err:=eb.NewService(store,runner,&environmentAPIRegistry{},&environmentAPIVault{},eb.Config{Enabled:true,BaseImage:base,WorkspaceImage:primary,WorkspaceProfiles:[]eb.WorkspaceProfile{{WorkspaceImage:image,BaseImage:"harbor.wellspiking.ai/public/cu126-base@sha256:"+strings.Repeat("6",64),PrepareImage:"harbor.wellspiking.ai/public/cu126-prepare@sha256:"+strings.Repeat("7",64)}},EncryptionKey:[]byte(strings.Repeat("k",32))})
   if err!=nil {t.Fatal(err)}
   router:=gin.New();router.Use(func(c *gin.Context){c.Set("ray-platform-principal",p);c.Next()});RegisterEnvironmentBuildRoutes(router.Group("/api/v1"),service)
   response:=httptest.NewRecorder()
   router.ServeHTTP(response,httptest.NewRequest(http.MethodGet,"/api/v1/environment-build-capabilities?workspaceId=victim&ownerId=victim&tenantId=other&baseImage=forged",nil))
   if name=="PAT" || name=="missing-owner" || name=="integration" {if response.Code!=403 || store.calls!=0 || runner.calls!=0 {t.Fatal("unauthorized owner inspected")};return}
   if response.Code!=200 || response.Header().Get("Cache-Control")!="no-store" {t.Fatalf("caps failed: %d",response.Code)}
   var envelope struct{Data map[string]any `json:"data"`};if err:=json.Unmarshal(response.Body.Bytes(),&envelope);err!=nil {t.Fatal(err)}
   if name=="owned-profile" {if envelope.Data["eligibleWorkspaceId"]!="own-workspace" || store.owner!=(eb.Owner{TenantID:"team",UserID:"stable-owner"}) {t.Fatal("hint ignored stable authenticated ownership")}} else if _,found:=envelope.Data["eligibleWorkspaceId"];found {t.Fatal("untrusted hint returned")}
   if envelope.Data["workspaceImage"]!=primary || envelope.Data["baseImage"]!=base || strings.Contains(response.Body.String(),"private cluster detail") || strings.Contains(response.Body.String(),"victim") {t.Fatal("caps changed defaults or leaked inspection data")}
  })
 }
}
