package api

import (
	"context"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

type ownerImageStore interface {
	ListImagesForUser(context.Context, string, string, string) ([]domain.PlatformImage, error)
}

// Filtering again makes ownerless legacy adapters safe too. The production
// repository implements ownerImageStore and never falls back to a raw ref.
func visibleImages(ctx context.Context, store ImageStore, tenantID, userID, kind string) ([]domain.PlatformImage, error) {
	images, _, err := visibleImageCatalog(ctx, store, tenantID, userID, kind)
	return images, err
}

// visibleImageCatalog snapshots both the visible images and whether this
// catalogue is authoritative. Capture the unfiltered size before applying ACLs
// so hiding a private entry cannot accidentally enable the legacy fallback.
func visibleImageCatalog(ctx context.Context, store ImageStore, tenantID, userID, kind string) ([]domain.PlatformImage, bool, error) {
	var images []domain.PlatformImage
	var err error
	ownerStore, ownerAware := store.(ownerImageStore)
	if ownerAware {
		images, err = ownerStore.ListImagesForUser(ctx, tenantID, userID, kind)
	} else {
		images, err = store.ListImages(ctx, tenantID, kind)
	}
	if err != nil {
		return nil, true, err
	}
	visible := make([]domain.PlatformImage, 0, len(images))
	for _, image := range images {
		if (kind == "" || image.Kind == kind) && image.VisibleTo(tenantID, userID) {
			visible = append(visible, image)
		}
	}
	return visible, ownerAware || len(images) > 0, nil
}

func visibleImageByReference(ctx context.Context, store ImageStore, tenantID, userID, kind, reference string) (domain.PlatformImage, error) {
	images, err := visibleImages(ctx, store, tenantID, userID, kind)
	if err != nil {
		return domain.PlatformImage{}, err
	}
	for _, image := range images {
		if image.Reference == reference {
			return image, nil
		}
	}
	return domain.PlatformImage{}, repositories.ErrImageNotFound
}
