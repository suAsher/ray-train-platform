package repositories

import (
 "context"
 "errors"
 "fmt"
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
func TestIntegrationCreatedGrantDoesNotReviveTombstone(t *testing.T){
 r,s,p:=integrationFixture(t);identity:=createIntegrationFixture(t,s,p);ctx:=context.Background();id:=strings.Repeat("c",32)
 if err:=r.db.Create(&MLflowTrackingExperimentRecord{ID:id,TenantID:p.TenantID,UserID:p.Subject,Name:"pending",State:"PENDING",IdempotencyHash:strings.Repeat("e",64)}).Error;err!=nil{t.Fatal(err)}
 machine:=auth.Principal{Subject:"integration:"+identity.ID,IntegrationID:identity.ID,TenantID:p.TenantID,AuthType:auth.AuthTypePAT,Scopes:[]string{"experiments:read","experiments:write"}}
 if err:=s.GrantCreated(ctx,machine,id);err!=nil{t.Fatal(err)}
 if err:=s.RevokeGrant(ctx,p,identity.ID,id,time.Now());err!=nil{t.Fatal(err)}
 if err:=s.GrantCreated(ctx,machine,id);!errors.Is(err,integrations.ErrNotFound){t.Fatalf("revoked grant retry must conceal metadata: %v",err)}
 if err:=r.db.Model(&MLflowTrackingExperimentRecord{}).Where("id = ?",id).Updates(map[string]any{"state":"READY","upstream_id":"10"}).Error;err!=nil{t.Fatal(err)}
 if _,err:=s.Authorize(ctx,machine,id,"read");!errors.Is(err,integrations.ErrNotFound){t.Fatalf("repeated create revived revoked grant: %v",err)}
}
func TestIntegrationLifecycleTokenRevocationListsAndGrantPermissions(t *testing.T){
 r,s,p:=integrationFixture(t);identity:=createIntegrationFixture(t,s,p);ctx:=context.Background();id:=strings.Repeat("c",32)
 if err:=r.db.Create(&MLflowTrackingExperimentRecord{ID:id,TenantID:p.TenantID,UserID:p.Subject,Name:"ready",State:"READY",UpstreamID:"77",IdempotencyHash:strings.Repeat("e",64)}).Error;err!=nil{t.Fatal(err)}
 items,err:=s.List(ctx,p);if err!=nil||len(items)!=1{t.Fatalf("list: %v %v",items,err)}
 grant:=integrations.Grant{IntegrationID:identity.ID,ExperimentID:id,Permissions:[]string{"read","write","artifacts:read","artifacts:write"},CreatedAt:time.Now().UTC()}
 if err:=s.PutGrant(ctx,p,identity.ID,grant);err!=nil{t.Fatal(err)};grants,err:=s.ListGrants(ctx,p,identity.ID);if err!=nil||len(grants)!=1{t.Fatal(err)}
 machine:=auth.Principal{Subject:"integration:"+identity.ID,IntegrationID:identity.ID,TenantID:p.TenantID,AuthType:auth.AuthTypePAT,Scopes:[]string{"experiments:read","experiments:write"}}
 ids,err:=s.ListGrantedExperimentIDs(ctx,machine,"artifacts:write");if err!=nil||len(ids)!=1||ids[0]!=id{t.Fatalf("granted ids %v %v",ids,err)}
 now:=time.Now().UTC();issued,err:=domain.IssuePersonalAccessToken(domain.PersonalAccessTokenInput{ID:strings.Repeat("b",32),TenantID:p.TenantID,UserID:machine.Subject,Scopes:machine.Scopes,ExpiresAt:now.Add(time.Hour)},[]byte(strings.Repeat("p",32)),now);if err!=nil{t.Fatal(err)}
 if err:=s.CreateToken(ctx,p,identity.ID,issued.PersonalAccessToken,issued.Digest);err!=nil{t.Fatal(err)}
 authenticator,err:=auth.NewPATAuthenticator(r,[]byte(strings.Repeat("p",32)),nil);if err!=nil{t.Fatal(err)}
 authenticated,err:=authenticator.Authenticate(ctx,issued.Token);if err!=nil||authenticated.Principal.IntegrationID!=identity.ID{t.Fatalf("auth failed %v",err)}
 tokens,err:=s.ListTokens(ctx,p,identity.ID);if err!=nil||len(tokens)!=1||tokens[0].LastUsedAt==nil{t.Fatalf("token list/touch %v %v",tokens,err)}
 if err:=s.RevokeToken(ctx,p,identity.ID,issued.ID,now);err!=nil{t.Fatal(err)};if _,err:=authenticator.Authenticate(ctx,issued.Token);!errors.Is(err,auth.ErrInvalidPAT){t.Fatalf("revoked token authenticated: %v",err)}
 if err:=s.RevokeGrant(ctx,p,identity.ID,id,now);err!=nil{t.Fatal(err)};ids,err=s.ListGrantedExperimentIDs(ctx,machine,"read");if err!=nil||len(ids)!=0{t.Fatalf("revoked grant listed: %v %v",ids,err)}
 if err:=s.PutGrant(ctx,p,identity.ID,grant);err!=nil{t.Fatal(err)};if _,err:=s.Authorize(ctx,machine,id,"write");err!=nil{t.Fatalf("explicit owner regrant failed: %v",err)}
 if err:=s.Revoke(ctx,p,identity.ID,now);err!=nil{t.Fatal(err)};if _,err:=s.Resolve(ctx,machine);!errors.Is(err,integrations.ErrNotFound){t.Fatalf("revoked identity resolved: %v",err)}
 if err:=s.PutGrant(ctx,p,identity.ID,grant);!errors.Is(err,integrations.ErrNotFound){t.Fatalf("revoked identity accepted new grant: %v",err)}
}
func TestIntegrationAuthenticationRejectsTenantRetirementExpiryAndUnknownScopes(t *testing.T){
 for _,mode:=range []string{"retired","expired","unsafe-scopes"}{t.Run(mode,func(t *testing.T){r,s,p:=integrationFixture(t);identity:=createIntegrationFixture(t,s,p);now:=time.Now().UTC();token:=MLflowIntegrationTokenRecord{ID:strings.Repeat("b",32),PublicID:"1234567890123456",IntegrationID:identity.ID,TokenDigest:strings.Repeat("f",64),ScopesJSON:`["experiments:read"]`,ExpiresAt:now.Add(time.Hour),CreatedAt:now};if mode=="expired"{token.ExpiresAt=now.Add(-time.Hour)};if mode=="unsafe-scopes"{token.ScopesJSON=`["experiments:read","jobs:write"]`};if err:=r.db.Create(&token).Error;err!=nil{t.Fatal(err)};if mode=="retired"{if err:=r.db.Model(&TenantRecord{}).Where("id = ?",p.TenantID).Update("retired_at",now).Error;err!=nil{t.Fatal(err)}};if _,err:=r.FindPATByPublicID(context.Background(),token.PublicID);!errors.Is(err,auth.ErrPATNotFound){t.Fatalf("accepted %s: %v",mode,err)}})}
}
func TestIntegrationOwnerAndTokenLimitsAndInvalidDirectStoreInputs(t *testing.T){
 r,s,p:=integrationFixture(t);identity:=createIntegrationFixture(t,s,p);ctx:=context.Background()
 invalid:=identity;invalid.ID="not-an-id";if err:=s.Create(ctx,p,invalid);!errors.Is(err,integrations.ErrInvalid){t.Fatal(err)}
 for i:=1;i<20;i++{if err:=r.db.Create(&MLflowIntegrationRecord{ID:strings.Repeat("d",30)+fmt.Sprintf("%02x",i),TenantID:p.TenantID,OwnerUserID:p.Subject,Name:"another"}).Error;err!=nil{t.Fatal(err)}}
 extra:=identity;extra.ID=strings.Repeat("f",32);if err:=s.Create(ctx,p,extra);!errors.Is(err,integrations.ErrLimit){t.Fatalf("owner cap not enforced: %v",err)}
 now:=time.Now().UTC();for i:=0;i<20;i++{if err:=r.db.Create(&MLflowIntegrationTokenRecord{ID:fmt.Sprintf("%032x",i+1),PublicID:fmt.Sprintf("%016x",i+1),IntegrationID:identity.ID,ScopesJSON:`["experiments:read"]`,ExpiresAt:now.Add(time.Hour)}).Error;err!=nil{t.Fatal(err)}}
 issued,err:=domain.IssuePersonalAccessToken(domain.PersonalAccessTokenInput{ID:strings.Repeat("b",32),TenantID:p.TenantID,UserID:"integration:"+identity.ID,Scopes:[]string{"experiments:read"},ExpiresAt:now.Add(time.Hour)},[]byte(strings.Repeat("p",32)),now);if err!=nil{t.Fatal(err)}
 if err:=s.CreateToken(ctx,p,identity.ID,issued.PersonalAccessToken,issued.Digest);!errors.Is(err,integrations.ErrLimit){t.Fatalf("token cap not enforced: %v",err)}
}
func TestIntegrationTokenListPreservesActiveCredentialsBeforeRevokedHistory(t *testing.T){
 r,s,p:=integrationFixture(t);identity:=createIntegrationFixture(t,s,p);now:=time.Now().UTC()
 active:=MLflowIntegrationTokenRecord{ID:strings.Repeat("f",32),PublicID:"activecredential",IntegrationID:identity.ID,ScopesJSON:`["experiments:read"]`,CreatedAt:now.Add(-24*time.Hour),ExpiresAt:now.Add(time.Hour)}
 if err:=r.db.Create(&active).Error;err!=nil{t.Fatal(err)}
 for i:=0;i<105;i++{if err:=r.db.Create(&MLflowIntegrationTokenRecord{ID:fmt.Sprintf("%032x",i+1),PublicID:fmt.Sprintf("%016x",i+1),IntegrationID:identity.ID,ScopesJSON:`["experiments:read"]`,CreatedAt:now,ExpiresAt:now.Add(time.Hour),RevokedAt:&now}).Error;err!=nil{t.Fatal(err)}}
 tokens,err:=s.ListTokens(context.Background(),p,identity.ID);if err!=nil{t.Fatal(err)};if len(tokens)!=100{t.Fatalf("token limit: got %d",len(tokens))};if tokens[0].ID!=active.ID||tokens[0].RevokedAt!=nil{t.Fatalf("active credential hidden behind history: first=%+v",tokens[0])}
}
