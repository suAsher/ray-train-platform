package storagesync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

func(m *Manager)Run(ctx context.Context){ticker:=time.NewTicker(m.options.ReconcileInterval);defer ticker.Stop();for{if err:=m.Reconcile(ctx);err!=nil&&ctx.Err()==nil{slog.Error("storage sync reconcile failed","error",err)};select{case<-ctx.Done():return;case<-ticker.C:}}}
// Reconcile must run only while the caller holds the Kubernetes leader Lease.
// PostgreSQL transactions additionally serialize decisions made by HTTP peers.
func(m *Manager)Reconcile(ctx context.Context)error{
	if m.jobs==nil{return errors.New("storage sync job client unavailable")}
	var failures []error
	if err:=m.schedule(ctx);err!=nil{failures=append(failures,err)}
	var previews []Preview;err:=m.repo.Transact(ctx,func(tx Tx)error{var err error;previews,err=tx.ListPreviews();return err});if err!=nil{return errors.Join(append(failures,err)...)}
	for _,p:=range previews{if p.State=="QUEUED"||p.State=="RUNNING"{if err:=m.reconcilePreview(ctx,p.ID);err!=nil{failures=append(failures,err)}}}
	runs,err:=m.repo.ListRuns(ctx,"");if err!=nil{return errors.Join(append(failures,err)...)}
	for _,r:=range runs{if r.Active()&&r.State!="PAUSED"{if err:=m.reconcileRun(ctx,r.ID);err!=nil{failures=append(failures,err)}}}
	return errors.Join(failures...)
}
func(m *Manager)schedule(ctx context.Context)error{return m.repo.Transact(ctx,func(tx Tx)error{
	plans,err:=tx.ListPlans();if err!=nil{return err}
	for _,plan:=range plans{
		if !plan.Enabled||plan.NextRunAt==nil||plan.NextRunAt.After(m.now()){continue};updated:=plan;actor:=plan.Owner;if actor==""{actor=plan.CreatedBy}
		if err:=m.authorize(ctx,actor);err!=nil{updated.Enabled=false;updated.NextRunAt=nil;updated.FailureReason="OWNER_AUTHORIZATION_REVOKED";updated.UpdatedAt=m.now();if err=tx.PutPlan(updated);err!=nil{return err};continue}
		runs,err:=tx.ListRuns(plan.ID);if err!=nil{return err};active:=false;for _,r:=range runs{if r.Active(){active=true}}
		updated.NextRunAt,err=plan.Config.Schedule.Next(m.now());if err!=nil{return err};updated.UpdatedAt=m.now()
		if active{updated.SkippedSchedules++;if err=tx.PutPlan(updated);err!=nil{return err};continue}
		resolved,err:=m.resolve(ctx,actor,plan.Config);if err!=nil{updated.FailureReason="SCHEDULED_RESOLUTION_FAILED";updated.SkippedSchedules++;if err=tx.PutPlan(updated);err!=nil{return err};continue}
		run:=m.newRun(plan,actor,resolved);run.Trigger="SCHEDULED";slot:=*plan.NextRunAt;run.ScheduledAt=&slot;run.IdempotencyKey=fmt.Sprintf("schedule:%d:%s",plan.Revision,slot.Format(time.RFC3339Nano));run.BaselineRef,err=baselineRef(tx,plan.ID,plan.Revision);if err!=nil{return err}
		if err=tx.AcquireLocks(run.ID,run.Attempt,LocksFor(resolved));err!=nil{if !errors.Is(err,ErrLocked){return err};updated.SkippedSchedules++;updated.FailureReason="SCHEDULED_PATH_LOCKED"}else{if err=tx.PutRun(run);err!=nil{return err};updated.FailureReason=""}
		if err=tx.PutPlan(updated);err!=nil{return err}
	}
	return nil
})}
func(m *Manager)reconcilePreview(ctx context.Context,id string)error{
	if err:=m.repo.Transact(ctx,func(tx Tx)error{p,err:=tx.GetPreview(id);if err!=nil{return err};if p.State!="QUEUED"{return nil};p.State="RUNNING";p.UpdatedAt=m.now();return tx.PutPreview(p)});err!=nil{return err}
	var externalErr error
	err:=m.repo.Transact(ctx,func(tx Tx)error{
		p,err:=tx.GetPreview(id);if err!=nil{return err};if p.State!="RUNNING"{return nil}
		if !p.ExpiresAt.After(m.now()){p.State="FAILED";p.FailureReason="PREVIEW_EXPIRED";p.UpdatedAt=m.now();return tx.PutPreview(p)}
		if err=m.authorize(ctx,p.Actor);err!=nil{p.State="FAILED";p.FailureReason="AUTHORIZATION_REVOKED";return tx.PutPreview(p)}
		var observed Observation
		if p.JobUID==""{observed,externalErr=m.jobs.Ensure(ctx,m.previewSpec(p))}else{observed,externalErr=m.jobs.Observe(ctx,p.ID,p.Attempt)}
		if externalErr!=nil{p.FailureReason="EXECUTOR_OBSERVATION_UNAVAILABLE";return tx.PutPreview(p)}
		if p.JobUID==""&&observed.Exists&&observed.JobUID!=""{p.JobUID=observed.JobUID}
		if observed.JobUID!=""&&p.JobUID!=observed.JobUID{p.FailureReason="EXECUTOR_UID_CHANGED";return tx.PutPreview(p)}
		if observed.Terminated&&observed.Exists&&p.JobUID!=""&&p.JobUID==observed.JobUID{
			if p.ReceiptState=="SUCCEEDED"{p.State="SUCCEEDED"}else{p.State="FAILED";if p.FailureReason==""{p.FailureReason="PREVIEW_RECEIPT_MISSING"}}
		}
		p.UpdatedAt=m.now();return tx.PutPreview(p)
	});return errors.Join(err,externalErr)
}
func(m *Manager)claimQueued(ctx context.Context,id string)error{return m.repo.Transact(ctx,func(tx Tx)error{
	r,err:=tx.GetRun(id);if err!=nil{return err};if r.State!="QUEUED"{return nil};runs,err:=tx.ListRuns("");if err!=nil{return err};active:=0;for _,other:=range runs{if other.ID!=id&&other.Active()&&other.State!="PAUSED"&&other.State!="QUEUED"{active++}};if active>=m.options.MaxActiveRuns{return nil}
	resolved,err:=m.resolve(ctx,runActor(r),r.Config);if err!=nil||resolutionDigest(resolved)!=r.ResolutionDigest{r.State="PAUSED";r.StopVerified=true;r.FailureReason="RESOLUTION_OR_AUTHORIZATION_CHANGED";if err=tx.ReleaseLocks(r.ID);err!=nil{return err};return tx.PutRun(r)}
	r.State="RUNNING";now:=m.now();r.UpdatedAt=now;if r.StartedAt==nil{r.StartedAt=&now};return tx.PutRun(r)
})}
func(m *Manager)reconcileRun(ctx context.Context,id string)error{
	// Commit the dispatch intent first. An Ensure response can be lost after the
	// Job was created; a later cancellation must never treat that as unstarted.
	if err:=m.claimQueued(ctx,id);err!=nil{return err};var externalErr error
	err:=m.repo.Transact(ctx,func(tx Tx)error{
		r,err:=tx.GetRun(id);if err!=nil{return err};if !r.Active()||r.State=="PAUSED"||r.State=="QUEUED"{return nil}
		if r.State=="RUNNING"{resolved,resolveErr:=m.resolve(ctx,runActor(r),r.Config);if resolveErr!=nil||resolutionDigest(resolved)!=r.ResolutionDigest{r.State="PAUSING";r.FailureReason="RESOLUTION_OR_AUTHORIZATION_CHANGED"}}
		var observed Observation
		if r.State=="RUNNING"&&r.JobUID==""{observed,externalErr=m.jobs.Ensure(ctx,m.runSpec(r))}else{observed,externalErr=m.jobs.Observe(ctx,r.ID,r.Attempt)}
		if externalErr!=nil{r.FailureReason="EXECUTOR_OBSERVATION_UNAVAILABLE";return tx.PutRun(r)}
		if r.JobUID==""&&observed.Exists&&observed.JobUID!=""{r.JobUID=observed.JobUID}
		if observed.JobUID!=""&&r.JobUID!=observed.JobUID{r.FailureReason="EXECUTOR_UID_CHANGED";return tx.PutRun(r)}
		if r.State=="PAUSING"||r.State=="CANCELLING"{if stopErr:=m.jobs.Stop(ctx,r.ID,r.Attempt);stopErr!=nil{externalErr=stopErr;r.FailureReason="EXECUTOR_STOP_UNCONFIRMED"}}
		stopped:=observed.Exists&&observed.Terminated&&r.JobUID!=""&&r.JobUID==observed.JobUID&&(r.Phase=="PREVIEW"||r.RequestsDrained)
		if stopped{return m.completeStopped(tx,r)}
		if !observed.Exists||observed.Terminated {r.FailureReason="WAITING_FOR_EXECUTOR_STOP_AND_REQUEST_DRAIN"}else if r.HeartbeatAt!=nil&&m.now().Sub(*r.HeartbeatAt)>30*time.Second{r.FailureReason="PROGRESS_STALE"}
		r.UpdatedAt=m.now();return tx.PutRun(r)
	});return errors.Join(err,externalErr)
}
func(m *Manager)completeStopped(tx Tx,r Run)error{
	r.StopVerified=true;r.UpdatedAt=m.now()
	switch r.State {
	case "PAUSING":r.State="PAUSED";if err:=tx.ReleaseLocks(r.ID);err!=nil{return err};return tx.PutRun(r)
	case "CANCELLING":r.State="CANCELLED"
	default:
		if r.Phase=="PREVIEW"&&r.ReceiptState=="SUCCEEDED"{
			r.Attempt++;r.Generation++;r.Sequence=0;r.JobUID="";r.WorkerID="";r.LastReportDigest="";r.RequestsDrained=false;r.StopVerified=false;r.ReceiptState="";r.Phase="TRANSFER";r.State="QUEUED";r.FailureReason=""
			if err:=tx.AcquireLocks(r.ID,r.Attempt,LocksFor(r.Resolved));err!=nil{return err};return tx.PutRun(r)
		}
		if r.ReceiptState=="SUCCEEDED"&&r.Phase=="TRANSFER"&&r.Progress.Complete(){r.State="SUCCEEDED"}else{r.State="FAILED";if r.FailureReason==""{r.FailureReason="VERIFIED_RECEIPT_MISSING"}}
	}
	now:=m.now();r.FinishedAt=&now;if err:=tx.ReleaseLocks(r.ID);err!=nil{return err};return tx.PutRun(r)
}
