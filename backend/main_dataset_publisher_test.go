package main

import (
	"context"
	"testing"
	"time"

	k8sfake "k8s.io/client-go/kubernetes/fake"
	"ray-train-platform-backend/api"
	"ray-train-platform-backend/config"
	"ray-train-platform-backend/datasetpublisher"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/k8s"
	"ray-train-platform-backend/repositories"
)

func TestNewDatasetPublicationManagerIsDisabledWithoutTheFeatureGate(t *testing.T) {
	manager, err := newDatasetPublicationManager(nil, nil, config.Config{DatasetVersioningEnabled: true})
	if err != nil || manager != nil {
		t.Fatalf("disabled publisher manager=%v err=%v", manager, err)
	}
}

func TestNewDatasetPublicationManagerRequiresControlPlaneDependencies(t *testing.T) {
	cfg := validDatasetPublicationManagerConfig()
	for _, test := range []struct {
		name       string
		repository *repositories.GormRepository
		client     *k8s.Client
	}{
		{name: "repository", client: k8s.NewClientFromInterfaces(nil, k8sfake.NewSimpleClientset())},
		{name: "Kubernetes", repository: repositories.NewGormRepository(nil)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if manager, err := newDatasetPublicationManager(test.repository, test.client, cfg); err == nil || manager != nil {
				t.Fatalf("missing %s dependency was accepted: manager=%v err=%v", test.name, manager, err)
			}
		})
	}
}

func TestNewDatasetPublicationManagerBuildsTheAPIAndLeaderController(t *testing.T) {
	repository := repositories.NewGormRepository(nil)
	client := k8s.NewClientFromInterfaces(nil, k8sfake.NewSimpleClientset())
	manager, err := newDatasetPublicationManager(repository, client, validDatasetPublicationManagerConfig())
	if err != nil {
		t.Fatalf("build dataset publication manager: %v", err)
	}
	if manager == nil {
		t.Fatal("enabled dataset publication manager is nil")
	}
	var _ api.DatasetPublicationManager = manager
	var _ interface{ Run(context.Context) error } = manager
	var _ datasetpublisher.PublicationManagerRepository = repository
	var _ datasetpublisher.PublicationJobClient = client
}

func TestDatasetPublicationControllerOptionsCarryOptionalIRSARoleTRN(t *testing.T) {
	for _, roleTRN := range []string{"", "trn:iam::2103446203:role/tos-rw"} {
		t.Run(roleTRN, func(t *testing.T) {
			cfg := validDatasetPublicationManagerConfig()
			cfg.DatasetPublisherIRSARoleTRN = roleTRN

			options := datasetPublicationControllerOptions(cfg)
			if options.IRSARoleTRN != roleTRN {
				t.Fatalf("controller IRSA role TRN=%q, want %q", options.IRSARoleTRN, roleTRN)
			}
		})
	}
}

func TestDatasetPublicationControllerOptionsCarryProxySecretReference(t *testing.T) {
	cfg := validDatasetPublicationManagerConfig()
	cfg.DatasetPublisherProxySecret = "dataset-publisher-egress"

	options := datasetPublicationControllerOptions(cfg)
	if options.ProxySecretName != cfg.DatasetPublisherProxySecret {
		t.Fatalf("controller proxy Secret=%q, want %q", options.ProxySecretName, cfg.DatasetPublisherProxySecret)
	}
}

func validDatasetPublicationManagerConfig() config.Config {
	return config.Config{
		DatasetVersioningEnabled:                 true,
		DatasetPublisherEnabled:                  true,
		DataSpacesPublicRoot:                     domain.DefaultPublicDataRoot,
		DatasetInternalPrefix:                    domain.DefaultDatasetInternalPrefix,
		DatasetPublisherImage:                    "registry.example/publisher@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		DatasetPublisherImagePullPolicy:          "IfNotPresent",
		DatasetPublisherSourceBucket:             "source-bucket",
		DatasetPublisherTargetBucket:             "target-bucket",
		DatasetPublisherTOSEndpoint:              "tos-cn-shanghai.ivolces.com",
		DatasetPublisherTOSRegion:                "cn-shanghai",
		DatasetPublisherServiceAccount:           "publisher",
		DatasetPublisherQueueName:                "publisher",
		DatasetPublisherPriorityClassName:        "publisher-low",
		DatasetPublisherWorkingDirectory:         "/data/output",
		DatasetPublisherSourceIndexName:          ".raytrain/trusted-index-v2.pkl",
		DatasetPublisherCPURequest:               "1",
		DatasetPublisherCPULimit:                 "4",
		DatasetPublisherMemoryRequest:            "2Gi",
		DatasetPublisherMemoryLimit:              "8Gi",
		DatasetPublisherClientMaxAttempts:        3,
		DatasetPublisherJobBackoffLimit:          3,
		DatasetPublisherJobActiveDeadlineSeconds: int((7 * 24 * time.Hour) / time.Second),
		DatasetPublisherJobTTLSeconds:            int((24 * time.Hour) / time.Second),
		DatasetPublisherInitialRetrySeconds:      1,
		DatasetPublisherMaximumRetrySeconds:      30,
		DatasetPublisherPollIntervalSeconds:      10,
	}
}
