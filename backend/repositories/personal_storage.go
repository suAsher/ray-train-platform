package repositories
import(
 "context"
 "errors"
 "time"
 ps "ray-train-platform-backend/personalstorage"
 "gorm.io/gorm"
 "gorm.io/gorm/clause"
)
type PersonalStorageRepository struct{db *gorm.DB}
type personalStorageRecord struct{
 OwnerID string `gorm:"primaryKey"`
 ID string `gorm:"uniqueIndex"`
 Username string `gorm:"uniqueIndex"`
 SecretName string `gorm:"uniqueIndex"`
 PublicKey,Fingerprint,State string
 Generation int64
 VerifiedAt *time.Time
 CreatedAt,UpdatedAt time.Time
}
func(personalStorageRecord)TableName()string{return "wellspiking_personal_bindings"}
func NewPersonalStorageRepository(database *gorm.DB)*PersonalStorageRepository{return &PersonalStorageRepository{db:database}}
func personalBinding(row personalStorageRecord)ps.Binding{return ps.Binding{ID:row.ID,OwnerID:row.OwnerID,Username:row.Username,SecretName:row.SecretName,PublicKey:row.PublicKey,Fingerprint:row.Fingerprint,State:row.State,Generation:row.Generation,VerifiedAt:row.VerifiedAt}}
func(r *PersonalStorageRepository)Get(ctx context.Context,ownerID string)(ps.Binding,error){
 if r==nil||r.db==nil||ownerID==""{return ps.Binding{},ps.ErrInvalid};var row personalStorageRecord
 if err:=r.db.WithContext(ctx).Where("owner_id = ?",ownerID).First(&row).Error;err!=nil{if errors.Is(err,gorm.ErrRecordNotFound){return ps.Binding{},ps.ErrNotFound};return ps.Binding{},ps.ErrUnavailable};return personalBinding(row),nil
}
func(r *PersonalStorageRepository)Create(ctx context.Context,b ps.Binding)(ps.Binding,error){
 if r==nil||r.db==nil||ps.ValidateOwner(ps.Owner{ID:b.OwnerID,Username:b.Username})!=nil||b.ID==""||b.SecretName==""||b.PublicKey==""||b.Fingerprint==""||b.Generation!=1||b.State!=ps.StatePending{return ps.Binding{},ps.ErrInvalid}
 row:=personalStorageRecord{OwnerID:b.OwnerID,ID:b.ID,Username:b.Username,SecretName:b.SecretName,PublicKey:b.PublicKey,Fingerprint:b.Fingerprint,Generation:b.Generation,State:b.State}
 if err:=r.db.WithContext(ctx).Clauses(clause.OnConflict{Columns:[]clause.Column{{Name:"owner_id"}},DoNothing:true}).Create(&row).Error;err!=nil{return ps.Binding{},ps.ErrConflict};return r.Get(ctx,b.OwnerID)
}
func(r *PersonalStorageRepository)MarkVerified(ctx context.Context,owner,id string,generation int64)(ps.Binding,error){
 if r==nil||r.db==nil||owner==""||id==""||generation!=1{return ps.Binding{},ps.ErrConflict};now:=time.Now().UTC()
 result:=r.db.WithContext(ctx).Model(&personalStorageRecord{}).Where("owner_id = ? AND id = ? AND generation = ? AND state IN ?",owner,id,generation,[]string{ps.StatePending,ps.StateReady}).Updates(map[string]any{"state":ps.StateReady,"verified_at":now,"updated_at":now})
 if result.Error!=nil{return ps.Binding{},ps.ErrUnavailable};if result.RowsAffected!=1{return ps.Binding{},ps.ErrConflict};return r.Get(ctx,owner)
}
var _ ps.Repository=(*PersonalStorageRepository)(nil)
