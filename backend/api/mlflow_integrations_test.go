package api

import (
 "context"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"
 "github.com/gin-gonic/gin"
 "ray-train-platform-backend/auth"
 "ray-train-platform-backend/integrations"
)
type integrationManagementFake struct{integrations.ManagementStore;creates int}
func(s *integrationManagementFake)Create(_ context.Context,_ auth.Principal,_ integrations.Identity)error{s.creates++;return nil}
func TestIntegrationManagementRequiresInteractiveLoginAndBoundedStrictJSON(t *testing.T){
 for _,tc:=range []struct{name string;principal auth.Principal;body string;want int}{
  {"PAT cannot mint",auth.Principal{Subject:"person",TenantID:"team",AuthType:auth.AuthTypePAT},`{"name":"test"}`,403},
  {"no demo",auth.DemoPrincipal(),`{"name":"test"}`,403},
  {"unknown field",auth.Principal{Subject:"person",TenantID:"team",AuthType:auth.AuthTypeLocal},`{"name":"test","tenantId":"other"}`,400},
  {"oversized body",auth.Principal{Subject:"person",TenantID:"team",AuthType:auth.AuthTypeLocal},`{"name":"`+strings.Repeat("x",17000)+`"}`,400},
  {"trailing JSON",auth.Principal{Subject:"person",TenantID:"team",AuthType:auth.AuthTypeLocal},`{"name":"test"}{}`,400},
  {"empty name",auth.Principal{Subject:"person",TenantID:"team",AuthType:auth.AuthTypeLocal},`{"name":"  "}`,400},
  {"valid",auth.Principal{Subject:"person",TenantID:"team",AuthType:auth.AuthTypeLocal},`{"name":"test"}`,201},
 }{t.Run(tc.name,func(t *testing.T){s:=&integrationManagementFake{};h,err:=NewMLflowIntegrationHandler(s,MLflowIntegrationOptions{Pepper:[]byte(strings.Repeat("p",32))});if err!=nil{t.Fatal(err)};r:=gin.New();r.Use(func(c *gin.Context){c.Set("ray-platform-principal",tc.principal);c.Next()});h.RegisterRoutes(r.Group("/api/v1"));w:=httptest.NewRecorder();r.ServeHTTP(w,httptest.NewRequest(http.MethodPost,"/api/v1/mlflow/integrations",strings.NewReader(tc.body)));if w.Code!=tc.want{t.Fatalf("got %d: %s",w.Code,w.Body.String())};if w.Header().Get("Cache-Control")!="no-store"{t.Fatal("missing no-store")};if tc.want!=201&&s.creates!=0{t.Fatal("invalid request mutated store")}})}
}
