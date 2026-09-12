package repositories

import (
 "context"
 "errors"
 "encoding/json"
 "time"
 "strings"

 "github.com/google/uuid"
 "gorm.io/gorm"
 "gorm.io/gorm/clause"
 ml "ray-train-platform-backend/modellifecycle"
)
func (s *ModelLifecycleStore) ReserveVersion(ctx context.Context,v ml.Version)(ml.Version,error) {
 if strings.TrimSpace(v.SourceETag)=="" || v.SizeBytes<1 || v.SizeBytes>ml.MaxFileSize || v.CreatorID=="" || v.IdempotencyKey=="" || v.RequestSHA256=="" {return ml.Version{},ml.ErrInvalid}
 var result ml.Version
 err:=s.db.WithContext(ctx).Transaction(func(tx *gorm.DB)error {
  var m ml.Model
  if err:=tx.Clauses(clause.Locking{Strength:"UPDATE"}).Where("id = ?",v.ModelID).First(&m).Error;err!=nil{return modelReadError(err)}
  // All models of a stable owner share one quota lock across backend replicas.
  if tx.Dialector.Name()=="postgres" {if err:=tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 734915))",m.OwnerID).Error;err!=nil{return err}}
  err:=tx.Where("model_id = ? AND creator_id = ? AND idempotency_key = ?",v.ModelID,v.CreatorID,v.IdempotencyKey).First(&result).Error
  if err==nil {if result.RequestSHA256!=v.RequestSHA256{return ml.ErrConflict};return nil}
  if !errors.Is(err,gorm.ErrRecordNotFound){return err};if m.Archived{return ml.ErrConflict}
  var usage struct { Bytes int64; Pending int64 }
  if err:=tx.Raw(`SELECT COALESCE(SUM(v.size_bytes),0) AS bytes, COALESCE(SUM(CASE WHEN v.state IN ('PENDING','COPYING') THEN 1 ELSE 0 END),0) AS pending FROM model_versions v JOIN model_catalog m ON m.id = v.model_id WHERE m.owner_id = ?`,m.OwnerID).Scan(&usage).Error;err!=nil{return err}
  // Failed snapshots remain charged until a future safe object cleanup releases them.
  if usage.Bytes>ml.OwnerBudget-v.SizeBytes || usage.Pending>=ml.MaxPending{return ml.ErrQuota}
  var number int64;if err:=tx.Model(&ml.Version{}).Select("COALESCE(MAX(number),0)").Where("model_id = ?",v.ModelID).Scan(&number).Error;err!=nil{return err}
  v.ID=uuid.NewString();v.Number=number+1;v.State=ml.Pending;v.Revision=1;v.SHA256="";v.Error="";v.Parts=[]ml.Part{};v.LeaseID="";v.LeaseExpiresAt=nil;v.CreatedAt=time.Now().UTC();v.UpdatedAt=v.CreatedAt
  if err:=tx.Create(&v).Error;err!=nil{return err}
  if err:=writeModelAudit(tx,m.ID,v.ID,ml.Actor{ID:v.CreatorID,Name:v.CreatorName},"version.requested",map[string]any{"number":v.Number,"jobId":v.JobID,"sizeBytes":v.SizeBytes});err!=nil{return err}
  result=v;return nil
 });return result,err
}
func (s *ModelLifecycleStore) ClaimVersion(ctx context.Context,lease string,now,until time.Time)(ml.Version,error) {
 if lease=="" || !until.After(now){return ml.Version{},ml.ErrInvalid}
 // Commit expired-lease recovery even when no new work can be claimed.
 if err:=s.db.WithContext(ctx).Model(&ml.Version{}).Where("state = ? AND lease_expires_at <= ?",ml.Copying,now).Updates(map[string]any{"state":ml.Failed,"error":"Snapshot copy failed; register a new version to retry.","lease_id":"","lease_expires_at":nil,"revision":gorm.Expr("revision + 1"),"updated_at":now}).Error;err!=nil{return ml.Version{},err}
 var v ml.Version
 err:=s.db.WithContext(ctx).Transaction(func(tx *gorm.DB)error {
  if err:=tx.Clauses(clause.Locking{Strength:"UPDATE",Options:"SKIP LOCKED"}).Where("state = ?",ml.Pending).Order("created_at ASC, id ASC").First(&v).Error;err!=nil{return modelReadError(err)}
  res:=tx.Model(&ml.Version{}).Where("id = ? AND state = ?",v.ID,ml.Pending).Updates(map[string]any{"state":ml.Copying,"lease_id":lease,"lease_expires_at":until,"revision":gorm.Expr("revision + 1"),"updated_at":now})
  if res.Error!=nil{return res.Error};if res.RowsAffected!=1{return ml.ErrConflict}
  v.State=ml.Copying;v.LeaseID=lease;v.LeaseExpiresAt=&until;v.Revision++;v.UpdatedAt=now;return nil
 });return v,err
}
func (s *ModelLifecycleStore) RenewVersion(ctx context.Context,id,lease string,now,until time.Time)error {
 if lease=="" || !until.After(now){return ml.ErrInvalid}
 res:=s.db.WithContext(ctx).Model(&ml.Version{}).Where("id = ? AND state = ? AND lease_id = ? AND lease_expires_at > ?",id,ml.Copying,lease,now).Update("lease_expires_at",until)
 if res.Error!=nil{return res.Error};if res.RowsAffected!=1{return ml.ErrConflict};return nil
}
func (s *ModelLifecycleStore) FinishVersion(ctx context.Context,id,lease,state string,parts []ml.Part,hash string,now time.Time)error {
 if lease=="" || (state!=ml.Ready && state!=ml.Failed){return ml.ErrInvalid}
 updates:=map[string]any{"state":state,"lease_id":"","lease_expires_at":nil,"revision":gorm.Expr("revision + 1"),"updated_at":now}
 if state==ml.Ready {
  // Serialize through the field serializer rather than passing a Go slice to SQL.
  if len(hash)!=64 || len(parts)==0{return ml.ErrInvalid}
  raw,err:=json.Marshal(parts);if err!=nil{return err};updates["parts"]=string(raw);updates["sha256"]=hash;updates["error"]=""
 }else {updates["error"]="Snapshot copy failed; register a new version to retry."}
 res:=s.db.WithContext(ctx).Model(&ml.Version{}).Where("id = ? AND state = ? AND lease_id = ? AND lease_expires_at > ?",id,ml.Copying,lease,now).Updates(updates)
 if res.Error!=nil{return res.Error};if res.RowsAffected!=1{return ml.ErrConflict};return nil
}
