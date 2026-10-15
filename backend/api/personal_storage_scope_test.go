package api

import (
 "context"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"
 "github.com/gin-gonic/gin"
 "ray-train-platform-backend/auth"
 "ray-train-platform-backend/domain"
 "ray-train-platform-backend/personalstorage"
)

func TestPersonalStorageScopeDisablesUnlistedAdminWithoutBindingReads(t *testing.T) {
 service:=&recordingPersonalService{view:personalstorage.View{State:"READY",BindingID:"private-binding",PublicKey:"ssh-ed25519 private-owner-metadata"},store:&personalHTTPStore{root:t.TempDir()}}
 principal:=auth.Principal{Subject:"stable-bob",Username:"alice",TenantID:"team-a",AuthType:auth.AuthTypeOIDC,Roles:[]string{domain.RoleSuperAdmin}}
 h:=NewHandler(&fakeJobRepository{},Options{PersonalStorage:service,PersonalStorageTestOwnerIDs:[]string{"stable-alice"}})
 router:=personalStorageTestRouter(h,&principal)
 w:=httptest.NewRecorder();router.ServeHTTP(w,httptest.NewRequest(http.MethodGet,"/api/v1/personal-storage/wellspiking",nil))
 if w.Code!=http.StatusOK||!strings.Contains(w.Body.String(),`"enabled":false`)||!strings.Contains(w.Body.String(),`"connectionStatus":"DISABLED"`){t.Fatalf("unlisted status not disabled: %d %s",w.Code,w.Body)}
 for _,forbidden:=range []string{"private-binding","private-owner-metadata","ssh-ed25519"}{if strings.Contains(w.Body.String(),forbidden){t.Fatal("unlisted status leaked binding metadata")}}
 if service.owner.ID!=""{t.Fatal("unlisted status queried binding")}
 cases:=[]struct{method,path,body string}{
 {http.MethodPost,"/api/v1/personal-storage/wellspiking",`{}`},
 {http.MethodPost,"/api/v1/personal-storage/wellspiking/verify",`{}`},
 {http.MethodGet,"/api/v1/data-spaces/wellspiking-personal/entries",""},
 {http.MethodGet,"/api/v1/data-spaces/wellspiking-personal/directories",""},
 {http.MethodPost,"/api/v1/data-spaces/wellspiking-personal/folders",`{"path":"test"}`},
 {http.MethodPost,"/api/v1/data-spaces/wellspiking-personal/uploads",`{"path":"test","sizeBytes":1,"contentType":"text/plain"}`},
 {http.MethodPut,"/api/v1/data-spaces/wellspiking-personal/content?path=test","x"},
 }
 for _,tc:=range cases{w:=httptest.NewRecorder();req:=httptest.NewRequest(tc.method,tc.path,strings.NewReader(tc.body));req.Header.Set("Content-Type","application/json");req.Header.Set("X-RayTrain-Personal-Storage","1");router.ServeHTTP(w,req);if w.Code!=http.StatusForbidden{t.Errorf("unlisted admin route %s %s accepted: %d",tc.method,tc.path,w.Code)}}
 if service.owner.ID!=""||service.enrolled!=0||service.verified!=0||service.opened!=0{t.Fatal("unlisted admin reached binding service")}
 claim,err:=h.resolveWorkspacePersonalStorage(context.Background(),principal)
 if err!=nil||claim!=""||service.owner.ID!=""{t.Fatal("unlisted workspace queried or mounted personal binding")}
}

func TestPersonalStorageScopeClonesDeploymentIDsAndFollowsStableUserAcrossTeam(t *testing.T) {
 service:=&recordingPersonalService{view:personalstorage.View{State:"UNBOUND"}}
 ids:=[]string{"stable-alice"}
 h:=NewHandler(&fakeJobRepository{},Options{PersonalStorage:service,PersonalStorageTestOwnerIDs:ids})
 ids[0]="stable-bob"
 principal:=auth.Principal{Subject:"stable-alice",Username:"alice",TenantID:"team-a",AuthType:auth.AuthTypeOIDC}
 router:=personalStorageTestRouter(h,&principal)
 for _,team:=range []string{"team-a","team-b"}{principal.TenantID=team;w:=httptest.NewRecorder();router.ServeHTTP(w,httptest.NewRequest(http.MethodGet,"/api/v1/personal-storage/wellspiking",nil));if w.Code!=http.StatusOK||!strings.Contains(w.Body.String(),`"enabled":true`)||service.owner.ID!="stable-alice"{t.Fatal("scope did not retain stable personal owner")}}
 req:=httptest.NewRequest(http.MethodPost,"/api/v1/personal-storage/wellspiking",strings.NewReader(`{"testOwnerIDs":["stable-bob"]}`));req.Header.Set("Content-Type","application/json");req.Header.Set("X-RayTrain-Personal-Storage","1");w:=httptest.NewRecorder();router.ServeHTTP(w,req);if w.Code!=http.StatusBadRequest||service.enrolled!=0{t.Fatal("request modified deployment-owned scope")}
 principal.Subject="stable-bob";w=httptest.NewRecorder();router.ServeHTTP(w,httptest.NewRequest(http.MethodGet,"/api/v1/personal-storage/wellspiking",nil));if !strings.Contains(w.Body.String(),`"enabled":false`){t.Fatal("mutating caller's allowlist expanded handler scope")}
}

func TestPersonalStorageScopeFiltersDataSpacesAndGenericAuthorization(t *testing.T) {
 service:=&recordingPersonalService{view:personalstorage.View{State:"UNBOUND"}}
 h:=NewHandler(&fakeJobRepository{},Options{PersonalStorage:service,PersonalStorageTestOwnerIDs:[]string{"stable-alice"},DataSpaces:&fakeDataSpaceStore{}})
 principal:=auth.Principal{Subject:"stable-bob",Username:"bob",TenantID:"team-a",AuthType:auth.AuthTypeOIDC,Roles:[]string{domain.RoleSuperAdmin}}
 router:=personalStorageTestRouter(h,&principal)
 w:=httptest.NewRecorder();router.ServeHTTP(w,httptest.NewRequest(http.MethodGet,"/api/v1/data-spaces",nil));if w.Code!=http.StatusOK||strings.Contains(w.Body.String(),"wellspiking-personal")||service.owner.ID!=""{t.Fatal("unlisted user's data spaces queried or exposed personal binding")}
 gin.SetMode(gin.TestMode);w=httptest.NewRecorder();ctx,_:=gin.CreateTestContext(w);ctx.Request=httptest.NewRequest(http.MethodGet,"/",nil)
 if _,ok:=h.dataSpaceForPrincipal(ctx,principal,domain.DataSpaceWellspikingPersonal);ok||w.Code!=http.StatusForbidden{t.Fatal("generic data space authorization bypassed deployment scope")}
 principal.Subject="stable-alice";principal.Username="alice";w=httptest.NewRecorder();router.ServeHTTP(w,httptest.NewRequest(http.MethodGet,"/api/v1/data-spaces",nil));if w.Code!=http.StatusOK||!strings.Contains(w.Body.String(),"wellspiking-personal")||service.owner.ID!="stable-alice"{t.Fatal("allowed user lost personal data space")}
}
