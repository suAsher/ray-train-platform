package repositories

import (
	"sort"
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

var legacyPublicHelpGuideIDs = map[string]string{
	"access":                                 "account-api",
	"artifacts":                              "training-guide",
	"cache":                                  "data",
	"cli-onboarding-v2":                      "quickstart",
	"code":                                   "data",
	"command-recipes":                        "training-guide",
	"contract":                               "training-guide",
	"custom-environment":                     "data",
	"data-mode":                              "data",
	"datasets":                               "data",
	"diagnose":                               "troubleshooting",
	"errors":                                 "troubleshooting",
	"idc-sync-lifecycle":                     "data",
	"mlflow-api-with-pat":                    "mlflow",
	"mlflow-external-tracking":               "mlflow",
	"mlflow-framework-metrics":               "mlflow",
	"observability":                          "mlflow",
	"portal-browser-tools-and-queue":         "troubleshooting",
	"portal-user-feature-map":                "quickstart",
	"preflight":                              "training-guide",
	"quota":                                  "quickstart",
	"ray-data":                               "training-guide",
	"resume":                                 "training-guide",
	"scaling":                                "training-guide",
	"scheduling-topology":                    "training-guide",
	"storage":                                "data",
	"streaming":                              "training-guide",
	"streaming-validation":                   "training-guide",
	"submit":                                 "training-guide",
	"telemetry-boundary":                     "troubleshooting",
	"unified-login-and-roles":                "quickstart",
	"uploads":                                "data",
	"worker-connect-and-scheduling-boundary": "debug",
}

type helpSummaryMeta struct {
	hasPlatformSeed bool
	updatedAt       time.Time
	version         int64
}

func publicHelpDocuments(items []domain.HelpDocument) []domain.HelpDocument {
	meta := publicHelpMeta(items)
	if !meta.hasPlatformSeed {
		out := make([]domain.HelpDocument, 0, len(items))
		for _, item := range items {
			if isPublicAdminHelpDocument(item) {
				continue
			}
			if item.UpdatedBy == platformSeedActor && platformSeedHelpIDs[item.ID] {
				continue
			}
			out = append(out, item)
		}
		return out
	}
	custom := make([]domain.HelpDocument, 0, len(items))
	customIDs := map[string]bool{}
	folded := map[string][]domain.HelpDocument{}
	for _, item := range items {
		if isPublicAdminHelpDocument(item) {
			continue
		}
		if item.UpdatedBy == platformSeedActor && platformSeedHelpIDs[item.ID] {
			continue
		}
		if target, ok := legacyPublicHelpGuideIDs[item.ID]; ok {
			folded[target] = append(folded[target], item)
			continue
		}
		custom = append(custom, item)
		customIDs[item.ID] = true
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
		guide = appendLegacyPublicSections(guide, folded[guide.ID])
		out = append(out, guide)
	}
	for i := range custom {
		custom[i] = appendLegacyPublicSections(custom[i], folded[custom[i].ID])
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

func appendLegacyPublicSections(guide domain.HelpDocument, sections []domain.HelpDocument) domain.HelpDocument {
	if len(sections) == 0 {
		return guide
	}
	ordered := append([]domain.HelpDocument(nil), sections...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].SortOrder != ordered[j].SortOrder {
			return ordered[i].SortOrder < ordered[j].SortOrder
		}
		return ordered[i].ID < ordered[j].ID
	})
	var body strings.Builder
	body.WriteString(guide.Markdown)
	for _, section := range ordered {
		body.WriteString("\n\n### ")
		body.WriteString(section.Title)
		body.WriteString("\n\n")
		body.WriteString(section.Markdown)
		if section.UpdatedAt.After(guide.UpdatedAt) {
			guide.UpdatedAt = section.UpdatedAt
			guide.UpdatedBy = section.UpdatedBy
		}
		if section.Version > guide.Version {
			guide.Version = section.Version
		}
		if section.PublishedVersion > guide.PublishedVersion {
			guide.PublishedVersion = section.PublishedVersion
		}
	}
	guide.Markdown = body.String()
	return guide
}
