package repositories

import (
 "context"
 "errors"
 "strings"
 "testing"
 "time"
 "ray-train-platform-backend/auth"
 "ray-train-platform-backend/domain"
 "ray-train-platform-backend/integrations"
)

func integrationFixture(t *testing.T)(*GormRepository,*MLflowIntegrationStore,auth.Principal){
 t.Helper();r:=patTestRepository(t)
 if err:=r.db.AutoMigrate(&LocalUserRecord{},&TenantMembershipRecord{},&MLflowTrackingExperimentRecord{},&MLflowIntegrationRecord{},&MLflowIntegrationTokenRecord{},&MLflowIntegrationGrantRecord{},&MLflowIntegrationAuditRecord{});err!=nil{t.Fatal(err)}
 p:=auth.Principal{Subject:"owner",TenantID:"team",AuthType:auth.AuthTypeLocal,Roles:[]string{"SuperAdmin"}}
 if err:=r.db.Create(&TenantRecord{ID:p.TenantID,Name:"team",Namespace:"team"}).Error;err!=nil{t.Fatal(err)}
 if err:=r.db.Create(&LocalUserRecord{ID:p.Subject,Username:"owner",TenantID:"team",ActiveTenantID:"team",StorageKey:"kept",RolesJSON:`["Engineer"]`,GlobalRolesJSON:`["SuperAdmin"]`}).Error;err!=nil{t.Fatal(err)}
 if err:=r.db.Create(&TenantMembershipRecord{IdentityID:p.Subject,TenantID:p.TenantID,RolesJSON:`["Engineer"]`,Status:domain.MembershipStatusActive}).Error;err!=nil{t.Fatal(err)}
 return r,NewMLflowIntegrationStore(r),p
}
func createIntegrationFixture(t *testing.T,s *MLflowIntegrationStore,p auth.Principal) integrations.Identity{
 t.Helper();v:=integrations.Identity{ID:strings.Repeat("a",32),TenantID:p.TenantID,OwnerUserID:p.Subject,Name:"pipeline",AllowCreateExperiments:true,CreatedAt:time.Now().UTC()};if err:=s.Create(context.Background(),p,v);err!=nil{t.Fatal(err)};return v
}
func TestIntegrationAuthenticationRechecksOwnerMembershipRevocationAndDoesNotInheritRoles(t *testing.T){
 r,s,p:=integrationFixture(t);identity:=createIntegrationFixture(t,s,p);now:=time.Now().UTC();issued,err:=domain.IssuePersonalAccessToken(domain.PersonalAccessTokenInput{ID:strings.Repeat("b",32),TenantID:p.TenantID,UserID:"integration:"+identity.ID,Scopes:[]string{"experiments:read"},ExpiresAt:now.Add(time.Hour)},[]byte(strings.Repeat("p",32)),now);if err!=nil{t.Fatal(err)}
 if err:=s.CreateToken(context.Background(),p,identity.ID,issued.PersonalAccessToken,issued.Digest);err!=nil{t.Fatal(err)}
 record,err:=r.FindPATByPublicID(context.Background(),issued.PublicID);if err!=nil{t.Fatal(err)};if record.Principal.IntegrationID!=identity.ID||record.Principal.Subject!="integration:"+identity.ID||len(record.Principal.Roles)!=0||record.Principal.StorageKey!=""{t.Fatalf("unsafe identity %+v",record.Principal)}
 for _,mutation:=range []struct{table,where,column string;value any}{{"tenant_memberships","identity_id = 'owner'","status","inactive"},{"local_users","id = 'owner'","disabled",true},{"mlflow_integrations","id = '"+identity.ID+"'","revoked_at",now}}{
  tx:=r.db.Begin();if err:=tx.Table(mutation.table).Where(mutation.where).Update(mutation.column,mutation.value).Error;err!=nil{t.Fatal(err)};isolated:=NewGormRepository(tx);if _,err:=isolated.FindPATByPublicID(context.Background(),issued.PublicID);!errors.Is(err,auth.ErrPATNotFound){t.Fatalf("revocation accepted %s: %v",mutation.table,err)};tx.Rollback()
 }
}
func TestIntegrationGrantsCheckOwnerReadyStateAndDoNotGrantSuperAdminOverride(t *testing.T){
 r,s,p:=integrationFixture(t);identity:=createIntegrationFixture(t,s,p);ctx:=context.Background();experimentID:=strings.Repeat("c",32)
 if err:=r.db.Create(&MLflowTrackingExperimentRecord{ID:experimentID,TenantID:p.TenantID,UserID:p.Subject,Name:"mine",State:"READY",UpstreamID:"123",IdempotencyHash:strings.Repeat("d",64)}).Error;err!=nil{t.Fatal(err)}
 grant:=integrations.Grant{IntegrationID:identity.ID,ExperimentID:experimentID,Permissions:[]string{"read"},CreatedAt:time.Now().UTC()};if err:=s.PutGrant(ctx,p,identity.ID,grant);err!=nil{t.Fatal(err)}
 machine:=auth.Principal{Subject:"integration:"+identity.ID,IntegrationID:identity.ID,TenantID:p.TenantID,AuthType:auth.AuthTypePAT}
 if _,err:=s.Authorize(ctx,machine,experimentID,"read");err!=nil{t.Fatal(err)};if _,err:=s.Authorize(ctx,machine,experimentID,"write");!errors.Is(err,integrations.ErrNotFound){t.Fatalf("write should not be granted: %v",err)}
 if err:=r.db.Model(&MLflowTrackingExperimentRecord{}).Where("id = ?",experimentID).Update("user_id","another-owner").Error;err!=nil{t.Fatal(err)};if _,err:=s.Authorize(ctx,machine,experimentID,"read");!errors.Is(err,integrations.ErrNotFound){t.Fatalf("foreign owner accepted: %v",err)}
 if err:=s.PutGrant(ctx,p,identity.ID,grant);!errors.Is(err,integrations.ErrNotFound){t.Fatalf("SuperAdmin managed another owner's experiment: %v",err)}
 other:=p;other.Subject="different";if _,err:=s.ListTokens(ctx,other,identity.ID);!errors.Is(err,integrations.ErrNotFound){t.Fatalf("foreign integration visible: %v",err)}
}
func TestIntegrationAuditFailureRollsBackMutation(t *testing.T){
 r,s,p:=integrationFixture(t);if err:=r.db.Migrator().DropTable(&MLflowIntegrationAuditRecord{});err!=nil{t.Fatal(err)}
 err:=s.Create(context.Background(),p,integrations.Identity{ID:strings.Repeat("a",32),TenantID:p.TenantID,OwnerUserID:p.Subject,Name:"pipeline",CreatedAt:time.Now().UTC()});if err==nil{t.Fatal("missing audit table accepted")};var n int64;r.db.Model(&MLflowIntegrationRecord{}).Count(&n);if n!=0{t.Fatal("mutation persisted without audit")}
}
