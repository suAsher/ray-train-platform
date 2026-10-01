package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
	"ray-train-platform-backend/repositories"
	ss "ray-train-platform-backend/storagesync"
)

type StorageSyncIdentityStore interface {
	FindLocalUserByID(context.Context,string)(domain.LocalUser,error)
	ListDataBindings(context.Context,string,string)([]domain.DataMountBinding,error)
	ListTenantSummaries(context.Context)([]repositories.TenantSummary,error)
}
type AdminStorageResolverOptions struct {
	Bucket,Region,PublicRoot string
	IDCSources map[domain.DataSpaceID]k8s.IDCDataMountSource
}
type AdminStorageResolver struct { store StorageSyncIdentityStore; options AdminStorageResolverOptions }
type StorageSyncSpace struct {
	SpaceID string `json:"spaceId"`
	TenantID string `json:"tenantId,omitempty"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	UserPath string `json:"userPath"`
	CanRead bool `json:"canRead"`
	CanWrite bool `json:"canWrite"`
}
func NewAdminStorageResolver(store StorageSyncIdentityStore, options AdminStorageResolverOptions)*AdminStorageResolver {
	sources:=make(map[domain.DataSpaceID]k8s.IDCDataMountSource,len(options.IDCSources));for id,source:=range options.IDCSources{sources[id]=source};options.IDCSources=sources
	return &AdminStorageResolver{store:store,options:options}
}
func (r *AdminStorageResolver) actor(ctx context.Context,id string)(domain.LocalUser,error){
	if r==nil||r.store==nil||id==""{return domain.LocalUser{},ss.ErrForbidden}
	user,err:=r.store.FindLocalUserByID(ctx,id)
	if err!=nil||user.ID!=id||user.Disabled||!(auth.Principal{Roles:user.Roles}).HasRole(domain.RoleSuperAdmin){return domain.LocalUser{},ss.ErrForbidden}
	return user,nil
}
func (r *AdminStorageResolver) IsAuthorized(ctx context.Context,actor string)error{_,err:=r.actor(ctx,actor);return err}
func (r *AdminStorageResolver) personalSpaces(ctx context.Context,user domain.LocalUser)([]domain.DataSpace,error){
	principal:=auth.Principal{Subject:user.ID,Username:user.Username,TenantID:user.TenantID,StorageTenantID:user.StorageTenantID,StorageKey:user.StorageKey}
	home:=StorageTenantForPrincipal(principal)
	root,err:=domain.PersonalDataRootFor(home,StorageKeyForPrincipal(principal));if err!=nil{return nil,ss.ErrInvalid}
	bindings,err:=r.store.ListDataBindings(ctx,user.TenantID,user.ID);if err!=nil{return nil,err}
	for _,binding:=range bindings {
		if binding.Scope!=domain.DataMountScopePersonal||binding.SpaceID!=domain.DataSpaceWorkspace||binding.TenantID!=user.TenantID||binding.UserID!=user.ID{continue}
		root=binding.RootPrefix;home=binding.StorageTenantID;if home==""{home=StorageTenantForPrincipal(principal)}
		break
	}
	return domain.PersonalDataSpacesForStorageHome(user.TenantID,home,root,r.options.PublicRoot)
}
func (r *AdminStorageResolver) Spaces(ctx context.Context,actor string)([]StorageSyncSpace,error){
	user,err:=r.actor(ctx,actor);if err!=nil{return nil,err}
	spaces,err:=r.personalSpaces(ctx,user);if err!=nil{return nil,err}
	result:=make([]StorageSyncSpace,0,len(spaces))
	for _,space:=range spaces {
		if space.ID==domain.DataSpaceTeamShared{continue}
		kind:="TOS";writable:=true
		if space.Provider==domain.StorageProviderIDC{if _,ok:=r.options.IDCSources[space.ID];!ok{continue};kind="IDC";writable=false}
		result=append(result,StorageSyncSpace{SpaceID:string(space.ID),Name:space.Name,Kind:kind,UserPath:space.MountPath,CanRead:true,CanWrite:writable})
	}
	teams,err:=r.store.ListTenantSummaries(ctx);if err!=nil{return nil,err}
	for _,team:=range teams{if team.RetiredAt==nil{result=append(result,StorageSyncSpace{SpaceID:string(domain.DataSpaceTeamShared),TenantID:team.ID,Name:team.Name+" · 团队共享",Kind:"TOS",UserPath:domain.TeamStorageMountPath,CanRead:true,CanWrite:true})}}
	return result,nil
}
func (r *AdminStorageResolver) Resolve(ctx context.Context,actor string,location ss.Location)(ss.ResolvedLocation,error){
	if err:=location.Validate();err!=nil{return ss.ResolvedLocation{},err}
	user,err:=r.actor(ctx,actor);if err!=nil{return ss.ResolvedLocation{},err}
	resolved:=ss.ResolvedLocation{Location:location,Kind:"TOS",Region:r.options.Region,Bucket:r.options.Bucket,StorageID:"tos:"+r.options.Region+":"+r.options.Bucket}
	spaceID:=domain.DataSpaceID(location.SpaceID)
	if source,ok:=r.options.IDCSources[spaceID];ok {
		if location.TenantID!=""||!domain.IsKnownDataSpace(spaceID)||!strings.HasPrefix(string(spaceID),"idc-")||source.Server==""||source.Path==""{return ss.ResolvedLocation{},ss.ErrInvalid}
		resolved.Kind="IDC";resolved.Bucket="";resolved.Region="";resolved.NFSServer=source.Server;resolved.NFSRoot=source.Path;resolved.Prefix=location.RelativePath
		resolved.StorageID="nfs:"+source.Server+":"+source.Path
	} else {
		root,err:=r.tosRoot(ctx,user,location);if err!=nil{return ss.ResolvedLocation{},err}
		resolved.Prefix=strings.TrimSuffix(root,"/");if location.RelativePath!=""{resolved.Prefix+="/"+location.RelativePath}
	}
	encoded,err:=json.Marshal(resolved);if err!=nil{return ss.ResolvedLocation{},err};digest:=sha256.Sum256(encoded);resolved.Revision=hex.EncodeToString(digest[:])
	return resolved,nil
}
func (r *AdminStorageResolver) tosRoot(ctx context.Context,user domain.LocalUser,location ss.Location)(string,error){
	if location.SpaceID==string(domain.DataSpaceTeamShared) {
		if location.TenantID==""{return "",fmt.Errorf("%w: select an explicit team",ss.ErrInvalid)}
		teams,err:=r.store.ListTenantSummaries(ctx);if err!=nil{return "",err}
		for _,team:=range teams{if team.ID==location.TenantID&&team.RetiredAt==nil{spaces,err:=domain.PersonalDataSpacesFor(team.ID,user.ID);if err!=nil{return "",ss.ErrInvalid};space,_:=domain.FindDataSpace(spaces,domain.DataSpaceTeamShared);return space.RootPrefix,nil}}
		return "",ss.ErrInvalid
	}
	if location.TenantID!=""{return "",ss.ErrInvalid}
	spaces,err:=r.personalSpaces(ctx,user);if err!=nil{return "",err}
	for _,space:=range spaces{if string(space.ID)==location.SpaceID&&space.Provider==domain.StorageProviderTOS&&space.RootPrefix!=""{return space.RootPrefix,nil}}
	return "",ss.ErrInvalid
}
