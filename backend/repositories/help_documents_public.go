package repositories

import (
	"strings"
	"time"

	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/helpdocs"
)

const platformSeedActor = "platform-seed"

var platformSeedHelpIDs = map[string]bool{
	"access":                                 true,
	"admin-node-onboarding":                  true,
	"admin-team-retirement":                  true,
	"artifacts":                              true,
	"cache":                                  true,
	"cli-onboarding-v2":                      true,
	"code":                                   true,
	"command-recipes":                        true,
	"custom-environment":                     true,
	"data-mode":                              true,
	"datasets":                               true,
	"debug":                                  true,
	"diagnose":                               true,
	"errors":                                 true,
	"idc-sync-lifecycle":                     true,
	"mlflow":                                 true,
	"mlflow-api-with-pat":                    true,
	"mlflow-external-tracking":               true,
	"mlflow-framework-metrics":               true,
	"observability":                          true,
	"portal-browser-tools-and-queue":         true,
	"portal-user-feature-map":                true,
	"preflight":                              true,
	"quota":                                  true,
	"quickstart":                             true,
	"ray-data":                               true,
	"resume":                                 true,
	"scaling":                                true,
	"scheduling-topology":                    true,
	"storage":                                true,
	"streaming":                              true,
	"streaming-validation":                   true,
	"submit":                                 true,
	"telemetry-boundary":                     true,
	"unified-login-and-roles":                true,
	"uploads":                                true,
	"worker-connect-and-scheduling-boundary": true,
}

type helpSummaryMeta struct {
	hasPlatformSeed bool
	updatedAt       time.Time
	version         int64
}

func publicHelpDocuments(items []domain.HelpDocument) []domain.HelpDocument {
	meta := publicHelpMeta(items)
	custom := make([]domain.HelpDocument, 0, len(items))
	customIDs := map[string]bool{}
	for _, item := range items {
		if isPublicAdminHelpDocument(item) {
			continue
		}
		if item.UpdatedBy == platformSeedActor && platformSeedHelpIDs[item.ID] {
			continue
		}
		custom = append(custom, item)
		customIDs[item.ID] = true
	}
	if !meta.hasPlatformSeed {
		return custom
	}
	guides := helpdocs.PublicGuides()
	out := make([]domain.HelpDocument, 0, len(guides)+len(custom))
	for _, guide := range guides {
		if customIDs[guide.ID] {
			continue
		}
		guide.Version = meta.version
		guide.PublishedVersion = meta.version
		guide.UpdatedAt = meta.updatedAt
		guide.UpdatedBy = platformSeedActor
		guide.Action = "summary"
		out = append(out, guide)
	}
	out = append(out, custom...)
	return out
}

func publicHelpMeta(items []domain.HelpDocument) helpSummaryMeta {
	var meta helpSummaryMeta
	for _, item := range items {
		if item.UpdatedBy != platformSeedActor || !platformSeedHelpIDs[item.ID] {
			continue
		}
		meta.hasPlatformSeed = true
		if item.UpdatedAt.After(meta.updatedAt) {
			meta.updatedAt = item.UpdatedAt
		}
		if item.Version > meta.version {
			meta.version = item.Version
		}
	}
	if meta.version < 1 {
		meta.version = 1
	}
	return meta
}

func isPublicAdminHelpDocument(item domain.HelpDocument) bool {
	return strings.HasPrefix(item.ID, "admin-") || item.Category == "06 进阶与管理员"
}
