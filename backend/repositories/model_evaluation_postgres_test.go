package repositories

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	databasepkg "ray-train-platform-backend/db"
	me "ray-train-platform-backend/modelevaluation"
	ml "ray-train-platform-backend/modellifecycle"
	"ray-train-platform-backend/domain"
)

func evaluationPostgresStores(t *testing.T, upgrade bool) (*ModelEvaluationRepository, *ModelEvaluationRepository) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" { t.Skip("POSTGRES_TEST_DSN is not set") }
	admin := openArtifactPostgresConnection(t, dsn)
	schema := fmt.Sprintf("model_evaluation_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil { t.Fatal(err) }
	t.Cleanup(func() { if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil { t.Error(err) } })
	a, b := openArtifactPostgresConnection(t,dsn), openArtifactPostgresConnection(t,dsn)
	for _, db := range []*gorm.DB{a,b} { if err:=db.Exec("SET search_path TO " + schema).Error;err!=nil{t.Fatal(err)} }
	if upgrade {
		if err:=a.Exec("CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)").Error;err!=nil{t.Fatal(err)}
		files,err:=os.ReadDir("../db/migrations");if err!=nil{t.Fatal(err)}
		for _,file:=range files {
			if !strings.HasSuffix(file.Name(),".up.sql"){continue}
			version,err:=strconv.Atoi(file.Name()[:4]);if err!=nil{t.Fatal(err)}
			if version>49{continue}
			raw,err:=os.ReadFile(filepath.Join("../db/migrations",file.Name()));if err!=nil{t.Fatal(err)}
			if err:=a.Transaction(func(tx *gorm.DB)error{if err:=tx.Exec(string(raw)).Error;err!=nil{return err};return tx.Exec("INSERT INTO schema_migrations(version) VALUES (?)",version).Error});err!=nil{t.Fatalf("apply historical migration %d: %v",version,err)}
		}
		old:=ml.Model{ID:"schema49-preserved",Name:"existing model",OwnerID:"stable-owner",TenantID:"previous-team",Revision:1}
		if err:=a.Create(&old).Error;err!=nil{t.Fatal(err)}
	}
	for i:=0;i<2;i++{if err:=databasepkg.ApplyMigrations(a);err!=nil{t.Fatal(err)}}
	var maxVersion int
	if err:=a.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&maxVersion).Error;err!=nil||maxVersion!=50{t.Fatalf("schema version=%d error=%v",maxVersion,err)}
	if upgrade {
		var old ml.Model
		if err:=a.Where("id = ?","schema49-preserved").First(&old).Error;err!=nil||old.Name!="existing model"{t.Fatalf("schema49 data changed: %+v %v",old,err)}
	}
	return NewModelEvaluationRepository(a),NewModelEvaluationRepository(b)
}

func TestModelEvaluationPostgresSchema49UpgradeReservationACL(t *testing.T) {
	a,_:=evaluationPostgresStores(t,true)
	runEvaluationReservationTests(t,a)
	var row modelEvaluationRecord
	if err:=a.db.Where("state = ?",me.Failed).First(&row).Error;err!=nil{t.Fatal(err)}
	for _,update:=range []map[string]any{{"job_id":"replace-job"},{"snapshot_json":"{}"},{"state":me.Creating,"finished_at":nil}}{
		if err:=a.db.Model(&modelEvaluationRecord{}).Where("id = ?",row.ID).Updates(update).Error;err==nil{t.Fatalf("immutable evaluation mutation accepted: %v",update)}
	}
	var evaluator modelEvaluatorRecord
	if err:=a.db.First(&evaluator).Error;err!=nil{t.Fatal(err)}
	if err:=a.db.Model(&evaluator).Update("snapshot_json","{}").Error;err==nil{t.Fatal("evaluator executable changed")}
}

func TestModelEvaluationPostgresReportIdentityTerminalAndFinite(t *testing.T) {
	a,_:=evaluationPostgresStores(t,false)
	runEvaluationReportTests(t,a)
	var row modelEvaluationRecord
	if err:=a.db.Where("state = ?",me.Succeeded).First(&row).Error;err!=nil{t.Fatal(err)}
	if err:=a.db.Model(&row).Update("report_json","{}").Error;err==nil{t.Fatal("immutable accepted report changed")}
}

func TestModelEvaluationPostgresConcurrentReservationsAndQuota(t *testing.T) {
	a,b:=evaluationPostgresStores(t,false)
	base:=seedEvaluationSources(t,a)
	ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second);defer cancel()
	type result struct{e me.Evaluation;created bool;err error}
	for _,different:=range []bool{false,true}{
		start:=make(chan struct{});results:=make(chan result,2)
		for i,s:=range []*ModelEvaluationRepository{a,b}{go func(i int,s *ModelEvaluationRepository){
			<-start;e:=base;e.ID=uuid.NewString();e.JobID="job-"+uuid.NewString();e.IdempotencyKey=fmt.Sprint("race-",different)
			if different&&i==1{e.RequestSHA256=strings.Repeat("f",64)}
			got,created,err:=s.ReserveEvaluation(ctx,e);results<-result{got,created,err}
		}(i,s)}
		close(start);one,two:=<-results,<-results
		if !different{if one.err!=nil||two.err!=nil||one.e.ID!=two.e.ID||one.created==two.created{t.Fatalf("duplicate reservation: %+v %+v",one,two)}}else if !(one.err==nil&&errors.Is(two.err,me.ErrConflict)||two.err==nil&&errors.Is(one.err,me.ErrConflict)){t.Fatalf("different request race: %+v %+v",one,two)}
	}
	for i:=2;i<maxActiveModelEvaluations-1;i++{reserveEvaluationFixture(t,a,base,fmt.Sprint("quota-",i))}
	start:=make(chan struct{});results:=make(chan error,2)
	for i,s:=range []*ModelEvaluationRepository{a,b}{go func(i int,s *ModelEvaluationRepository){
		<-start;e:=base;e.ID=uuid.NewString();e.JobID="job-"+uuid.NewString();e.IdempotencyKey=fmt.Sprint("quota-race-",i)
		_,_,err:=s.ReserveEvaluation(ctx,e);results<-err
	}(i,s)}
	close(start);one,two:=<-results,<-results
	if !(one==nil&&errors.Is(two,me.ErrQuota)||two==nil&&errors.Is(one,me.ErrQuota)){t.Fatalf("active quota race: %v %v",one,two)}
	var count int64
	if err:=a.db.Model(&modelEvaluationRecord{}).Where("owner_id = ? AND state = ?",base.OwnerID,me.Creating).Count(&count).Error;err!=nil||count!=maxActiveModelEvaluations{t.Fatalf("active rows=%d error=%v",count,err)}
}

