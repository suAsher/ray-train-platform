package personalcsi

import (
 "os"
 "strings"
 "testing"
)

func TestPersonalStorageNodeManifestTrustBoundary(t *testing.T) {
 raw,err:=os.ReadFile("../../helm/ray-train-platform/templates/personal-storage-csi.yaml");if err!=nil{t.Fatal(err)}
 text:=string(raw)
 for _,required:=range []string{"kind: CSIDriver","attachRequired: false","fsGroupPolicy: None","volumeLifecycleModes: [Persistent]","type: OnDelete","automountServiceAccountToken: false","mountPropagation: Bidirectional","path: /dev/fuse","type: CharDevice","--kubelet-registration-path=","personalStorage.csi.registrarImage must be pinned","personalStorage.csi.image.digest must be an actual"} {if !strings.Contains(text,required){t.Errorf("missing node trust boundary %q",required)}}
 for _,forbidden:=range []string{"hostNetwork:","hostPID:","ClusterRole","privateKey","hostPort:"} {if strings.Contains(text,forbidden){t.Errorf("unexpected node authority %q",forbidden)}}
}
