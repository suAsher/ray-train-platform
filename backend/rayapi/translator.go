package rayapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"ray-train-platform-backend/domain"
)

const (
	metadataImage          = "ray-platform.image"
	metadataWorkerReplicas = "ray-platform.worker-replicas"
	metadataGPUsPerWorker  = "ray-platform.gpus-per-worker"
	metadataCPUPerWorker   = "ray-platform.cpu-per-worker"
	metadataMemoryWorker   = "ray-platform.memory-per-worker"
	metadataQueue          = "ray-platform.queue"
)

var (
	digestPackageName = regexp.MustCompile(`^[0-9a-f]{64}\.zip$`)
	rayPackageName    = regexp.MustCompile(`^_ray_pkg_[0-9a-f]{16}\.zip$`)
	dnsLabel          = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	memoryQuantity    = regexp.MustCompile(`^([1-9][0-9]*)(Mi|Gi)$`)
	submissionID      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

func ParsePackageName(protocol, value string) (PackageName, error) {
	if protocol != "gcs" || strings.ContainsAny(value, `/\\?#`) || (!digestPackageName.MatchString(value) && !rayPackageName.MatchString(value)) {
		return PackageName{}, fmt.Errorf("invalid package reference")
	}
	return PackageName{Name: value}, nil
}

func TranslateSubmitRequest(request JobSubmitRequest) (TranslatedSubmitRequest, error) {
	if strings.TrimSpace(request.Entrypoint) == "" || len(request.Entrypoint) > 8192 {
		return TranslatedSubmitRequest{}, fmt.Errorf("invalid entrypoint")
	}
	if request.EntrypointNumCPUs != nil || request.EntrypointNumGPUs != nil || request.EntrypointMemory != nil || len(request.EntrypointResources) != 0 {
		return TranslatedSubmitRequest{}, fmt.Errorf("unsupported entrypoint resources")
	}
	externalID := request.SubmissionID
	if externalID == "" {
		externalID = request.JobID
	}
	if externalID != "" && !submissionID.MatchString(externalID) {
		return TranslatedSubmitRequest{}, fmt.Errorf("invalid submission id")
	}
	packageName, err := parseWorkingDir(request.RuntimeEnv)
	if err != nil {
		return TranslatedSubmitRequest{}, err
	}
	resources, queue, image, err := parseMetadata(request.Metadata)
	if err != nil {
		return TranslatedSubmitRequest{}, err
	}
	nameSeed := externalID
	if nameSeed == "" {
		nameSeed = packageName.Name + "\\x00" + request.Entrypoint
	}
	nameHash := sha256.Sum256([]byte(nameSeed))
	return TranslatedSubmitRequest{
		Package: packageName,
		Spec: domain.JobSpec{
			Name:       "rayjob-" + hex.EncodeToString(nameHash[:])[:24],
			Image:      image,
			Entrypoint: domain.Entrypoint{Command: []string{"/bin/sh", "-lc", request.Entrypoint}},
			Resources:  resources,
			Queue:      queue,
		},
		ExternalSubmissionID: externalID,
	}, nil
}

func parseWorkingDir(runtimeEnv map[string]any) (PackageName, error) {
	if runtimeEnv == nil {
		return PackageName{}, fmt.Errorf("runtime environment is required")
	}
	workingDirectory, ok := runtimeEnv["working_dir"].(string)
	if !ok || !strings.HasPrefix(workingDirectory, "gcs://") {
		return PackageName{}, fmt.Errorf("working directory must use gcs")
	}
	return ParsePackageName("gcs", strings.TrimPrefix(workingDirectory, "gcs://"))
}

func parseMetadata(metadata map[string]string) (domain.Resources, string, string, error) {
	if metadata == nil {
		return domain.Resources{}, "", "", fmt.Errorf("metadata is required")
	}
	image := strings.TrimSpace(metadata[metadataImage])
	if err := domain.ValidatePinnedImage(image); err != nil {
		return domain.Resources{}, "", "", fmt.Errorf("invalid image")
	}
	workers, err := boundedInt(metadata[metadataWorkerReplicas], 1, 3)
	if err != nil {
		return domain.Resources{}, "", "", fmt.Errorf("invalid worker replicas")
	}
	gpus, err := boundedInt(metadata[metadataGPUsPerWorker], 1, 8)
	if err != nil || workers*gpus > 24 {
		return domain.Resources{}, "", "", fmt.Errorf("invalid GPUs per worker")
	}
	cpu, err := boundedInt64(metadata[metadataCPUPerWorker], 1, 64)
	if err != nil {
		return domain.Resources{}, "", "", fmt.Errorf("invalid CPU per worker")
	}
	memory := strings.TrimSpace(metadata[metadataMemoryWorker])
	if !validMemory(memory) {
		return domain.Resources{}, "", "", fmt.Errorf("invalid memory per worker")
	}
	queue := strings.TrimSpace(metadata[metadataQueue])
	if len(queue) > 63 || !dnsLabel.MatchString(queue) {
		return domain.Resources{}, "", "", fmt.Errorf("invalid queue")
	}
	return domain.Resources{WorkerReplicas: workers, GPUsPerWorker: gpus, CPUPerWorker: cpu, MemoryPerWorker: memory}, queue, image, nil
}

func boundedInt(value string, minimum, maximum int) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum || strconv.Itoa(parsed) != value {
		return 0, fmt.Errorf("out of range")
	}
	return parsed, nil
}

func boundedInt64(value string, minimum, maximum int64) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < minimum || parsed > maximum || strconv.FormatInt(parsed, 10) != value {
		return 0, fmt.Errorf("out of range")
	}
	return parsed, nil
}

func validMemory(value string) bool {
	matches := memoryQuantity.FindStringSubmatch(value)
	if matches == nil {
		return false
	}
	amount, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil || amount < 1 {
		return false
	}
	if matches[2] == "Gi" {
		return amount <= 1024
	}
	return amount <= 1024*1024
}

func rayPackageArtifactID(tenantID, userID, packageName string) string {
	sum := sha256.Sum256([]byte(tenantID + "\\x00" + userID + "\\x00" + packageName))
	return "raypkg-" + hex.EncodeToString(sum[:])
}
