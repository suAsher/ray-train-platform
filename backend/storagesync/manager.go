package storagesync

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type Options struct {
	Now func()time.Time
	PreviewTTL time.Duration
	CheckpointRetention time.Duration
	ReconcileInterval time.Duration
	MaxActiveRuns int
	MaxPendingPreviews int
	CallbackURL string
	MetadataURL string
}
type Manager struct {repo Repository;jobs JobClient;resolver Resolver;options Options}
func NewManager(repo Repository,jobs JobClient,resolver Resolver,options Options)*Manager {
	if options.Now==nil{options.Now=time.Now};if options.PreviewTTL<=0{options.PreviewTTL=15*time.Minute}
	if options.CheckpointRetention<=0{options.CheckpointRetention=7*24*time.Hour};if options.ReconcileInterval<=0{options.ReconcileInterval=5*time.Second}
	if options.MaxActiveRuns<1{options.MaxActiveRuns=1};if options.MaxPendingPreviews<1{options.MaxPendingPreviews=8}
	return &Manager{repo:repo,jobs:jobs,resolver:resolver,options:options}
}
func(m *Manager)now()time.Time{return m.options.Now().UTC()}
func(m *Manager)authorize(ctx context.Context,actor string)error {
	if strings.TrimSpace(actor)==""||m.resolver==nil{return ErrForbidden};return m.resolver.IsAuthorized(ctx,actor)
}
func copyConfig(c Config)Config{out:=c;out.Mappings=append([]Mapping(nil),c.Mappings...);return out}
func(m *Manager)resolve(ctx context.Context,actor string,c Config)([]ResolvedMapping,error){
	if err:=m.authorize(ctx,actor);err!=nil{return nil,err};out:=make([]ResolvedMapping,0,len(c.Mappings))
	for _,mapping:=range c.Mappings {
		source,err:=m.resolver.Resolve(ctx,actor,mapping.Source);if err!=nil{return nil,err}
		dest,err:=m.resolver.Resolve(ctx,actor,mapping.Destination);if err!=nil{return nil,err}
		if mapping.Layout=="DIRECTORY"{dest.Prefix=path.Join(dest.Prefix,path.Base(mapping.Source.RelativePath))}
		out=append(out,ResolvedMapping{Source:source,Destination:dest,Layout:mapping.Layout})
	}
	if err:=ValidateResolved(out);err!=nil{return nil,err};return out,nil
}
func(m *Manager)CreatePlan(ctx context.Context,actor,name string,c Config)(Plan,error){
	if err:=m.authorize(ctx,actor);err!=nil{return Plan{},err};if err:=c.Validate();err!=nil{return Plan{},err}
	name=strings.TrimSpace(name);if name==""||utf8.RuneCountInString(name)>160{return Plan{},fmt.Errorf("%w: name must have 1 to 160 characters",ErrInvalid)}
	if _,err:=m.resolve(ctx,actor,c);err!=nil{return Plan{},err};now:=m.now()
	p:=Plan{ID:"ssp-"+uuid.NewString(),Name:name,CreatedBy:actor,Revision:1,Enabled:false,Config:copyConfig(c),CreatedAt:now,UpdatedAt:now}
	err:=m.repo.Transact(ctx,func(tx Tx)error{return tx.PutPlan(p)});return p,err
}
func(m *Manager)UpdatePlan(ctx context.Context,actor,id string,revision int64,name string,enabled bool,c Config)(Plan,error){
	if err:=m.authorize(ctx,actor);err!=nil{return Plan{},err};if err:=c.Validate();err!=nil{return Plan{},err}
	name=strings.TrimSpace(name);if name==""||utf8.RuneCountInString(name)>160{return Plan{},ErrInvalid};if _,err:=m.resolve(ctx,actor,c);err!=nil{return Plan{},err}
	var result Plan;err:=m.repo.Transact(ctx,func(tx Tx)error{
		old,err:=tx.GetPlan(id);if err!=nil{return err};if !canAccessPlan(actor,old){return ErrNotFound};if old.Revision!=revision{return ErrConflict}
		updated:=old;updated.Name=name;updated.Enabled=enabled;updated.Config=copyConfig(c);updated.Revision++;updated.UpdatedAt=m.now();updated.Owner=actor;updated.FailureReason=""
		updated.NextRunAt,err=c.Schedule.Next(m.now());if err!=nil{return err};if !enabled{updated.NextRunAt=nil};if err=tx.PutPlan(updated);err!=nil{return err};result=updated;return nil
	});return result,err
}
func(m *Manager)CreatePreview(ctx context.Context,actor,planID string,revision int64)(Preview,error){
	if err:=m.authorize(ctx,actor);err!=nil{return Preview{},err};var result Preview
	err:=m.repo.Transact(ctx,func(tx Tx)error{
		plan,err:=tx.GetPlan(planID);if err!=nil{return err};if !canAccessPlan(actor,plan){return ErrNotFound};if plan.Revision!=revision{return ErrConflict}
		if err=m.previewCapacity(tx,actor);err!=nil{return err};resolved,err:=m.resolve(ctx,actor,plan.Config);if err!=nil{return err}
		now:=m.now();result=Preview{ID:"ssv-"+uuid.NewString(),Kind:"PREVIEW",PlanID:planID,Actor:actor,ConfigRevision:revision,Config:copyConfig(plan.Config),Resolved:resolved,ResolutionDigest:resolutionDigest(resolved),State:"QUEUED",Attempt:1,Generation:1,CreatedAt:now,UpdatedAt:now,ExpiresAt:now.Add(m.options.PreviewTTL)}
		result.ExpiresAt=m.previewLease(result);result.BaselineRef,err=baselineRef(tx,planID,revision);if err!=nil{return err};return tx.PutPreview(result)
	});return result,err
}
func(m *Manager)previewCapacity(tx Tx,actor string)error{
	items,err:=tx.ListPreviews();if err!=nil{return err};active,own:=0,0
	for _,p:=range items{if (p.State=="QUEUED"||p.State=="RUNNING")&&p.ExpiresAt.After(m.now()){active++;if p.Actor==actor{own++}}}
	if active>=m.options.MaxPendingPreviews||own>=4{return fmt.Errorf("%w: too many pending previews",ErrConflict)};return nil
}
func(m *Manager)CreateBrowse(ctx context.Context,actor string,location Location,cursor string,limit int)(Preview,error){
	if err:=m.authorize(ctx,actor);err!=nil{return Preview{},err};if err:=location.Validate();err!=nil{return Preview{},err}
	if limit==0{limit=100};if limit<1||limit>1000||len(cursor)>2048{return Preview{},ErrInvalid}
	resolved,err:=m.resolver.Resolve(ctx,actor,location);if err!=nil{return Preview{},err};now:=m.now()
	p:=Preview{ID:"ssv-"+uuid.NewString(),Kind:"BROWSE",Actor:actor,Location:location,Cursor:cursor,Limit:limit,Resolved:[]ResolvedMapping{{Source:resolved}},State:"QUEUED",Attempt:1,Generation:1,CreatedAt:now,UpdatedAt:now,ExpiresAt:now.Add(m.options.PreviewTTL)}
	p.ResolutionDigest=resolutionDigest(p.Resolved);p.ExpiresAt=m.previewLease(p)
	err=m.repo.Transact(ctx,func(tx Tx)error{if err:=m.previewCapacity(tx,actor);err!=nil{return err};return tx.PutPreview(p)});return p,err
}
func(m *Manager)Start(ctx context.Context,actor,planID string,request StartRequest)(Run,error){
	if err:=m.authorize(ctx,actor);err!=nil{return Run{},err};if len(request.IdempotencyKey)<1||len(request.IdempotencyKey)>128||strings.ContainsAny(request.IdempotencyKey,"\x00\r\n")||request.PreviewID==""||request.ManifestDigest==""{return Run{},ErrInvalid}
	var result Run;err:=m.repo.Transact(ctx,func(tx Tx)error{
		plan,err:=tx.GetPlan(planID);if err!=nil{return err};if !canAccessPlan(actor,plan){return ErrNotFound};runs,err:=tx.ListRuns(planID);if err!=nil{return err}
		for _,run:=range runs{if run.RequestedBy==actor&&run.IdempotencyKey==request.IdempotencyKey{if run.PreviewID!=request.PreviewID||run.ConfigRevision!=request.ConfigRevision||run.ManifestDigest!=request.ManifestDigest{return ErrConflict};result=run;return nil}}
		if plan.Revision!=request.ConfigRevision{return ErrPreviewInvalid};for _,run:=range runs{if run.Active(){return ErrConflict}}
		preview,err:=tx.GetPreview(request.PreviewID);if err!=nil{return err}
		if preview.Kind!="PREVIEW"||preview.PlanID!=planID||preview.Actor!=actor||preview.ConfigRevision!=plan.Revision||preview.State!="SUCCEEDED"||!preview.ExpiresAt.After(m.now())||preview.ManifestDigest!=request.ManifestDigest{return ErrPreviewInvalid}
		resolved,err:=m.resolve(ctx,actor,plan.Config);if err!=nil{return err};if resolutionDigest(resolved)!=preview.ResolutionDigest{return ErrPreviewInvalid}
		result=m.newRun(plan,actor,resolved);result.PreviewID=preview.ID;result.IdempotencyKey=request.IdempotencyKey;result.ManifestDigest=preview.ManifestDigest;result.SourceFingerprint=preview.SourceFingerprint;result.TargetFingerprint=preview.TargetFingerprint;result.BaselineRef=preview.BaselineRef
		if err=tx.AcquireLocks(result.ID,result.Attempt,LocksFor(resolved));err!=nil{return err};return tx.PutRun(result)
	});return result,err
}
func(m *Manager)newRun(plan Plan,actor string,resolved []ResolvedMapping)Run{now:=m.now();return Run{ID:"ssr-"+uuid.NewString(),PlanID:plan.ID,RequestedBy:actor,ConfigRevision:plan.Revision,Config:copyConfig(plan.Config),Resolved:resolved,ResolutionDigest:resolutionDigest(resolved),State:"QUEUED",Phase:"PREVIEW",Trigger:"MANUAL",Attempt:1,Generation:1,CreatedAt:now,UpdatedAt:now,RecoverableUntil:now.Add(m.options.CheckpointRetention)}}
func(m *Manager)GetPlan(ctx context.Context,id string)(Plan,error){return m.repo.GetPlan(ctx,id)}
func(m *Manager)ListPlans(ctx context.Context)([]Plan,error){return m.repo.ListPlans(ctx)}
func(m *Manager)GetRun(ctx context.Context,id string)(Run,error){return m.repo.GetRun(ctx,id)}
func(m *Manager)ListRuns(ctx context.Context,planID string)([]Run,error){return m.repo.ListRuns(ctx,planID)}
func(m *Manager)GetPreview(ctx context.Context,id string)(Preview,error){return m.repo.GetPreview(ctx,id)}
func(m *Manager)GetWorkSpec(ctx context.Context,id string,attempt int,generation int64)(WorkSpec,error){
	run,err:=m.repo.GetRun(ctx,id);if err==nil{if run.Attempt!=attempt||run.Generation!=generation||!run.Active()||run.State!="RUNNING"{return WorkSpec{},ErrStaleAttempt};return m.runSpec(run),nil};if !errors.Is(err,ErrNotFound){return WorkSpec{},err}
	p,err:=m.repo.GetPreview(ctx,id);if err!=nil{return WorkSpec{},err};if p.Attempt!=attempt||p.Generation!=generation||p.State!="RUNNING"||!p.ExpiresAt.After(m.now()){return WorkSpec{},ErrStaleAttempt};return m.previewSpec(p),nil
}
// GetReportSpec permits an exact replay of a final receipt after its run has
// finished. It must never authorize a metadata read or a new worker claim.
func(m *Manager)GetReportSpec(ctx context.Context,id string,attempt int,generation int64)(WorkSpec,error){
	r,err:=m.repo.GetRun(ctx,id);if err==nil{if r.Attempt!=attempt||r.Generation!=generation{return WorkSpec{},ErrStaleAttempt};return m.runSpec(r),nil};if !errors.Is(err,ErrNotFound){return WorkSpec{},err}
	p,err:=m.repo.GetPreview(ctx,id);if err!=nil{return WorkSpec{},err};if p.Attempt!=attempt||p.Generation!=generation{return WorkSpec{},ErrStaleAttempt};return m.previewSpec(p),nil
}
func(m *Manager)ControlForRun(ctx context.Context,id string)(string,error){r,err:=m.repo.GetRun(ctx,id);if err!=nil{return "",err};switch r.State{case "PAUSING":return "PAUSE",nil;case "CANCELLING":return "CANCEL",nil};return "",nil}
func(m *Manager)runSpec(r Run)WorkSpec{return WorkSpec{SubjectKind:"run",RunID:r.ID,PreviewID:r.PreviewID,Attempt:r.Attempt,Generation:r.Generation,Phase:r.Phase,Config:r.Config,Mappings:r.Resolved,ManifestDigest:r.ManifestDigest,SourceFingerprint:r.SourceFingerprint,TargetFingerprint:r.TargetFingerprint,BaselineRef:r.BaselineRef,CheckpointRef:"/work/"+r.ID,CallbackURL:m.options.CallbackURL,MetadataURL:m.options.MetadataURL}}
func(m *Manager)previewSpec(p Preview)WorkSpec{return WorkSpec{SubjectKind:"preview",RunID:p.ID,PreviewID:p.ID,Attempt:p.Attempt,Generation:p.Generation,Phase:p.Kind,Config:p.Config,Mappings:p.Resolved,BaselineRef:p.BaselineRef,CheckpointRef:"/work/previews/"+p.ID,CallbackURL:m.options.CallbackURL,MetadataURL:m.options.MetadataURL,Cursor:p.Cursor,Limit:p.Limit}}
func baselineRef(tx Tx,planID string,revision int64)(string,error){runs,err:=tx.ListRuns(planID);if err!=nil{return "",err};var latest Run;for _,r:=range runs{if r.State=="SUCCEEDED"&&r.ConfigRevision==revision&&(latest.ID==""||r.CreatedAt.After(latest.CreatedAt)){latest=r}};if latest.ID==""{return "",nil};return "/work/"+latest.ID,nil}
