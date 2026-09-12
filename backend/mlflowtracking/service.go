package mlflowtracking

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

type Service struct {store Store; provider Provider; key []byte; newID func()(string,error); now func()time.Time}

func New(store Store,provider Provider,options Options)*Service {
	if options.NewID==nil {options.NewID=secureID}
	if options.Now==nil {options.Now=time.Now}
	return &Service{store:store,provider:provider,key:append([]byte(nil),options.CursorKey...),newID:options.NewID,now:options.Now}
}

func secureID()(string,error) {var data [16]byte;if _,err:=rand.Read(data[:]);err!=nil {return "",err};return hex.EncodeToString(data[:]),nil}
func keyDigest(value string)string {digest:=sha256.Sum256([]byte(value));return hex.EncodeToString(digest[:])}

func(s *Service) ready(actor Actor)error {
	if !validActor(actor) {return ErrInvalid}
	if s==nil||s.store==nil||s.provider==nil||len(s.key)<32 {return ErrUnavailable}
	return nil
}

func(s *Service) CreateExperiment(ctx context.Context,actor Actor,key,name string)(Experiment,error) {
	if err:=s.ready(actor);err!=nil {return Experiment{},err}
	if !validKey(key)||!validName(name) {return Experiment{},ErrInvalid}
	id,err:=s.newID();if err!=nil||!validID(id) {return Experiment{},ErrUnavailable}
	now:=s.now().UTC()
	record,claimed,err:=s.store.ReserveExperiment(ctx,Experiment{ID:id,TenantID:actor.TenantID,UserID:actor.UserID,IdempotencyHash:keyDigest(key),Name:name,State:"PENDING",CreatedAt:now,UpdatedAt:now})
	if err!=nil {return Experiment{},err}
	if record.Name!=name {return Experiment{},ErrConflict}
	if record.State=="READY" {return record,nil}
	ctx,cancel:=context.WithTimeout(ctx,20*time.Second);defer cancel()
	var upstream string
	if claimed {upstream,err=s.provider.CreateExperiment(ctx,record.ID)} else {
		var found bool;upstream,found,err=s.provider.FindExperiment(ctx,record.ID)
		if err==nil&&!found {return record,ErrPending}
	}
	if err!=nil||upstream=="" {return record,ErrPending}
	return s.store.CompleteExperiment(ctx,actor,record.ID,upstream)
}

func(s *Service) CreateRun(ctx context.Context,actor Actor,experimentID,key,name string)(Run,error) {
	if err:=s.ready(actor);err!=nil {return Run{},err}
	if !validID(experimentID)||!validKey(key)||!validName(name) {return Run{},ErrInvalid}
	experiment,err:=s.store.GetExperiment(ctx,actor,experimentID);if err!=nil {return Run{},err}
	if experiment.State!="READY" {return Run{},ErrPending}
	id,err:=s.newID();if err!=nil||!validID(id) {return Run{},ErrUnavailable}
	now:=s.now().UTC()
	record,claimed,err:=s.store.ReserveRun(ctx,Run{ID:id,ExperimentID:experimentID,TenantID:actor.TenantID,UserID:actor.UserID,IdempotencyHash:keyDigest(key),Name:name,State:"PENDING",StartTimeMS:now.UnixMilli(),CreatedAt:now,UpdatedAt:now})
	if err!=nil {return Run{},err}
	if record.Name!=name||record.ExperimentID!=experimentID {return Run{},ErrConflict}
	if record.State!="PENDING" {return record,nil}
	ctx,cancel:=context.WithTimeout(ctx,20*time.Second);defer cancel()
	var upstream string
	if claimed {upstream,err=s.provider.CreateRun(ctx,experiment.UpstreamID,record.ID,name)} else {
		var found bool;upstream,found,err=s.provider.FindRun(ctx,experiment.UpstreamID,record.ID)
		if err==nil&&!found {return record,ErrPending}
	}
	if err!=nil||!validID(upstream) {return record,ErrPending}
	return s.store.CompleteRun(ctx,actor,record.ID,upstream)
}

func(s *Service) GetRun(ctx context.Context,actor Actor,id string)(RunDetail,error) {
	if err:=s.ready(actor);err!=nil {return RunDetail{},err}
	if !validID(id) {return RunDetail{},ErrInvalid}
	record,experiment,err:=s.runAndExperiment(ctx,actor,id);if err!=nil {return RunDetail{},err}
	if record.State=="PENDING" {return RunDetail{},ErrPending}
	ctx,cancel:=context.WithTimeout(ctx,20*time.Second);defer cancel()
	data,err:=s.provider.ReadRun(ctx,experiment.UpstreamID,record.UpstreamID,record.ID)
	if err!=nil {return RunDetail{},err}
	return RunDetail{Run:record,Latest:data.Latest,Params:data.Params,Series:data.Series},nil
}

