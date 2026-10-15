package k8s
import (
 "testing"
 "strings"
 "ray-train-platform-backend/domain"
)
func TestPersonalStorageOnlyEditorWorkerMountsClaim(t *testing.T) {
 workspace := domain.DevWorkspace{ID:"ws-1",TenantID:"team-a",UserID:"alice",Name:"debug-a",Namespace:"tenant-a",GPUCount:0}
 manifest, err := RenderDevRayCluster(workspace,WorkspaceRenderOptions{Image:"registry.example/dev@sha256:"+strings.Repeat("a",64),PersonalStorageClaim:"personal-alice"})
 if err != nil { t.Fatal(err) }
 head,_,_ := nestedMap(manifest.Object,"spec","headGroupSpec")
 if strings.Contains(string(mustJSON(head)),"personal-alice") || strings.Contains(string(mustJSON(head)),"/mnt/storage/wellspiking") { t.Fatal("Ray head must not mount personal SFTP") }
 workers,_,_ := nestedSlice(manifest.Object,"spec","workerGroupSpecs")
 body := string(mustJSON(workers))
 if !strings.Contains(body,"personal-alice") || !strings.Contains(body,"/mnt/storage/wellspiking") { t.Fatalf("editor worker lacks personal mount: %s",body) }
 if strings.Contains(body,"nodePublishSecretRef") || strings.Contains(body,"/dev/fuse") || strings.Contains(body,"SYS_ADMIN") { t.Fatal("workload must receive only PVC, never credentials or FUSE privilege") }
}
