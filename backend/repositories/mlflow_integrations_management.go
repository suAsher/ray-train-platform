package repositories

import (
 "context"
 "encoding/hex"
 "encoding/json"
 "errors"
 "time"
 "gorm.io/gorm"
 "gorm.io/gorm/clause"
 "ray-train-platform-backend/auth"
 "ray-train-platform-backend/domain"
 "ray-train-platform-backend/integrations"
)

func(s *MLflowIntegrationStore)List(ctx context.Context,p auth.Principal)([]integrations.Identity,error){
 if !auth.IsInteractiveAuthType(p.AuthType)||p.IntegrationID!=""{return nil,integrations.ErrNotFound};tx:=s.db.WithContext(ctx);if err:=integrationOwner(tx,p.TenantID,p.Subject,false);err!=nil{return nil,err}
 var rows []MLflowIntegrationRecord;if err:=tx.Where("tenant_id = ? AND owner_user_id = ?",p.TenantID,p.Subject).Order("created_at DESC, id ASC").Limit(20).Find(&rows).Error;err!=nil{return nil,err};result:=make([]integrations.Identity,0,len(rows));for _,r:=range rows{result=append(result,integrationValue(r))};return result,nil
}
func(s *MLflowIntegrationStore)Create(ctx context.Context,p auth.Principal,v integrations.Identity)error{
 if !auth.IsInteractiveAuthType(p.AuthType)||p.IntegrationID!=""||!integrations.ValidID(v.ID)||!integrations.ValidName(v.Name)||v.TenantID!=p.TenantID||v.OwnerUserID!=p.Subject||v.RevokedAt!=nil{return integrations.ErrInvalid}
 return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB)error{
  if err:=integrationOwner(tx,p.TenantID,p.Subject,true);err!=nil{return err};var count int64;if err:=tx.Model(&MLflowIntegrationRecord{}).Where("tenant_id = ? AND owner_user_id = ?",p.TenantID,p.Subject).Count(&count).Error;err!=nil{return err};if count>=20{return integrations.ErrLimit}
  if err:=integrationAudit(tx,p,v.ID,"integration.create",v.ID);err!=nil{return err}
  return tx.Create(&MLflowIntegrationRecord{ID:v.ID,TenantID:v.TenantID,OwnerUserID:v.OwnerUserID,Name:v.Name,AllowCreateExperiments:v.AllowCreateExperiments,CreatedAt:v.CreatedAt.UTC()}).Error
 })
}
func(s *MLflowIntegrationStore)Revoke(ctx context.Context,p auth.Principal,id string,now time.Time)error{
 return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB)error{
  if _,err:=managedIntegration(tx,p,id,true,true);err!=nil{return err};if err:=integrationAudit(tx,p,id,"integration.revoke",id);err!=nil{return err}
  if err:=tx.Model(&MLflowIntegrationRecord{}).Where("id = ? AND revoked_at IS NULL",id).Update("revoked_at",now.UTC()).Error;err!=nil{return err}
  return tx.Model(&MLflowIntegrationTokenRecord{}).Where("integration_id = ? AND revoked_at IS NULL",id).Update("revoked_at",now.UTC()).Error
 })
}
func integrationTokenValue(row MLflowIntegrationTokenRecord,identity MLflowIntegrationRecord)(domain.PersonalAccessToken,error){
 var scopes []string;if err:=json.Unmarshal([]byte(row.ScopesJSON),&scopes);err!=nil{return domain.PersonalAccessToken{},err};scopes,err:=integrations.NormalizeScopes(scopes);if err!=nil{return domain.PersonalAccessToken{},err}
 return domain.PersonalAccessToken{ID:row.ID,PublicID:row.PublicID,TenantID:identity.TenantID,UserID:"integration:"+identity.ID,Scopes:scopes,ExpiresAt:row.ExpiresAt,LastUsedAt:row.LastUsedAt,RevokedAt:row.RevokedAt,CreatedAt:row.CreatedAt},nil
}
func(s *MLflowIntegrationStore)ListTokens(ctx context.Context,p auth.Principal,id string)([]domain.PersonalAccessToken,error){
 tx:=s.db.WithContext(ctx);identity,err:=managedIntegration(tx,p,id,false,true);if err!=nil{return nil,err};var rows []MLflowIntegrationTokenRecord
 if err:=tx.Where("integration_id = ?",id).Order("CASE WHEN revoked_at IS NULL THEN 0 ELSE 1 END, created_at DESC, id ASC").Limit(100).Find(&rows).Error;err!=nil{return nil,err}
 values:=make([]domain.PersonalAccessToken,0,len(rows));for _,r:=range rows{v,err:=integrationTokenValue(r,identity);if err!=nil{return nil,err};values=append(values,v)};return values,nil
}
func(s *MLflowIntegrationStore)CreateToken(ctx context.Context,p auth.Principal,id string,token domain.PersonalAccessToken,digest string)error{
 scopes,err:=integrations.NormalizeScopes(token.Scopes);if err!=nil{return err};if !integrations.ValidID(token.ID)||token.TenantID!=p.TenantID||token.UserID!="integration:"+id||token.RevokedAt!=nil||!token.ExpiresAt.After(time.Now().UTC())||!token.ExpiresAt.After(token.CreatedAt)||token.ExpiresAt.After(token.CreatedAt.Add(30*24*time.Hour)){return integrations.ErrInvalid}
 if _,err:=hex.DecodeString(digest);err!=nil||len(digest)!=64||len(token.PublicID)!=16{return integrations.ErrInvalid};encoded,err:=json.Marshal(scopes);if err!=nil{return err}
 return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB)error{
  if _,err:=managedIntegration(tx,p,id,true,false);err!=nil{return err};var count int64
  if err:=tx.Model(&MLflowIntegrationTokenRecord{}).Where("integration_id = ? AND revoked_at IS NULL",id).Count(&count).Error;err!=nil{return err};if count>=20{return integrations.ErrLimit}
  if err:=integrationAudit(tx,p,id,"integration.token.create",token.ID);err!=nil{return err}
  return tx.Create(&MLflowIntegrationTokenRecord{ID:token.ID,PublicID:token.PublicID,IntegrationID:id,TokenDigest:digest,ScopesJSON:string(encoded),ExpiresAt:token.ExpiresAt.UTC(),CreatedAt:token.CreatedAt.UTC()}).Error
 })
}
func(s *MLflowIntegrationStore)RevokeToken(ctx context.Context,p auth.Principal,id,tokenID string,now time.Time)error{
 if !integrations.ValidID(tokenID){return integrations.ErrNotFound};return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB)error{
  if _,err:=managedIntegration(tx,p,id,true,true);err!=nil{return err};var row MLflowIntegrationTokenRecord;if err:=tx.Where("id = ? AND integration_id = ?",tokenID,id).First(&row).Error;err!=nil{return integrationError(err)}
  if err:=integrationAudit(tx,p,id,"integration.token.revoke",tokenID);err!=nil{return err};return tx.Model(&MLflowIntegrationTokenRecord{}).Where("id = ? AND revoked_at IS NULL",tokenID).Update("revoked_at",now.UTC()).Error
 })
}
func(s *MLflowIntegrationStore)ListGrants(ctx context.Context,p auth.Principal,id string)([]integrations.Grant,error){
 tx:=s.db.WithContext(ctx);if _,err:=managedIntegration(tx,p,id,false,true);err!=nil{return nil,err};var rows []MLflowIntegrationGrantRecord;if err:=tx.Where("integration_id = ?",id).Order("experiment_id ASC").Limit(100).Find(&rows).Error;err!=nil{return nil,err}
 values:=make([]integrations.Grant,0,len(rows));for _,r:=range rows{permissions,err:=decodeIntegrationPermissions(r.PermissionsJSON);if err!=nil{return nil,err};values=append(values,integrations.Grant{IntegrationID:r.IntegrationID,ExperimentID:r.ExperimentID,Permissions:permissions,RevokedAt:r.RevokedAt,CreatedAt:r.CreatedAt})};return values,nil
}
func(s *MLflowIntegrationStore)PutGrant(ctx context.Context,p auth.Principal,id string,grant integrations.Grant)error{
 permissions,err:=integrations.NormalizePermissions(grant.Permissions);if err!=nil{return err};if grant.IntegrationID!=id||grant.RevokedAt!=nil{return integrations.ErrInvalid};encoded,err:=json.Marshal(permissions);if err!=nil{return err}
 return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB)error{
  identity,err:=managedIntegration(tx,p,id,true,false);if err!=nil{return err};if err:=integrationExperiment(tx,identity,grant.ExperimentID,false);err!=nil{return err};if err:=integrationGrantLimit(tx,id,grant.ExperimentID);err!=nil{return err}
  if err:=integrationAudit(tx,p,id,"integration.grant.put",grant.ExperimentID);err!=nil{return err}
  row:=MLflowIntegrationGrantRecord{IntegrationID:id,ExperimentID:grant.ExperimentID,PermissionsJSON:string(encoded),CreatedAt:grant.CreatedAt.UTC()}
  return tx.Clauses(clause.OnConflict{Columns:[]clause.Column{{Name:"integration_id"},{Name:"experiment_id"}},DoUpdates:clause.Assignments(map[string]any{"permissions":string(encoded),"revoked_at":nil})}).Create(&row).Error
 })
}
func integrationGrantLimit(tx *gorm.DB,id,experimentID string)error{
 var exists int64;if err:=tx.Model(&MLflowIntegrationGrantRecord{}).Where("integration_id = ? AND experiment_id = ?",id,experimentID).Count(&exists).Error;err!=nil{return err};if exists>0{return nil};var count int64;if err:=tx.Model(&MLflowIntegrationGrantRecord{}).Where("integration_id = ?",id).Count(&count).Error;err!=nil{return err};if count>=100{return integrations.ErrLimit};return nil
}
func(s *MLflowIntegrationStore)RevokeGrant(ctx context.Context,p auth.Principal,id,experimentID string,now time.Time)error{
 if !integrations.ValidID(experimentID){return integrations.ErrNotFound};return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB)error{
  if _,err:=managedIntegration(tx,p,id,true,true);err!=nil{return err};var row MLflowIntegrationGrantRecord;if err:=tx.Where("integration_id = ? AND experiment_id = ?",id,experimentID).First(&row).Error;err!=nil{return integrationError(err)}
  if err:=integrationAudit(tx,p,id,"integration.grant.revoke",experimentID);err!=nil{return err};return tx.Model(&MLflowIntegrationGrantRecord{}).Where("integration_id = ? AND experiment_id = ? AND revoked_at IS NULL",id,experimentID).Update("revoked_at",now.UTC()).Error
 })
}
func(s *MLflowIntegrationStore)GrantCreated(ctx context.Context,p auth.Principal,experimentID string)error{
 permissions,err:=integrations.PermissionsFromScopes(p.Scopes);if err!=nil{return err};if !integrations.HasPermission(permissions,"write"){return integrations.ErrNotFound};encoded,err:=json.Marshal(permissions);if err!=nil{return err}
 return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB)error{
  identity,err:=resolveIntegration(tx,p,true);if err!=nil{return err};if !identity.AllowCreateExperiments{return integrations.ErrNotFound};if err:=integrationExperiment(tx,identity,experimentID,true);err!=nil{return err};if err:=integrationGrantLimit(tx,identity.ID,experimentID);err!=nil{return err}
  // Both active grants and revoked tombstones survive an idempotent create retry.
  var existing MLflowIntegrationGrantRecord;err:=tx.Where("integration_id = ? AND experiment_id = ?",identity.ID,experimentID).First(&existing).Error;if err==nil{if existing.RevokedAt!=nil{return integrations.ErrNotFound};return nil};if !errors.Is(err,gorm.ErrRecordNotFound){return err}
  if err:=integrationAudit(tx,p,identity.ID,"integration.grant.created",experimentID);err!=nil{return err}
  return tx.Clauses(clause.OnConflict{DoNothing:true}).Create(&MLflowIntegrationGrantRecord{IntegrationID:identity.ID,ExperimentID:experimentID,PermissionsJSON:string(encoded),CreatedAt:time.Now().UTC()}).Error
 })
}