func(s *Service) runAndExperiment(ctx context.Context,actor Actor,id string)(Run,Experiment,error) {
	record,err:=s.store.GetRun(ctx,actor,id);if err!=nil {return Run{},Experiment{},err}
	experiment,err:=s.store.GetExperiment(ctx,actor,record.ExperimentID)
	if err!=nil {return Run{},Experiment{},err}
	if experiment.State!="READY" {return Run{},Experiment{},ErrPending}
	return record,experiment,nil
}

func(s *Service) LogRun(ctx context.Context,actor Actor,id string,batch Batch)error {
	if err:=s.ready(actor);err!=nil {return err};if !validID(id) {return ErrInvalid}
	if err:=batch.Validate();err!=nil {return err}
	_,experiment,err:=s.runAndExperiment(ctx,actor,id);if err!=nil {return err}
	leaseID,err:=s.newID();if err!=nil {return ErrUnavailable}
	now:=s.now().UTC()
	record,err:=s.store.ClaimRunLease(ctx,actor,id,leaseID,"",now,now.Add(time.Minute),0);if err!=nil {return err}
	callCtx,cancel:=context.WithTimeout(ctx,20*time.Second)
	callErr:=s.provider.LogRun(callCtx,experiment.UpstreamID,record.UpstreamID,record.ID,batch);cancel()
	_,releaseErr:=s.release(ctx,actor,id,leaseID,"")
	if releaseErr!=nil {return fmt.Errorf("%w: mutation outcome could not be persisted",ErrUnavailable)}
	return callErr
}

func(s *Service) FinishRun(ctx context.Context,actor Actor,id,status string)(Run,error) {
	if err:=s.ready(actor);err!=nil {return Run{},err}
	return s.FinishRunAt(ctx,actor,id,status,s.now().UTC().UnixMilli())
}

// FinishRunAt records the first requested end time as durable intent. A retry
// may supply a later clock value, but never changes the stored first intent.
func(s *Service) FinishRunAt(ctx context.Context,actor Actor,id,status string,endTimeMS int64)(Run,error) {
	if err:=s.ready(actor);err!=nil {return Run{},err}
	if !validID(id)||!terminal(status)||endTimeMS<0||endTimeMS>253402300799999 {return Run{},ErrInvalid}
	before,experiment,err:=s.runAndExperiment(ctx,actor,id);if err!=nil {return Run{},err}
	if endTimeMS<before.StartTimeMS{return Run{},ErrInvalid}
	leaseID,err:=s.newID();if err!=nil {return Run{},ErrUnavailable};now:=s.now().UTC()
	record,err:=s.store.ClaimRunLease(ctx,actor,id,leaseID,status,now,now.Add(time.Minute),endTimeMS);if err!=nil {return Run{},err}
	if record.State==status {return record,nil}
	callCtx,cancel:=context.WithTimeout(ctx,20*time.Second)
	callErr:=s.provider.FinishRun(callCtx,experiment.UpstreamID,record.UpstreamID,record.ID,status,record.EndTimeMS)
	finishedStatus:=status;if callErr!=nil {finishedStatus=""}
	if errors.Is(callErr,ErrConflict){
		snapshot,readErr:=s.provider.ReadRun(callCtx,experiment.UpstreamID,record.UpstreamID,record.ID)
		if readErr==nil&&terminal(snapshot.Status){finishedStatus=snapshot.Status}
	}
	cancel()
	updated,releaseErr:=s.release(ctx,actor,id,leaseID,finishedStatus)
	if releaseErr!=nil {return record,ErrPending}
	if errors.Is(callErr,ErrConflict)&&finishedStatus!=""{return updated,ErrConflict}
	if callErr!=nil {return updated,ErrPending}
	return updated,nil
}

func(s *Service) release(ctx context.Context,actor Actor,id,leaseID,finish string)(Run,error) {
	releaseCtx,cancel:=context.WithTimeout(context.WithoutCancel(ctx),3*time.Second);defer cancel()
	return s.store.ReleaseRunLease(releaseCtx,actor,id,leaseID,finish,s.now().UTC())
}

func terminal(status string)bool {return status=="FINISHED"||status=="FAILED"||status=="KILLED"}
