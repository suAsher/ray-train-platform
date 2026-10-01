package api

import (
	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/domain"
	ss "ray-train-platform-backend/storagesync"
)

func syncHasPersonal(config ss.Config) bool {
	for _, mapping := range config.Mappings {
		for _, location := range []ss.Location{mapping.Source, mapping.Destination} {
			switch domain.DataSpaceID(location.SpaceID) {
			case domain.DataSpaceWorkspace, domain.DataSpaceMyStorage, domain.DataSpaceMyFiles, domain.DataSpaceMyRuns:
				return true
			}
		}
	}
	return false
}
func syncCanReadPlan(actor string, plan ss.Plan) bool {
	owner := plan.Owner
	if owner == "" {
		owner = plan.CreatedBy
	}
	return !syncHasPersonal(plan.Config) || owner == actor
}
func syncCanReadRun(actor string, run ss.Run) bool {
	return !syncHasPersonal(run.Config) || run.RequestedBy == actor
}
func (h *StorageSyncHandler) checkPlan(c *gin.Context, id string) bool {
	plan, err := h.manager.GetPlan(c.Request.Context(), id)
	if err == nil && !syncCanReadPlan(c.GetString("storageSyncActor"), plan) {
		err = ss.ErrNotFound
	}
	if err != nil {
		h.fail(c, err)
		return false
	}
	return true
}
func (h *StorageSyncHandler) checkRun(c *gin.Context, id string) bool {
	run, err := h.manager.GetRun(c.Request.Context(), id)
	if err == nil && !syncCanReadRun(c.GetString("storageSyncActor"), run) {
		err = ss.ErrNotFound
	}
	if err != nil {
		h.fail(c, err)
		return false
	}
	return true
}
func (h *StorageSyncHandler) workerAuthority(c *gin.Context, spec ss.WorkSpec) bool {
	var actor string
	var err error
	if spec.SubjectKind == "preview" {
		var preview ss.Preview
		preview, err = h.manager.GetPreview(c.Request.Context(), spec.RunID)
		actor = preview.Actor
	} else {
		var run ss.Run
		run, err = h.manager.GetRun(c.Request.Context(), spec.RunID)
		actor = run.AuthorizedBy
		if actor == "" {
			actor = run.RequestedBy
		}
	}
	if err != nil {
		h.fail(c, err)
		return false
	}
	if err = h.resolver.IsAuthorized(c.Request.Context(), actor); err != nil {
		h.fail(c, ss.ErrForbidden)
		return false
	}
	return true
}
