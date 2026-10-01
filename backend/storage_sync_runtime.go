package main

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"ray-train-platform-backend/api"
	"ray-train-platform-backend/config"
	"ray-train-platform-backend/k8s"
	"ray-train-platform-backend/repositories"
	ss "ray-train-platform-backend/storagesync"
)

type authenticatedStorageSyncJobs struct{base ss.JobClient;key []byte;origin string}
func(j authenticatedStorageSyncJobs) decorate(spec ss.WorkSpec)ss.WorkSpec {
	result:=spec
	prefix:=fmt.Sprintf("%s/api/v1/internal/storage-sync/%s/%s/%d/%d",strings.TrimRight(j.origin,"/"),spec.SubjectKind,spec.RunID,spec.Attempt,spec.Generation)
	result.CallbackURL=prefix+"/report";result.MetadataURL=prefix+"/metadata"
	result.CallbackToken=api.StorageSyncWorkerToken(j.key,spec.SubjectKind,spec.RunID,spec.Attempt,spec.Generation)
	return result
}
func(j authenticatedStorageSyncJobs) Ensure(ctx context.Context,spec ss.WorkSpec)(ss.Observation,error){return j.base.Ensure(ctx,j.decorate(spec))}
func(j authenticatedStorageSyncJobs) Observe(ctx context.Context,id string,attempt int)(ss.Observation,error){return j.base.Observe(ctx,id,attempt)}
func(j authenticatedStorageSyncJobs) Stop(ctx context.Context,id string,attempt int)error{return j.base.Stop(ctx,id,attempt)}
func(j authenticatedStorageSyncJobs) RecoverReceipt(ctx context.Context,spec ss.WorkSpec)error{
	client,ok:=j.base.(interface{RecoverReceipt(context.Context,ss.WorkSpec)error});if !ok{return fmt.Errorf("receipt recovery unavailable")};return client.RecoverReceipt(ctx,j.decorate(spec))
}
func newStorageSyncComponents(database *gorm.DB,repo *repositories.GormRepository,kube *k8s.Client,cfg config.Config,metadata api.StorageSyncMetadataStore)(*ss.Manager,*api.StorageSyncHandler,error){
	if !cfg.StorageSync.Enabled{return nil,nil,nil}
	if kube==nil||metadata==nil||len(cfg.PATPepper)<16||!cfg.DataSpacesEnabled{return nil,nil,fmt.Errorf("storage sync requires configured Kubernetes, platform storage and callback key")}
	if cfg.StorageSync.Bucket!=cfg.TOSBucket||cfg.StorageSync.Region!=cfg.TOSRegion||strings.TrimRight(cfg.StorageSync.Endpoint,"/")!=strings.TrimRight(cfg.TOSEndpoint,"/"){return nil,nil,fmt.Errorf("storage sync must use the configured platform TOS backend")}
	if err:=config.ValidateStorageSyncConfig(cfg.StorageSync);err!=nil{return nil,nil,err}
	sources:=idcDataSpaceSources(cfg);if !cfg.IDCDataSpacesEnabled{sources=nil}
	resolver:=api.NewAdminStorageResolver(repo,api.AdminStorageResolverOptions{Bucket:cfg.TOSBucket,Region:cfg.TOSRegion,PublicRoot:cfg.DataSpacesPublicRoot,IDCSources:sources})
	key:=[]byte(cfg.PATPepper)
	jobs:=authenticatedStorageSyncJobs{base:kube.StorageSyncRuntime(cfg.StorageSync),key:key,origin:cfg.StorageSync.CallbackBaseURL}
	manager:=ss.NewManager(repositories.NewStorageSyncRepository(database),jobs,resolver,ss.Options{MaxActiveRuns:cfg.StorageSync.MaxActiveRuns})
	return manager,api.NewStorageSyncHandler(manager,resolver,metadata,key),nil
}
