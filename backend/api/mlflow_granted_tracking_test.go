package api

import (
 "context"
 "errors"
 "strings"
 "testing"

 "ray-train-platform-backend/auth"
 "ray-train-platform-backend/integrations"
 "ray-train-platform-backend/mlflowtracking"
)

type integrationAccessFake struct { denied bool; seen auth.Principal; permission string; ids []string; created string }
func (f *integrationAccessFake) Resolve(_ context.Context, p auth.Principal) (integrations.Identity,error) {
 f.seen=p
 if f.denied { return integrations.Identity{}, integrations.ErrNotFound }
 return integrations.Identity{ID:p.IntegrationID,TenantID:p.TenantID,OwnerUserID:"owner",AllowCreateExperiments:true},nil
}
func (f *integrationAccessFake) Authorize(ctx context.Context,p auth.Principal,id,permission string)(integrations.Identity,error){ f.permission=permission; return f.Resolve(ctx,p) }
func (f *integrationAccessFake) ListGrantedExperimentIDs(ctx context.Context,p auth.Principal,permission string)([]string,error){ _,err:=f.Resolve(ctx,p);return f.ids,err }
func (f *integrationAccessFake) GrantCreated(_ context.Context,p auth.Principal,id string) error { f.created=id; return nil }

type integrationTrackingRecordsFake struct { run mlflowtracking.Run; experiment mlflowtracking.Experiment }
func (f integrationTrackingRecordsFake) GetRun(_ context.Context,a mlflowtracking.Actor,id string)(mlflowtracking.Run,error){ if a.UserID!="owner" { return mlflowtracking.Run{},mlflowtracking.ErrNotFound }; return f.run,nil }
func (f integrationTrackingRecordsFake) GetExperiment(_ context.Context,a mlflowtracking.Actor,id string)(mlflowtracking.Experiment,error){ return f.experiment,nil }

func TestGrantedTrackingRejectsBeforeUpstream(t *testing.T){
 upstream:=&fakeMLflowTrackingService{}
 access:=&integrationAccessFake{denied:true}
 service:=NewGrantedMLflowTracking(upstream,integrationTrackingRecordsFake{},access,[]byte(strings.Repeat("k",32)))
 actor:=mlflowtracking.Actor{TenantID:"team",UserID:"integration:"+strings.Repeat("a",32),IntegrationID:strings.Repeat("a",32)}
 _,err:=service.GetRun(machineTestContext(actor),actor,strings.Repeat("b",32))
 if !errors.Is(err,mlflowtracking.ErrNotFound)||upstream.runID!="" {t.Fatalf("denied machine reached upstream: %v %s",err,upstream.runID)}
}

func TestGrantedTrackingPreservesHumanAndNamespacesMachine(t *testing.T){
 upstream:=&fakeMLflowTrackingService{experiment:mlflowtracking.Experiment{ID:strings.Repeat("c",32),State:"READY"}}
 access:=&integrationAccessFake{}
 service:=NewGrantedMLflowTracking(upstream,integrationTrackingRecordsFake{},access,[]byte(strings.Repeat("k",32)))
 human:=mlflowtracking.Actor{TenantID:"team",UserID:"owner"}
 if _,err:=service.CreateExperiment(context.Background(),human,"same-key","Human");err!=nil||upstream.createKey!="same-key"||upstream.actor!=human {t.Fatal("human contract changed",err)}
 machine:=mlflowtracking.Actor{TenantID:"team",UserID:"integration:"+strings.Repeat("a",32),IntegrationID:strings.Repeat("a",32)}
 if _,err:=service.CreateExperiment(machineTestContext(machine),machine,"same-key","Machine");err!=nil {t.Fatal(err)}
 firstKey:=upstream.createKey
 if upstream.actor.UserID!="owner"||upstream.actor.IntegrationID!=""||firstKey=="same-key"||len(firstKey)>128||access.seen.Subject!=machine.UserID||access.created!=upstream.experiment.ID {t.Fatal("machine delegation lost identity or grant",upstream.actor)}
 other:=machine;other.IntegrationID=strings.Repeat("d",32);other.UserID="integration:"+other.IntegrationID
 if _,err:=service.CreateExperiment(machineTestContext(other),other,"same-key","Machine");err!=nil||upstream.createKey==firstKey {t.Fatal("keys collide between machines",err)}
}

func TestGrantedTrackingChecksWriteGrantAndCursorIdentity(t *testing.T){
 runID,expID:=strings.Repeat("b",32),strings.Repeat("c",32)
 upstream:=&fakeMLflowTrackingService{runPage:mlflowtracking.RunPage{NextCursor:"owner-private-cursor"}}
 access:=&integrationAccessFake{}
 records:=integrationTrackingRecordsFake{run:mlflowtracking.Run{ID:runID,ExperimentID:expID}}
 service:=NewGrantedMLflowTracking(upstream,records,access,[]byte(strings.Repeat("k",32)))
 actor:=mlflowtracking.Actor{TenantID:"team",UserID:"integration:"+strings.Repeat("a",32),IntegrationID:strings.Repeat("a",32)}
 if err:=service.LogRun(machineTestContext(actor),actor,runID,mlflowtracking.Batch{});err!=nil||access.permission!="write" {t.Fatal("write grant not checked",err)}
 page,err:=service.ListRuns(machineTestContext(actor),actor,expID,1,"")
 if err!=nil||page.NextCursor=="owner-private-cursor"||page.NextCursor=="" {t.Fatal("owner cursor exposed",err)}
 other:=actor;other.IntegrationID=strings.Repeat("d",32);other.UserID="integration:"+other.IntegrationID
 if _,err=service.ListRuns(machineTestContext(other),other,expID,1,page.NextCursor);!errors.Is(err,mlflowtracking.ErrInvalid){t.Fatal("cross identity cursor accepted",err)}
 access.denied=true
 if _,err=service.ListRuns(machineTestContext(actor),actor,expID,1,page.NextCursor);!errors.Is(err,mlflowtracking.ErrNotFound){t.Fatal("revoked grant cursor usable",err)}
}

func machineTestContext(a mlflowtracking.Actor) context.Context { return auth.SetPrincipalContext(context.Background(),auth.Principal{Subject:a.UserID,TenantID:a.TenantID,IntegrationID:a.IntegrationID,AuthType:auth.AuthTypePAT,Scopes:[]string{"experiments:read","experiments:write","artifacts:read","artifacts:write"}}) }
