package k8s

import (
	"reflect"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestPersonalAliasKeepsStorageHomeAndExistingMounts(t *testing.T) {
	const subPath = "tenants/pending-users/users/jikuan.tang"
	plan := DataMountPlan{
		Personal: &DataMountRoot{ClaimName: "data-tenant-yolo", SubPath: subPath},
		Team: &DataMountRoot{ClaimName: "data-tenant-yolo", SubPath: "tenants/yolo/shared", ReadOnly: true},
		Public: &DataMountRoot{ClaimName: "data-tenant-yolo", SubPath: "public", ReadOnly: true},
	}
	before := *plan.Personal
	workspace := domain.DevWorkspace{ID: "ws-alias", TenantID: "yolo", UserID: "opaque-user", Name: "dev-alias", Namespace: "tenant-yolo", GPUCount: 0}
	dev, err := RenderDevRayCluster(workspace, WorkspaceRenderOptions{
		Image: "registry.example/dev@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DataMounts: plan,
	})
	if err != nil { t.Fatal(err) }
	job := validRenderJob()
	job.Spec.ResolvedDataRoots = domain.ResolvedDataSpaceRoots{
		Personal: &domain.ResolvedDataRoot{Space: domain.DataSpaceWorkspace, ClaimName: "data-tenant-yolo", SubPath: subPath},
		Team: &domain.ResolvedDataRoot{Space: domain.DataSpaceTeamShared, ClaimName: "data-tenant-yolo", SubPath: "tenants/yolo/shared", ReadOnly: true},
		Public: &domain.ResolvedDataRoot{Space: domain.DataSpacePublic, ClaimName: "data-tenant-yolo", SubPath: "public", ReadOnly: true},
	}
	training, err := RenderRayJob(job, testRenderOptions())
	if err != nil { t.Fatal(err) }
	for name, pod := range map[string]map[string]any{
		"workspace-head": dataMountDevPodSpec(t, dev, "head"),
		"workspace-worker": dataMountDevPodSpec(t, dev, "worker"),
		"training-head": cacheHeadPodSpec(t, training.Object),
		"training-worker": cacheWorkerPodSpec(t, training.Object),
	} {
		t.Run(name, func(t *testing.T) {
			assertStorageMountWithSubPath(t, pod, "platform-data-personal", "data-tenant-yolo", domain.MyStorageMountPath, subPath, false)
			assertStorageMountWithSubPath(t, pod, "platform-data-personal", "data-tenant-yolo", "/mnt/storage/jikuan.tang", subPath, false)
			assertStorageMountWithSubPath(t, pod, "platform-data-personal", "data-tenant-yolo", domain.TeamStorageMountPath, "tenants/yolo/shared", true)
			assertStorageMountWithSubPath(t, pod, "platform-data-personal", "data-tenant-yolo", domain.PublicStorageMountPath, "public", true)
			if countPVCVolumes(pod, "data-tenant-yolo") != 1 { t.Fatal("alias must reuse the existing PVC volume") }
		})
	}
	if !reflect.DeepEqual(before, *plan.Personal) { t.Fatal("rendering mutated the resolved personal root") }
}

func TestPersonalAliasDoesNotGuessLegacyRootsOrOverrideReservedPaths(t *testing.T) {
	for _, subPath := range []string{"", "other/root", "tenants/t/users/me", "tenants/t/users/team", "tenants/t/users/public", "tenants/t/users/alice/files", "tenants/t/shared/alice"} {
		t.Run(subPath, func(t *testing.T) {
			mounts, _ := appendTrainingDataRoots(nil, nil, domain.ResolvedDataSpaceRoots{
				Personal: &domain.ResolvedDataRoot{Space: domain.DataSpaceWorkspace, ClaimName: "personal", SubPath: subPath},
			})
			if len(mounts) != 1 || mounts[0].(map[string]any)["mountPath"] != domain.MyStorageMountPath {
				t.Fatalf("legacy/reserved root must keep only the existing personal path: %#v", mounts)
			}
		})
	}
}
