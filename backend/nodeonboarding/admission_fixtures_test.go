package nodeonboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

// Set NODE_ONBOARDING_FIXTURE_CONFIG to a JSON admissionFixtureConfig and
// NODE_ONBOARDING_FIXTURE_DIR to an empty temporary directory to export the exact
// controller templates for kubectl create --dry-run=server. No cluster API is used.
type admissionFixtureConfig struct {
	Config   Config
	NodeName string
	NodeUID  types.UID
	Hostname string
	Shares   []nfsShare
}

func TestAdmissionFixtures(t *testing.T) {
	ctx := context.Background()
	input := admissionFixtureConfig{Config: testConfig(), NodeName: "gpu-1", NodeUID: "fixture-node-uid", Hostname: "gpu-1", Shares: []nfsShare{{Server: "10.0.0.1", Path: "/exports"}}}
	outputDir, configPath := os.Getenv("NODE_ONBOARDING_FIXTURE_DIR"), os.Getenv("NODE_ONBOARDING_FIXTURE_CONFIG")
	if outputDir != "" && configPath == "" {
		t.Fatal("fixture export requires NODE_ONBOARDING_FIXTURE_CONFIG with actual deployment values")
	}
	if configPath != "" {
		input = admissionFixtureConfig{}
		raw, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			t.Fatal(err)
		}
	}
	if !validNode(input.NodeName) || input.NodeUID == "" {
		t.Fatal("valid NodeName and nonempty NodeUID required")
	}
	if input.Hostname == "" {
		input.Hostname = input.NodeName
	}
	client := fake.NewSimpleClientset()
	controller, err := NewController(client, input.Config)
	if err != nil {
		t.Fatal(err)
	}
	node := eligibleNode()
	node.Name = input.NodeName
	node.UID = input.NodeUID
	node.Labels["kubernetes.io/hostname"] = input.Hostname
	sharesJSON, err := json.Marshal(input.Shares)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().ConfigMaps(controller.config.ConfigNamespace).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: controller.config.NFSConfigMap, Namespace: controller.config.ConfigNamespace}, Data: map[string]string{"shares.json": string(sharesJSON)}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	shares, err := controller.nfsShares(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{controller.config.Data1StorageClass, controller.config.Data2StorageClass} {
		if _, err := client.StorageV1().StorageClasses().Create(ctx, &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: name}, VolumeBindingMode: pointer(storagev1.VolumeBindingWaitForFirstConsumer)}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := controller.ensureClaims(ctx, node); err != nil {
		t.Fatal(err)
	}
	prepare, probe := controller.preparePod(node), controller.probePod(node, shares)
	prepare.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}
	probe.TypeMeta = prepare.TypeMeta
	objects := map[string]any{"prepare-pod.json": prepare, "probe-pod.json": probe}
	for i := 1; i <= 2; i++ {
		claim, err := client.CoreV1().PersistentVolumeClaims(controller.config.Namespace).Get(ctx, resourceName(node, fmt.Sprintf("cache%d", i)), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		claim.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"}
		objects[fmt.Sprintf("cache%d-pvc.json", i)] = claim
	}
	if outputDir != "" {
		clean := filepath.Clean(outputDir)
		if !filepath.IsAbs(clean) || !(strings.HasPrefix(clean, "/private/tmp/") || strings.HasPrefix(clean, "/tmp/") || strings.HasPrefix(clean, filepath.Clean(os.TempDir())+string(os.PathSeparator))) {
			t.Fatal("fixture output must be an absolute temporary subdirectory")
		}
		if err := os.MkdirAll(clean, 0700); err != nil {
			t.Fatal(err)
		}
		outputDir = clean
	}
	for name, object := range objects {
		raw, err := json.MarshalIndent(object, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		var check map[string]any
		if err := json.Unmarshal(raw, &check); err != nil {
			t.Fatal(err)
		}
		if check["apiVersion"] != "v1" {
			t.Fatal("missing Kubernetes TypeMeta")
		}
		if outputDir == "" {
			continue
		}
		target := filepath.Join(outputDir, name)
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.Write(append(raw, '\n'))
		closeErr := file.Close()
		if writeErr != nil {
			t.Fatal(writeErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		t.Log(target)
	}
}
