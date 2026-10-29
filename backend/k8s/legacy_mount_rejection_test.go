package k8s

import (
	"ray-train-platform-backend/domain"
	"testing"
)

func TestStandaloneRejectsLegacyCompanyMountPlans(t *testing.T) {
	plan := DataMountPlan{IDCOriginal: &DataMountRoot{ClaimName: "legacy-source", ReadOnly: true}}
	if err := plan.Validate(); err == nil {
		t.Fatal("legacy mount plan was accepted")
	}
	job := validRenderJob()
	job.Spec.ResolvedDataRoots.IDCOriginal = &domain.ResolvedDataRoot{Space: domain.DataSpaceIDCOriginal, ClaimName: "legacy-source", ReadOnly: true}
	if _, err := RenderRayJob(job, testRenderOptions()); err == nil {
		t.Fatal("persisted legacy root must not be rendered")
	}
}
