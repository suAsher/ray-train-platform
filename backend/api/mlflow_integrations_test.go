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
  {"noncanonical field",auth.Principal{Subject:"person",TenantID:"team",AuthType:auth.AuthTypeLocal},`{"NAME":"test"}`,400},
  {"duplicate fields",auth.Principal{Subject:"person",TenantID:"team",AuthType:auth.AuthTypeLocal},`{"name":"first","name":"second"}`,400},
  {"duplicate permission policy",auth.Principal{Subject:"person",TenantID:"team",AuthType:auth.AuthTypeLocal},`{"name":"test","allowCreateExperiments":false,"allowCreateExperiments":true}`,400},
  {"empty name",auth.Principal{Subject:"person",TenantID:"team",AuthType:auth.AuthTypeLocal},`{"name":"  "}`,400},
  {"valid",auth.Principal{Subject:"person",TenantID:"team",AuthType:auth.AuthTypeLocal},`{"name":"test"}`,201},
 }{t.Run(tc.name,func(t *testing.T){s:=&integrationManagementFake{};h,err:=NewMLflowIntegrationHandler(s,MLflowIntegrationOptions{Pepper:[]byte(strings.Repeat("p",32))});if err!=nil{t.Fatal(err)};r:=gin.New();r.Use(func(c *gin.Context){c.Set("ray-platform-principal",tc.principal);c.Next()});h.RegisterRoutes(r.Group("/api/v1"));w:=httptest.NewRecorder();r.ServeHTTP(w,httptest.NewRequest(http.MethodPost,"/api/v1/mlflow/integrations",strings.NewReader(tc.body)));if w.Code!=tc.want{t.Fatalf("got %d: %s",w.Code,w.Body.String())};if w.Header().Get("Cache-Control")!="no-store"{t.Fatal("missing no-store")};if tc.want!=201&&s.creates!=0{t.Fatal("invalid request mutated store")}})}
}
type integrationTokenFake struct{integrations.ManagementStore;token domain.PersonalAccessToken;digest string;created int}
func(s *integrationTokenFake)CreateToken(_ context.Context,_ auth.Principal,_ string,token domain.PersonalAccessToken,digest string)error{s.token=token;s.digest=digest;s.created++;return nil}
func(s *integrationTokenFake)ListTokens(context.Context,auth.Principal,string)([]domain.PersonalAccessToken,error){return []domain.PersonalAccessToken{s.token},nil}
func TestIntegrationTokenIssuanceReturnsSecretOnlyOnceAndValidatesScopes(t *testing.T){
 s:=&integrationTokenFake{};h,err:=NewMLflowIntegrationHandler(s,MLflowIntegrationOptions{Pepper:[]byte(strings.Repeat("p",32))});if err!=nil{t.Fatal(err)}
 r:=gin.New();r.Use(func(c *gin.Context){c.Set("ray-platform-principal",auth.Principal{Subject:"owner",TenantID:"team",AuthType:auth.AuthTypeLocal});c.Next()});h.RegisterRoutes(r.Group("/api/v1"));path:="/api/v1/mlflow/integrations/"+strings.Repeat("a",32)+"/tokens"
 for _,body:=range []string{`{"scopes":["experiments:read"],"expiresInDays":0}`,`{"scopes":["experiments:read"],"expiresInDays":31}`,`{"scopes":["experiments:write"],"expiresInDays":1}`,`{"scopes":["experiments:read","jobs:read"],"expiresInDays":1}`,`{"scopes":["experiments:read","artifacts:write"],"expiresInDays":1}`}{w:=httptest.NewRecorder();r.ServeHTTP(w,httptest.NewRequest("POST",path,strings.NewReader(body)));if w.Code!=400{t.Fatalf("accepted %s: %d",body,w.Code)}}
 if s.created!=0{t.Fatal("invalid scopes minted a token")};w:=httptest.NewRecorder();r.ServeHTTP(w,httptest.NewRequest("POST",path,strings.NewReader(`{"scopes":["experiments:read","experiments:write","artifacts:read","artifacts:write"],"expiresInDays":1}`)));if w.Code!=201{t.Fatalf("issue %d: %s",w.Code,w.Body.String())}
 var result struct{Data domain.IssuedPersonalAccessToken `json:"data"`};if err:=json.Unmarshal(w.Body.Bytes(),&result);err!=nil{t.Fatal(err)};if !domain.VerifyPersonalAccessToken([]byte(strings.Repeat("p",32)),result.Data.Token,s.digest)||s.created!=1{t.Fatal("token did not match persisted digest")};if strings.Contains(w.Body.String(),s.digest){t.Fatal("response exposed digest")}
 w=httptest.NewRecorder();r.ServeHTTP(w,httptest.NewRequest("GET",path,nil));if w.Code!=200||strings.Contains(w.Body.String(),result.Data.Token)||strings.Contains(w.Body.String(),s.digest)||strings.Contains(w.Body.String(),`"token":`){t.Fatalf("list exposed secret: status %d",w.Code)};if w.Header().Get("Cache-Control")!="no-store"{t.Fatal("token list cacheable")}
}
