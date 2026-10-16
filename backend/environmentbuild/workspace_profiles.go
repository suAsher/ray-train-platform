package environmentbuild

import (
 "strings"
 "ray-train-platform-backend/domain"
)

// WorkspaceProfile is administrator-owned runtime identity. Workspace output
// and client requests cannot select a different base or prepare implementation.
type WorkspaceProfile struct {
 WorkspaceImage string `json:"workspaceImage"`
 BaseImage string `json:"baseImage"`
 PrepareImage string `json:"prepareImage"`
 Environment domain.ImageEnvironment `json:"environment"`
}

func pinnedProfileImage(image string) bool {
 parts:=strings.Split(image,"@")
 return len(parts)==2 && compatibleWorkspaceSourcePattern.MatchString(image) && strings.HasPrefix(image, RegistryHost+"/") && digestPattern.MatchString(parts[1])
}

// NormalizeWorkspaceProfiles keeps one immutable pairing per workspace digest.
// Legacy same-base compatibility is intentionally separate from new profiles.
func NormalizeWorkspaceProfiles(primary string, compatible []string, profiles []WorkspaceProfile) ([]WorkspaceProfile, error) {
 if len(profiles)>16 { return nil,ErrInvalid }
 seen:=map[string]bool{primary:true}
 for _,image:=range compatible { seen[image]=true }
 normalized:=make([]WorkspaceProfile,0,len(profiles))
 for _,profile:=range profiles {
  if !pinnedProfileImage(profile.WorkspaceImage) || !pinnedProfileImage(profile.BaseImage) || !pinnedProfileImage(profile.PrepareImage) || seen[profile.WorkspaceImage] || profile.Environment.Validate()!=nil { return nil,ErrInvalid }
  seen[profile.WorkspaceImage]=true
  normalized=append(normalized,profile)
 }
 return normalized,nil
}

// ResolveWorkspaceProfile preserves the original single-base configuration and
// only adds explicitly registered profiles; callers must validate configuration.
func ResolveWorkspaceProfile(image, primary, base, prepare string, compatible []string, profiles []WorkspaceProfile) (WorkspaceProfile,bool) {
 if image==primary {
  return WorkspaceProfile{WorkspaceImage:image,BaseImage:base,PrepareImage:prepare},true
 }
 for _,candidate:=range compatible {
  if image==candidate { return WorkspaceProfile{WorkspaceImage:image,BaseImage:base,PrepareImage:prepare},true }
 }
 for _,profile:=range profiles {
  if image==profile.WorkspaceImage { return profile,true }
 }
 return WorkspaceProfile{},false
}

func (s *Service) workspaceProfile(image string) (WorkspaceProfile,bool) {
 return ResolveWorkspaceProfile(image,s.config.WorkspaceImage,s.config.BaseImage,"",s.config.CompatibleWorkspaceImages,s.config.WorkspaceProfiles)
}