func TestModelEvaluationPostgresCanonicalConfigRoundTrip(t *testing.T) {
	s,_:=evaluationPostgresStores(t,false)
	base:=seedEvaluationSources(t,s)
	canonical,hash,err:=me.CanonicalConfig([]byte(`{"z":{"threshold":1.50,"seed":9007199254740993},"a":[100,0,{"nested":true}]}`))
	if err!=nil{t.Fatal(err)}
	base.Config,base.ConfigSHA256=canonical,hash
	e:=reserveEvaluationFixture(t,s,base,"config-roundtrip")
	for _,read:=range []func()(me.Evaluation,error){
		func()(me.Evaluation,error){return s.GetEvaluationByJobID(context.Background(),e.JobID)},
		func()(me.Evaluation,error){return s.FindEvaluationRequest(context.Background(),e.TenantID,e.OwnerID,e.IdempotencyKey)},
	}{
		got,err:=read();if err!=nil{t.Fatal(err)}
		sum:=sha256.Sum256(got.Config)
		if string(got.Config)!=string(canonical)||hex.EncodeToString(sum[:])!=hash||!strings.Contains(string(got.Config),"9007199254740993"){t.Fatalf("JSONB changed runtime config bytes: %s hash=%s",got.Config,got.ConfigSHA256)}
	}
}

func evaluationSubmissionTestJob(e me.Evaluation) domain.TrainingJob {
	job:=testJob()
	job.ID,job.TenantID,job.UserID=e.JobID,e.TenantID,e.OwnerID
	job.SubmissionOrigin,job.ExternalSubmissionID=domain.SubmissionOriginEvaluation,e.ID
	job.Spec.Name=e.ID
	job.Spec.TrainingEngine=domain.TrainingEngineRayTrain
	job.Spec.RayVersion=domain.RayVersionCanary
	return job
}

func TestModelEvaluationPostgresCancelVersusJobCreation(t *testing.T) {
	a,b:=evaluationPostgresStores(t,false)
	base:=seedEvaluationSources(t,a)
	ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second);defer cancel()
	// Deterministic cancellation first: no job, outbox, or idempotency record
	// may appear after the reservation has released its active slot.
	first:=reserveEvaluationFixture(t,a,base,"cancel-first")
	for i:=0;i<2;i++{done,err:=a.CancelEvaluationReservation(ctx,first.ID);if err!=nil||!done{t.Fatalf("cancel retry %v %v",done,err)}}
	job:=evaluationSubmissionTestJob(first)
	if err:=NewGormRepository(b.db).Create(ctx,&job,"evaluation:"+first.ID);!errors.Is(err,me.ErrConflict){t.Fatalf("late job after cancel: %v",err)}
	for _,table:=range []string{"training_jobs","outbox_events","idempotency_keys"}{
		var count int64
		if err:=a.db.Table(table).Count(&count).Error;err!=nil||count!=0{t.Fatalf("cancel left %s rows=%d error=%v",table,count,err)}
	}
	for i:=0;i<8;i++{
		e:=reserveEvaluationFixture(t,a,base,fmt.Sprint("cancel-race-",i))
		start:=make(chan struct{});created:=make(chan error,1)
		type cancellation struct{done bool;err error};cancelled:=make(chan cancellation,1)
		go func(){<-start;job:=evaluationSubmissionTestJob(e);created<-NewGormRepository(a.db).Create(ctx,&job,"evaluation:"+e.ID)}()
		go func(){<-start;done,err:=b.CancelEvaluationReservation(ctx,e.ID);cancelled<-cancellation{done,err}}()
		close(start);createErr,cancelResult:=<-created,<-cancelled
		if cancelResult.err!=nil{t.Fatal(cancelResult.err)}
		var jobs int64
		if err:=a.db.Model(&JobRecord{}).Where("id = ?",e.JobID).Count(&jobs).Error;err!=nil{t.Fatal(err)}
		current,err:=a.GetEvaluation(ctx,e.ID,e.TenantID,false);if err!=nil{t.Fatal(err)}
		if cancelResult.done{
			if jobs!=0||current.State!=me.Cancelled||!errors.Is(createErr,me.ErrConflict){t.Fatalf("cancel won but late job appeared: jobs=%d state=%s create=%v",jobs,current.State,createErr)}
		}else{
			if createErr!=nil||jobs!=1||current.State!=me.Creating{t.Fatalf("create won but reservation changed: jobs=%d state=%s create=%v",jobs,current.State,createErr)}
			job:=evaluationSubmissionTestJob(e)
			var conflict *IdempotencyConflictError
			if err:=NewGormRepository(a.db).Create(ctx,&job,"evaluation:"+e.ID);!errors.As(err,&conflict)||conflict.JobID!=e.JobID{t.Fatalf("existing job replay: %v",err)}
		}
	}
}
