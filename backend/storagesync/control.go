package storagesync

import (
	"context"
	"fmt"
	"strings"
)

func(m *Manager)Control(ctx context.Context,actor,id,action string)(Run,error){
	if err:=m.authorize(ctx,actor);err!=nil{return Run{},err};var result Run
	action=strings.ToLower(action)
	err:=m.repo.Transact(ctx,func(tx Tx)error{
		run,err:=tx.GetRun(id);if err!=nil{return err};updated:=run;updated.UpdatedAt=m.now()
		switch strings.ToLower(action) {
		case "pause":
			if run.State=="PAUSED"||run.State=="PAUSING"{result=run;return nil};if !run.Active()||run.State=="CANCELLING"{return ErrConflict}
			if run.State=="QUEUED"{updated.State="PAUSED";updated.StopVerified=true;if err=tx.ReleaseLocks(id);err!=nil{return err}}else{updated.State="PAUSING"}
		case "cancel":
			if run.State=="CANCELLED"||run.State=="CANCELLING"{result=run;return nil};if !run.Active(){return ErrConflict}
			if run.State=="QUEUED"||run.State=="PAUSED"{updated.State="CANCELLED";updated.StopVerified=true;now:=m.now();updated.FinishedAt=&now;if err=tx.ReleaseLocks(id);err!=nil{return err}}else{updated.State="CANCELLING"}
		case "resume","retry":
			if (action=="resume"&&run.State!="PAUSED")||(action=="retry"&&run.State!="FAILED"){return ErrConflict}
			if !run.StopVerified{return fmt.Errorf("%w: waiting for old executor termination and request drain",ErrConflict)}
			if !run.RecoverableUntil.After(m.now()){return fmt.Errorf("%w: checkpoint expired; start a new run",ErrConflict)}
			resolved,err:=m.resolve(ctx,actor,run.Config);if err!=nil{return err};if resolutionDigest(resolved)!=run.ResolutionDigest{return ErrPreviewInvalid}
			runs,err:=tx.ListRuns(run.PlanID);if err!=nil{return err};for _,other:=range runs{if other.ID!=id&&other.Active(){return ErrConflict}}
			if err=tx.AcquireLocks(id,run.Attempt+1,LocksFor(resolved));err!=nil{return err}
			updated.Attempt++;updated.Generation++;updated.Sequence=0;updated.JobUID="";updated.WorkerID="";updated.AuthorizedBy=actor;updated.RequestsDrained=false;updated.StopVerified=false;updated.ReceiptState="";updated.State="QUEUED";updated.FinishedAt=nil;updated.FailureReason="";updated.LastReportDigest=""
		default:return ErrInvalid
		}
		if err=tx.PutRun(updated);err!=nil{return err};result=updated;return nil
	});return result,err
}

func(m *Manager)ListRunFiles(ctx context.Context,id,cursor string,limit int)(FilePage,error){
	if limit==0{limit=100};if limit<1||limit>1000||len(cursor)>8192{return FilePage{},ErrInvalid};return m.repo.ListRunFiles(ctx,id,cursor,limit)
}
