package main

import (
	"fmt"
	"ray-train-platform-backend/config"
	"ray-train-platform-backend/environmentbuild"
	"ray-train-platform-backend/k8s"
	"ray-train-platform-backend/registryauth"
	"ray-train-platform-backend/repositories"
	"time"
)

func newEnvironmentBuildService(repository *repositories.GormRepository, client *k8s.Client, cfg config.Config) (*environmentbuild.Service, error) {
	settings := cfg.EnvironmentBuild
	if !settings.Enabled {
		return environmentbuild.NewService(repository, nil, nil, nil, environmentbuild.Config{})
	}
	if client == nil || len(cfg.ImagePullSecrets) == 0 {
		return nil, fmt.Errorf("environment builds require Kubernetes and a configured pull credential")
	}
	// The first pull secret remains the configured base-image credential. Other
	// platform-managed pull secrets may authorize verification of target images.
	runner := k8s.NewEnvironmentRunner(client, k8s.EnvironmentRunnerConfig{
		RegistryHosts: settings.RegistryHosts, Namespace: runtimeNamespace(), BaseImage: settings.BaseImage, WorkspaceImage: settings.WorkspaceImage,
		CompatibleWorkspaceImages: settings.CompatibleWorkspaceImages,
		WorkspaceProfiles:         settings.WorkspaceProfiles,
		PrepareImage:              settings.PrepareImage, PublisherImage: settings.PublisherImage,
		StorageClass: settings.StorageClass, StorageGiB: 60, WheelIndexURL: settings.WheelIndexURL,
		ImagePullSecrets: cfg.ImagePullSecrets, PullSecretName: cfg.ImagePullSecrets[0], NodeSelector: settings.NodeSelector,
		JobTimeout: 30 * time.Minute,
	})
	return environmentbuild.NewService(repository, runner, environmentbuild.HarborRegistry{Client: registryauth.NewClient(), RegistryHosts: append([]string(nil), settings.RegistryHosts...)}, k8s.NewEnvironmentVault(client, runtimeNamespace()), environmentbuild.Config{
		Enabled: true, BaseImage: settings.BaseImage, WorkspaceImage: settings.WorkspaceImage,
		CompatibleWorkspaceImages: settings.CompatibleWorkspaceImages,
		WorkspaceProfiles:         settings.WorkspaceProfiles,
		RegistryHosts:             settings.RegistryHosts,
		EncryptionKey:             settings.EncryptionKey, GlobalConcurrency: 2, UserConcurrency: 1, AuthorizationTTL: 24 * time.Hour,
	})
}
