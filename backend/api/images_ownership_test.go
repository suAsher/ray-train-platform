package api

import (
 "context"
 "errors"
 "testing"
 "net/http"
 "net/http/httptest"
 "strings"
 "github.com/gin-gonic/gin"
 "ray-train-platform-backend/auth"
 "ray-train-platform-backend/domain"
)

type ownedImageStore struct { stubImageStore }
func (s *ownedImageStore) ListImagesForUser(ctx context.Context, tenant, user, kind string) ([]domain.PlatformImage,error) { return s.ListImages(ctx,tenant,kind) }

func TestRuntimeResolutionChecksImageOwnerWithoutRoleBypass(t *testing.T) {
 image:=catalogImage("registry.example/private:one")
 image.IsDefault=false
 image.TenantID,image.OwnerUserID,image.Visibility="team-a","owner",domain.ImageVisibilityPersonal
 service:=NewSubmissionService(&submissionServiceRepository{},SubmissionServiceOptions{Images:&ownedImageStore{stubImageStore{images:[]domain.PlatformImage{image}}},ImageAllowlist:[]string{image.Reference}})
 for _,tc:=range []struct{tenant,user string; allowed bool}{{"team-a","owner",true},{"team-a","admin",false},{"team-b","owner",false},{"team-a","",false}} {
  _,err:=service.resolveRuntime(context.Background(),tc.tenant,tc.user,domain.JobSpec{Image:image.Reference})
  if tc.allowed && err!=nil {t.Fatal(err)}
  if !tc.allowed && !errors.Is(err,ErrSubmissionImageNotAllowed) {t.Fatalf("private ref bypass %+v: %v",tc,err)}
 }
}

func TestImageListAndWorkspaceHidePersonalImageFromAdministrators(t *testing.T) {
 image:=catalogImage("registry.example/private:one")
 image.ID,image.IsDefault,image.TenantID,image.OwnerUserID,image.Visibility="private-image",false,"team-a","owner",domain.ImageVisibilityPersonal
 for _,role:=range []string{domain.RoleEngineer,domain.RoleTenantAdmin,domain.RoleSuperAdmin} {
  p:=auth.Principal{Subject:"other",TenantID:"team-a",Roles:[]string{role},AuthType:auth.AuthTypeLocal}
  store:=&ownedImageStore{stubImageStore{images:[]domain.PlatformImage{image}}}
  for _,suffix:=range []string{"", "?includeAllTenants=true"} {
   w:=httptest.NewRecorder();imageScopeRouter(store,p).ServeHTTP(w,httptest.NewRequest(http.MethodGet,"/api/v1/images"+suffix,nil))
   if strings.Contains(w.Body.String(),image.Reference) {t.Fatalf("private list leak: %s",w.Body.String())}
  }
  workspaceImage:=image;workspaceImage.Kind=domain.ImageKindWorkspace
  h:=NewHandler(nil,Options{Images:&ownedImageStore{stubImageStore{images:[]domain.PlatformImage{workspaceImage}}}})
  w:=httptest.NewRecorder(); c,_:=gin.CreateTestContext(w);c.Request=httptest.NewRequest(http.MethodPost,"/workspaces",nil);c.Set("ray-platform-principal",p)
  if _,ok:=h.resolveWorkspaceImage(c,p.TenantID,workspaceImage.Reference);ok {t.Fatalf("%s opened private workspace image",role)}
 }
}

func TestSharedRuntimeContractsNeverInheritOwnerImage(t *testing.T) {
 p:=auth.Principal{Subject:"owner",TenantID:"team-a",Roles:[]string{domain.RoleSuperAdmin}}
 for _,input:=range []SubmissionInput{
  {Principal:p,SharedImagesOnly:true},
  {Principal:p,Origin:domain.SubmissionOriginEvaluation},
  {Principal:p,Origin:domain.SubmissionOriginServing},
 } {if got:=imageUserID(input);got!="" {t.Fatalf("shared contract inherited owner %q",got)}}
 if got:=imageUserID(SubmissionInput{Principal:p,Origin:domain.SubmissionOriginPortal});got!="owner" {t.Fatalf("ordinary training lost owner: %q",got)}
}
