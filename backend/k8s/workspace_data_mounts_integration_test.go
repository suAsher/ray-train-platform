package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"ray-train-platform-backend/domain"
)

// This explicitly gated test creates only static adapters, never Pods or TOS
// objects. Run on the builder with an existing real personal binding. The JSON
// file is flat DataMountBinding JSON plus storageKey, storageTenantId,
// rootPrefix and volumeAttributesJSON (a JSON-encoded string, without secrets).
func TestWorkspaceFSXStaticAdapterIntegration(t *testing.T) {
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	namespace := os.Getenv("WORKSPACE_FSX_INTEGRATION_NAMESPACE")
	kubeconfig := os.Getenv("WORKSPACE_FSX_INTEGRATION_KUBECONFIG")
	bindingFile := os.Getenv("WORKSPACE_FSX_INTEGRATION_BINDING_FILE")
	if namespace == "" && kubeconfig == "" && bindingFile == "" {
		t.Skip("requires explicit WORKSPACE_FSX_INTEGRATION_NAMESPACE, KUBECONFIG and BINDING_FILE")
	}
	if namespace == "" || kubeconfig == "" || bindingFile == "" {
		t.Fatal("all three WORKSPACE_FSX_INTEGRATION_* settings are required")
	}
	raw, err := os.ReadFile(bindingFile)
	check(err)
	var fixture struct {
		domain.DataMountBinding
		StorageKey           string `json:"storageKey"`
		StorageTenantID      string `json:"storageTenantId"`
		RootPrefix           string `json:"rootPrefix"`
		VolumeAttributesJSON string `json:"volumeAttributesJSON"`
	}
	check(json.Unmarshal(raw, &fixture))
	personal := fixture.DataMountBinding
	personal.StorageKey, personal.StorageTenantID = fixture.StorageKey, fixture.StorageTenantID
	personal.RootPrefix, personal.VolumeAttributesJSON = fixture.RootPrefix, fixture.VolumeAttributesJSON
	capacity := "1Ti"
	pv, pvc, err := BuildWorkspaceMountResources(personal, namespace, capacity)
	check(err)
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	check(err)
	cfg.Timeout = 15 * time.Second
	cfg.ContentType, cfg.AcceptContentTypes = "application/json", "application/json"
	created := map[string]types.UID{}
	paths := map[string]string{"/api/v1/persistentvolumes": "pv", "/api/v1/namespaces/" + namespace + "/persistentvolumeclaims": "pvc"}
	cfg.WrapTransport = func(next http.RoundTripper) http.RoundTripper {
		return workspaceIntegrationTransport(func(request *http.Request) (*http.Response, error) {
			response, err := next.RoundTrip(request)
			kind := paths[request.URL.Path]
			if err != nil || kind == "" || request.Method != http.MethodPost || request.URL.Query().Get("dryRun") != "" || response.StatusCode != http.StatusCreated {
				return response, err
			}
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil {
				return nil, readErr
			}
			response.Body = io.NopCloser(bytes.NewReader(body))
			var object metav1.PartialObjectMetadata
			if err := json.Unmarshal(body, &object); err != nil || object.UID == "" {
				return nil, fmt.Errorf("created %s response lacks a valid UID", kind)
			}
			created[kind] = object.UID
			return response, nil
		})
	}
	kube, err := kubernetes.NewForConfig(cfg)
	check(err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	volumes, claims := kube.CoreV1().PersistentVolumes(), kube.CoreV1().PersistentVolumeClaims(namespace)
	if _, err := volumes.Get(ctx, pv.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected workspace PV to be absent; refusing to modify existing resource (error=%v)", err)
	}
	if _, err := claims.Get(ctx, pvc.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected workspace PVC to be absent; refusing to modify existing resource (error=%v)", err)
	}
	t.Cleanup(func() { cleanupWorkspaceIntegrationAdapters(t, kube, namespace, pv.Name, created) })
	serverPV, err := volumes.Create(ctx, pv, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	check(err)
	check(verifyWorkspacePersistentVolume(serverPV, pv))
	serverPVC, err := claims.Create(ctx, pvc, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	check(err)
	check(verifyWorkspacePersistentVolumeClaim(serverPVC, pvc, serverPV.Spec.ClaimRef))
	if serverPVC.Spec.VolumeName != pv.Name || !sameStorageClass(serverPVC.Spec.StorageClassName, pvc.Spec.StorageClassName) || !reflect.DeepEqual(serverPVC.Spec.AccessModes, pvc.Spec.AccessModes) {
		t.Fatal("server-defaulted PVC changed its static binding contract")
	}
	client := NewClientFromInterfaces(nil, kube)
	claim, _, err := client.EnsureWorkspaceMountResources(ctx, personal, namespace, capacity)
	check(err)
	if claim != pvc.Name || created["pv"] == "" || created["pvc"] == "" {
		t.Fatal("ensure did not create both expected adapters; refusing to adopt concurrently created resources")
	}
	err = wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		current, err := claims.Get(ctx, claim, metav1.GetOptions{})
		return err == nil && current.Status.Phase == corev1.ClaimBound, err
	})
	check(err)
	claim, ready, err := client.EnsureWorkspaceMountResources(ctx, personal, namespace, capacity)
	if err != nil || !ready || claim != pvc.Name {
		t.Fatalf("reuse of server-persisted bound adapters failed: ready=%t err=%v", ready, err)
	}
	t.Logf("server-defaulted static FSX adapters bound and reused: namespace=%s name=%s", namespace, pv.Name)
}

type workspaceIntegrationTransport func(*http.Request) (*http.Response, error)

func (transport workspaceIntegrationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func cleanupWorkspaceIntegrationAdapters(t *testing.T, kube kubernetes.Interface, namespace, name string, created map[string]types.UID) {
	t.Helper()
	if len(created) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pods, err := kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("cannot prove adapters are unused; leaving them retained: %v", err)
	}
	for _, pod := range pods.Items {
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == name {
				t.Fatalf("Pod %s references adapter; refusing cleanup", pod.Name)
			}
		}
	}
	volumes, claims := kube.CoreV1().PersistentVolumes(), kube.CoreV1().PersistentVolumeClaims(namespace)
	if uid := created["pv"]; uid != "" {
		current, err := volumes.Get(ctx, name, metav1.GetOptions{})
		if !apierrors.IsNotFound(err) && (err != nil || current.UID != uid || current.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain) {
			t.Fatalf("PV ownership or Retain policy changed; refusing cleanup: %v", err)
		}
		if err == nil && current.Spec.ClaimRef != nil && current.Spec.ClaimRef.UID != "" && current.Spec.ClaimRef.UID != created["pvc"] {
			t.Fatal("PV references a claim not created by this test; refusing cleanup")
		}
	}
	if uid := created["pvc"]; uid != "" {
		err := claims.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
		if err != nil && !apierrors.IsNotFound(err) {
			t.Fatalf("delete test PVC: %v", err)
		}
		err = wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
			_, err := claims.Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		})
		if err != nil {
			t.Fatalf("test PVC still exists; preserving PV: %v", err)
		}
	}
	if uid := created["pv"]; uid != "" {
		err := volumes.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
		if err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete retained test PV: %v", err)
		}
		err = wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
			_, err := volumes.Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		})
		if err != nil {
			t.Errorf("test PV cleanup did not finish: %v", err)
		}
	}
}
