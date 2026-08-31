package domain

import (
	"fmt"
	"strings"
)

// ResolvedDataRoot is the task-time snapshot of a logical data-space claim.
// It stays internal to the control plane; browser responses retain only the
// logical space and fixed container path.
type ResolvedDataRoot struct {
	Space     DataSpaceID `json:"space"`
	ClaimName string      `json:"claimName"`
	// SubPath confines a logical user-visible root below a wider internal PVC.
	// It is persisted only after control-plane resolution and is not accepted
	// from a browser or CLI submission.
	SubPath  string `json:"subPath,omitempty"`
	ReadOnly bool   `json:"readOnly"`
}

type ResolvedDataSpaceRoots struct {
	Personal       *ResolvedDataRoot `json:"personal,omitempty"`
	Team           *ResolvedDataRoot `json:"team,omitempty"`
	Public         *ResolvedDataRoot `json:"public,omitempty"`
	IDCOriginal    *ResolvedDataRoot `json:"idcOriginal,omitempty"`
	IDCWellspiking *ResolvedDataRoot `json:"idcWellspiking,omitempty"`
	IDCShared      *ResolvedDataRoot `json:"idcShared,omitempty"`
	IDCSPKHybrid   *ResolvedDataRoot `json:"idcSpkHybrid,omitempty"`
	IDCSPKSSD      *ResolvedDataRoot `json:"idcSpkSsd,omitempty"`
}

func (roots ResolvedDataSpaceRoots) Validate() error {
	checks := []struct {
		name     string
		root     *ResolvedDataRoot
		space    DataSpaceID
		readOnly bool
	}{
		{name: "personal", root: roots.Personal, space: DataSpaceWorkspace, readOnly: false},
		{name: "team", root: roots.Team, space: DataSpaceTeamShared, readOnly: true},
		{name: "public", root: roots.Public, space: DataSpacePublic, readOnly: true},
		{name: "IDC original", root: roots.IDCOriginal, space: DataSpaceIDCOriginal, readOnly: true},
		{name: "IDC Wellspiking", root: roots.IDCWellspiking, space: DataSpaceIDCWellspiking, readOnly: true},
		{name: "IDC shared", root: roots.IDCShared, space: DataSpaceIDCShared, readOnly: true},
		{name: "IDC SPK Hybrid", root: roots.IDCSPKHybrid, space: DataSpaceIDCSPKHybrid, readOnly: true},
		{name: "IDC SPK SSD", root: roots.IDCSPKSSD, space: DataSpaceIDCSPKSSD, readOnly: true},
	}
	for _, check := range checks {
		if check.root == nil {
			continue
		}
		if check.root.Space != check.space || check.root.ReadOnly != check.readOnly || strings.TrimSpace(check.root.ClaimName) == "" || !dnsLabel.MatchString(check.root.ClaimName) {
			return fmt.Errorf("resolved %s data root has an invalid mount contract", check.name)
		}
		if check.root.SubPath != "" {
			normalized, err := NormalizeStorageRelativePath(check.root.SubPath)
			if err != nil || normalized != check.root.SubPath {
				return fmt.Errorf("resolved %s data root has an invalid confined subpath", check.name)
			}
		}
	}
	return nil
}
