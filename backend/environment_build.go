package main

import (
	"fmt"
	"time"
	"ray-train-platform-backend/config"
	"ray-train-platform-backend/environmentbuild"
	"ray-train-platform-backend/k8s"
	"ray-train-platform-backend/registryauth"
	"ray-train-platform-backend/repositories"
)

func newEnvironmentBuildService(repository *repositories.GormRepository, client *k8s.Client, cfg config.Config) (*environmentbuild.Service, error) {
	settings := cfg.EnvironmentBuild
	if !settings.Enabled { return environmentbuild.NewService(repository, nil, nil, nil, environmentbuild.Config{}) }
	if client == nil || len(cfg.ImagePullSecrets) == 0 { return nil, fmt.Errorf("environment builds require Kubernetes and a configured pull credential") }
	runner := k8s.NewEnvironmentRunner(client, k8s.EnvironmentRunnerConfig{
		Namespace: runtimeNamespace(), BaseImage: settings.BaseImage, WorkspaceImage: settings.WorkspaceImage,
		PrepareImage: settings.PrepareImage, PublisherImage: settings.PublisherImage,
		StorageClass: settings.StorageClass, StorageGiB: 60, WheelIndexURL: settings.WheelIndexURL,
		ImagePullSecrets: cfg.ImagePullSecrets, PullSecretName: cfg.ImagePullSecrets[0], NodeSelector: settings.NodeSelector,
		JobTimeout: 30*time.Minute,
	})
	return environmentbuild.NewService(repository, runner, environmentbuild.HarborRegistry{Client: registryauth.NewClient()}, k8s.NewEnvironmentVault(client, runtimeNamespace()), environmentbuild.Config{
		Enabled: true, BaseImage: settings.BaseImage, WorkspaceImage: settings.WorkspaceImage,
		EncryptionKey: settings.EncryptionKey, GlobalConcurrency: 2, UserConcurrency: 1, AuthorizationTTL: 24*time.Hour,
	})
}
