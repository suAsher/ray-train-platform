package environmentbuild

import (
 "context"
 "errors"
 "fmt"
 "strings"
 "testing"

 "ray-train-platform-backend/registryauth"
)

type authenticationFailureRegistry struct{Registry;failure error}
func(r authenticationFailureRegistry)Authenticate(context.Context,Credentials)error{return r.failure}
func(r authenticationFailureRegistry)CheckPush(context.Context,Credentials,string)error{return r.failure}

func TestRegistryAvailabilityIsNotReportedAsIncorrectCredentials(t *testing.T){
 for _,tc:=range []struct{failure,want error}{
  {registryauth.ErrCredentials,ErrAuthorization},{registryauth.ErrForbidden,ErrAuthorization},{ErrAuthorization,ErrAuthorization},
  {registryauth.ErrUnavailable,ErrUnavailable},{context.DeadlineExceeded,ErrUnavailable},{errors.New("private-upstream?token=fixture-secret"),ErrUnavailable},
 }{
  s,_,_,_,_:=lifecycleFixture(t);s.registry=authenticationFailureRegistry{failure:fmt.Errorf("wrapped: %w",tc.failure)}
  _,err:=s.CreateAuthorization(context.Background(),Owner{UserID:"owner",TenantID:"team"},Credentials{Username:"harbor-user",Secret:"fixture"})
  if !errors.Is(err,tc.want) || strings.Contains(err.Error(),"fixture-secret"){t.Fatalf("wrong authentication classification: %v",err)}
  err=s.CheckTarget(context.Background(),Owner{UserID:"owner",TenantID:"team"},"auth","project","image")
  if !errors.Is(err,tc.want) || strings.Contains(err.Error(),"fixture-secret"){t.Fatalf("wrong target classification: %v",err)}
 }
}
