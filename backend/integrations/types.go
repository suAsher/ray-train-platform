// Package integrations defines the resource boundary for external MLflow clients.
package integrations

import (
 "context"
 "errors"
 "time"
 "ray-train-platform-backend/auth"
 "ray-train-platform-backend/domain"
)

var (
 ErrNotFound = errors.New("integration resource not found")
 ErrInvalid = errors.New("invalid integration request")
 ErrLimit = errors.New("integration resource limit reached")
 ErrConflict = errors.New("integration resource conflict")
)
type Identity struct {
 ID string `json:"id"`
 TenantID string `json:"tenantId"`
 OwnerUserID string `json:"ownerUserId"`
 Name string `json:"name"`
 AllowCreateExperiments bool `json:"allowCreateExperiments"`
 RevokedAt *time.Time `json:"revokedAt,omitempty"`
 CreatedAt time.Time `json:"createdAt"`
}
type Grant struct {
 IntegrationID string `json:"integrationId"`
 ExperimentID string `json:"experimentId"`
 Permissions []string `json:"permissions"`
 RevokedAt *time.Time `json:"revokedAt,omitempty"`
 CreatedAt time.Time `json:"createdAt"`
}
// AccessStore always resolves the live owner and tenant; callers must not cache grants.
type AccessStore interface {
 Resolve(context.Context, auth.Principal) (Identity,error)
 Authorize(context.Context, auth.Principal,string,string) (Identity,error)
 ListGrantedExperimentIDs(context.Context, auth.Principal,string) ([]string,error)
 GrantCreated(context.Context, auth.Principal,string) error
}
type ManagementStore interface {
 AccessStore
 List(context.Context,auth.Principal)([]Identity,error)
 Create(context.Context,auth.Principal,Identity) error
 Revoke(context.Context,auth.Principal,string,time.Time) error
 ListTokens(context.Context,auth.Principal,string)([]domain.PersonalAccessToken,error)
 CreateToken(context.Context,auth.Principal,string,domain.PersonalAccessToken,string) error
 RevokeToken(context.Context,auth.Principal,string,string,time.Time) error
 ListGrants(context.Context,auth.Principal,string)([]Grant,error)
 PutGrant(context.Context,auth.Principal,string,Grant) error
 RevokeGrant(context.Context,auth.Principal,string,string,time.Time) error
}
