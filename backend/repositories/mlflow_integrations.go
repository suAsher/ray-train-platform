package repositories

import (
 "context"
 "crypto/rand"
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

type MLflowIntegrationRecord struct {
 ID string `gorm:"primaryKey"`
 TenantID string `gorm:"index"`
 OwnerUserID string
 Name string
 AllowCreateExperiments bool
 RevokedAt *time.Time
 CreatedAt time.Time
}
func(MLflowIntegrationRecord)TableName()string{return "mlflow_integrations"}
type MLflowIntegrationTokenRecord struct {
 ID string `gorm:"primaryKey"`
 PublicID string `gorm:"uniqueIndex"`
 IntegrationID string `gorm:"index"`
 TokenDigest string
 ScopesJSON string `gorm:"column:scopes;type:jsonb"`
 ExpiresAt time.Time
 LastUsedAt *time.Time
 RevokedAt *time.Time
 CreatedAt time.Time
}
func(MLflowIntegrationTokenRecord)TableName()string{return "mlflow_integration_tokens"}
type MLflowIntegrationGrantRecord struct {
 IntegrationID string `gorm:"primaryKey"`
 ExperimentID string `gorm:"primaryKey"`
 PermissionsJSON string `gorm:"column:permissions;type:jsonb"`
 RevokedAt *time.Time
 CreatedAt time.Time
}
func(MLflowIntegrationGrantRecord)TableName()string{return "mlflow_integration_grants"}
type MLflowIntegrationAuditRecord struct {
 ID string `gorm:"primaryKey"`
 TenantID string
 ActorID string
 IntegrationID string
 Action string
 ResourceID string
 CreatedAt time.Time
}
func(MLflowIntegrationAuditRecord)TableName()string{return "mlflow_integration_audit"}

type MLflowIntegrationStore struct{repo *GormRepository;db *gorm.DB}
func NewMLflowIntegrationStore(repo *GormRepository)*MLflowIntegrationStore{return &MLflowIntegrationStore{repo:repo,db:repo.db}}
var _ integrations.ManagementStore=(*MLflowIntegrationStore)(nil)

func integrationValue(r MLflowIntegrationRecord)integrations.Identity{return integrations.Identity{ID:r.ID,TenantID:r.TenantID,OwnerUserID:r.OwnerUserID,Name:r.Name,AllowCreateExperiments:r.AllowCreateExperiments,RevokedAt:r.RevokedAt,CreatedAt:r.CreatedAt}}
func integrationError(err error)error{if errors.Is(err,gorm.ErrRecordNotFound)||errors.Is(err,ErrTenantRetirementBlocked)||errors.Is(err,ErrMembershipNotFound){return integrations.ErrNotFound};return err}
func integrationOwner(tx *gorm.DB,tenantID,ownerID string,lock bool)error{
 if tenantID==""||ownerID==""{return integrations.ErrNotFound}
 if err:=requireActiveIdentityTenant(tx,tenantID,lock);err!=nil{return integrationError(err)}
 query:=tx;if lock{query=query.Clauses(clause.Locking{Strength:"UPDATE"})};var owner LocalUserRecord
 if err:=query.Where("id = ? AND disabled = FALSE AND decommissioned_at IS NULL",ownerID).First(&owner).Error;err!=nil{return integrationError(err)}
 var membership TenantMembershipRecord
 if err:=query.Where("identity_id = ? AND tenant_id = ? AND status = ?",ownerID,tenantID,domain.MembershipStatusActive).First(&membership).Error;err!=nil{return integrationError(err)}
 return nil
}
func managedIntegration(tx *gorm.DB,p auth.Principal,id string,lock,allowRevoked bool)(MLflowIntegrationRecord,error){
 if !auth.IsInteractiveAuthType(p.AuthType)||p.IntegrationID!=""||!integrations.ValidID(id){return MLflowIntegrationRecord{},integrations.ErrNotFound}
 if err:=integrationOwner(tx,p.TenantID,p.Subject,lock);err!=nil{return MLflowIntegrationRecord{},err}
 query:=tx;if lock{query=query.Clauses(clause.Locking{Strength:"UPDATE"})};if !allowRevoked{query=query.Where("revoked_at IS NULL")}
 var row MLflowIntegrationRecord;err:=query.Where("id = ? AND tenant_id = ? AND owner_user_id = ?",id,p.TenantID,p.Subject).First(&row).Error;return row,integrationError(err)
}
func resolveIntegration(tx *gorm.DB,p auth.Principal,lock bool)(MLflowIntegrationRecord,error){
 if p.AuthType!=auth.AuthTypePAT||!integrations.ValidID(p.IntegrationID)||p.Subject!="integration:"+p.IntegrationID{return MLflowIntegrationRecord{},integrations.ErrNotFound}
 var row MLflowIntegrationRecord
 if err:=tx.Where("id = ? AND tenant_id = ? AND revoked_at IS NULL",p.IntegrationID,p.TenantID).First(&row).Error;err!=nil{return row,integrationError(err)}
 if err:=integrationOwner(tx,row.TenantID,row.OwnerUserID,lock);err!=nil{return row,err}
 if lock{if err:=tx.Clauses(clause.Locking{Strength:"UPDATE"}).Where("id = ? AND revoked_at IS NULL",row.ID).First(&row).Error;err!=nil{return row,integrationError(err)}}
 return row,nil
}
func integrationAudit(tx *gorm.DB,p auth.Principal,integrationID,action,resource string)error{
 id:=make([]byte,16);if _,err:=rand.Read(id);err!=nil{return err}
 return tx.Create(&MLflowIntegrationAuditRecord{ID:hex.EncodeToString(id),TenantID:p.TenantID,ActorID:p.Subject,IntegrationID:integrationID,Action:action,ResourceID:resource,CreatedAt:time.Now().UTC()}).Error
}
func(s *MLflowIntegrationStore)Resolve(ctx context.Context,p auth.Principal)(integrations.Identity,error){r,err:=resolveIntegration(s.db.WithContext(ctx),p,false);return integrationValue(r),err}
func(s *MLflowIntegrationStore)Authorize(ctx context.Context,p auth.Principal,experimentID,permission string)(integrations.Identity,error){
 tx:=s.db.WithContext(ctx);r,err:=resolveIntegration(tx,p,false);if err!=nil{return integrations.Identity{},err}
 if err:=integrationExperiment(tx,r,experimentID,false);err!=nil{return integrations.Identity{},err}
 var grant MLflowIntegrationGrantRecord
 if err:=tx.Where("integration_id = ? AND experiment_id = ? AND revoked_at IS NULL",r.ID,experimentID).First(&grant).Error;err!=nil{return integrations.Identity{},integrationError(err)}
 permissions,err:=decodeIntegrationPermissions(grant.PermissionsJSON);if err!=nil{return integrations.Identity{},err};if !integrations.HasPermission(permissions,permission){return integrations.Identity{},integrations.ErrNotFound}
 return integrationValue(r),nil
}
func integrationExperiment(tx *gorm.DB,r MLflowIntegrationRecord,id string,allowPending bool)error{
 if !integrations.ValidID(id){return integrations.ErrNotFound};states:=[]string{"READY"};if allowPending{states=append(states,"PENDING")}
 var experiment MLflowTrackingExperimentRecord
 return integrationError(tx.Where("id = ? AND tenant_id = ? AND user_id = ? AND state IN ?",id,r.TenantID,r.OwnerUserID,states).First(&experiment).Error)
}
func decodeIntegrationPermissions(raw string)([]string,error){var permissions []string;if err:=json.Unmarshal([]byte(raw),&permissions);err!=nil{return nil,err};return integrations.NormalizePermissions(permissions)}
func(s *MLflowIntegrationStore)ListGrantedExperimentIDs(ctx context.Context,p auth.Principal,permission string)([]string,error){
 tx:=s.db.WithContext(ctx);r,err:=resolveIntegration(tx,p,false);if err!=nil{return nil,err};var rows []MLflowIntegrationGrantRecord
 if err:=tx.Table("mlflow_integration_grants AS g").Select("g.*").Joins("JOIN mlflow_tracking_experiments AS e ON e.id=g.experiment_id AND e.tenant_id=? AND e.user_id=? AND e.state='READY'",r.TenantID,r.OwnerUserID).Where("g.integration_id = ? AND g.revoked_at IS NULL",r.ID).Order("g.experiment_id ASC").Limit(100).Scan(&rows).Error;err!=nil{return nil,err}
 ids:=make([]string,0,len(rows));for _,row:=range rows{permissions,err:=decodeIntegrationPermissions(row.PermissionsJSON);if err!=nil{return nil,err};if integrations.HasPermission(permissions,permission){ids=append(ids,row.ExperimentID)}};return ids,nil
}
