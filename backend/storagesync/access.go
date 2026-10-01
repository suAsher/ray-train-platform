package storagesync

import "ray-train-platform-backend/domain"

func hasPersonalMapping(config Config) bool {
	for _, mapping := range config.Mappings {
		for _, location := range []Location{mapping.Source, mapping.Destination} {
			switch domain.DataSpaceID(location.SpaceID) {
			case domain.DataSpaceWorkspace, domain.DataSpaceMyStorage, domain.DataSpaceMyFiles, domain.DataSpaceMyRuns:
				return true
			}
		}
	}
	return false
}

func canAccessPlan(actor string, plan Plan) bool {
	owner := plan.Owner
	if owner == "" {
		owner = plan.CreatedBy
	}
	return !hasPersonalMapping(plan.Config) || owner == actor
}

func canAccessRun(actor string, run Run) bool {
	return !hasPersonalMapping(run.Config) || run.RequestedBy == actor
}
