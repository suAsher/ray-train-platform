package api

import (
 "context"
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"

 "github.com/gin-gonic/gin"
 "ray-train-platform-backend/auth"
 "ray-train-platform-backend/domain"
 "ray-train-platform-backend/modellifecycle"
)

type modelStoreFake struct {
 modellifecycle.Repository
 model modellifecycle.Model
 updated bool
}
func (s *modelStoreFake) GetModel(context.Context,string)(modellifecycle.Model,error){return s.model,nil}
func (s *modelStoreFake) ListModels(context.Context,modellifecycle.Filter)(modellifecycle.ModelPage,error){return modellifecycle.ModelPage{Items:[]modellifecycle.Model{s.model}},nil}
func (s *modelStoreFake) UpdateModel(_ context.Context,_ string,_ modellifecycle.ModelUpdate,_ modellifecycle.Actor)(modellifecycle.Model,error){s.updated=true;return s.model,nil}

func modelRouter(h *Handler,p auth.Principal)*gin.Engine{
 gin.SetMode(gin.TestMode)
 r:=gin.New()
 r.Use(func(c *gin.Context){c.Set("ray-platform-principal",p);c.Next()})
 h.RegisterModelReadRoutes(r.Group("/api/v1"))
 write:=r.Group("/api/v1")
 write.Use(auth.RequireInteractiveSession(false))
 h.RegisterModelManagementRoutes(write)
 return r
}
func TestSharedModelReadCrossTenantDoesNotGrantManagement(t *testing.T){
 store:=&modelStoreFake{model:modellifecycle.Model{ID:"model-1",OwnerID:"alice",OwnerName:"alice",TenantID:"another-team",Name:"Shared model"}}
 h:=NewHandler(&fakeJobRepository{},Options{Models:store})
 r:=modelRouter(h,auth.Principal{Subject:"bob",TenantID:"local",AuthType:auth.AuthTypeLocal})
 w:=httptest.NewRecorder();r.ServeHTTP(w,httptest.NewRequest("GET","/api/v1/models",nil))
 if w.Code!=200 {t.Fatalf("%d %s",w.Code,w.Body.String())}
 var response struct{Data struct{Items []struct{CanManage bool `json:"canManage"`;Name string `json:"name"`}}}
 if err:=json.Unmarshal(w.Body.Bytes(),&response);err!=nil{t.Fatal(err)}
 if len(response.Data.Items)!=1 || response.Data.Items[0].CanManage {t.Fatalf("unexpected shared response: %s",w.Body.String())}
 request:=httptest.NewRequest("PATCH","/api/v1/models/model-1",strings.NewReader(`{"description":"stolen","revision":1}`));request.Header.Set("Content-Type","application/json")
 w=httptest.NewRecorder();r.ServeHTTP(w,request)
 if w.Code!=403||store.updated{t.Fatalf("foreign mutation accepted: %d",w.Code)}
}
func TestSharedModelManagementRequiresInteractiveSession(t *testing.T){
 store:=&modelStoreFake{model:modellifecycle.Model{ID:"model-1",OwnerID:"alice"}}
 h:=NewHandler(&fakeJobRepository{},Options{Models:store})
 r:=modelRouter(h,auth.Principal{Subject:"alice",TenantID:"local",AuthType:auth.AuthTypePAT,Scopes:[]string{domain.PATScopeJobsRead,domain.PATScopeJobsWrite}})
 request:=httptest.NewRequest("PATCH","/api/v1/models/model-1",strings.NewReader(`{"description":"new","revision":1}`));request.Header.Set("Content-Type","application/json")
 w:=httptest.NewRecorder();r.ServeHTTP(w,request)
 if w.Code!=http.StatusForbidden||store.updated{t.Fatalf("PAT mutation accepted %d",w.Code)}
}
func TestModelUpdateRejectsUnknownAndOversizedBody(t *testing.T){
 for _,body:=range []string{`{"ownerId":"other","revision":1}`,`{"description":"`+strings.Repeat("x",40000)+`","revision":1}`} {
  store:=&modelStoreFake{model:modellifecycle.Model{ID:"model-1",OwnerID:"alice"}}
  h:=NewHandler(&fakeJobRepository{},Options{Models:store})
  r:=modelRouter(h,auth.Principal{Subject:"alice",TenantID:"local",AuthType:auth.AuthTypeLocal})
  req:=httptest.NewRequest("PATCH","/api/v1/models/model-1",strings.NewReader(body));req.Header.Set("Content-Type","application/json")
  w:=httptest.NewRecorder();r.ServeHTTP(w,req)
  if w.Code!=400&&w.Code!=413{t.Fatalf("invalid body accepted %d %s",w.Code,w.Body.String())}
  if store.updated {t.Fatal("invalid body reached storage")}
 }
}
