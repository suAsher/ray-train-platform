package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/repositories"
)

type stubImageStore struct {
	images                []domain.PlatformImage
	updatedID             string
	updatedShared         bool
	updatedTargetTenantID string
}

func (s *stubImageStore) CreateImage(context.Context, domain.PlatformImage) error { return nil }
func (s *stubImageStore) ListImages(_ context.Context, tenantID, kind string) ([]domain.PlatformImage, error) {
	matching := make([]domain.PlatformImage, 0, len(s.images))
	for _, image := range s.images {
		if kind != "" && image.Kind != kind {
			continue
		}
		if image.TenantID == "" || image.TenantID == tenantID {
			matching = append(matching, image)
		}
	}
	return matching, nil
}
func (s *stubImageStore) DefaultImage(context.Context, string, string) (domain.PlatformImage, error) {
	return domain.PlatformImage{}, repositories.ErrImageNotFound
}
func (s *stubImageStore) ImageByReference(ctx context.Context, tenantID, kind, reference string) (domain.PlatformImage, error) {
	images, _ := s.ListImages(ctx, tenantID, kind)
	for _, image := range images {
		if image.Reference == reference {
			return image, nil
		}
	}
	return domain.PlatformImage{}, repositories.ErrImageNotFound
}
func (s *stubImageStore) DeleteImage(context.Context, string, string, bool) error { return nil }
func (s *stubImageStore) SetImageShared(_ context.Context, tenantID, id string, shared bool, targetTenantID string) (domain.PlatformImage, error) {
	s.updatedID = id
	s.updatedShared = shared
	s.updatedTargetTenantID = targetTenantID
	targetTenant := tenantID
	if shared {
		targetTenant = ""
	} else if targetTenantID != "" {
		targetTenant = targetTenantID
	}
	return domain.PlatformImage{ID: id, TenantID: targetTenant, Name: "runtime", Kind: domain.ImageKindTraining, Reference: "registry.example/runtime:stable"}, nil
}

func catalogImage(reference string) domain.PlatformImage {
	return domain.PlatformImage{
		ID: "img-1", Name: "PyTorch", Kind: domain.ImageKindTraining, Reference: reference, IsDefault: true,
	}
}

func submissionSpec(image string) domain.JobSpec {
	return domain.JobSpec{
		Name:       "catalog-job",
		Image:      image,
		Source:     domain.CodeSource{Type: "git", URL: "https://git.example/train", Commit: "0123456789abcdef"},
		Entrypoint: domain.Entrypoint{Command: []string{"python", "train.py"}},
		Resources:  domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 4, MemoryPerWorker: "16Gi"},
	}
}

func submitWithCatalog(t *testing.T, store ImageStore, allowlist []string, image string) error {
	t.Helper()
	service := NewSubmissionService(&fakeJobRepository{}, SubmissionServiceOptions{
		ImageAllowlist: allowlist, Images: store,
	})
	_, err := service.Submit(context.Background(), SubmissionInput{
		Principal: auth.Principal{Subject: "u", TenantID: "team-a", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal},
		Spec:      submissionSpec(image),
		Origin:    domain.SubmissionOriginPortal,
	})
	return err
}

// An image an administrator published must be submittable even though it is
// absent from the static config allowlist — the catalogue is the allowlist.
func TestSubmissionAcceptsCatalogueImageOutsideStaticAllowlist(t *testing.T) {
	reference := "registry.internal/pytorch@sha256:" + strings.Repeat("a", 64)
	store := &stubImageStore{images: []domain.PlatformImage{catalogImage(reference)}}

	if err := submitWithCatalog(t, store, []string{"registry.other/only"}, reference); err != nil {
		t.Fatalf("a catalogued image must be accepted: %v", err)
	}
}

// Once the catalogue has entries it becomes authoritative, so an image outside
// it is refused even if the loose config allowlist would match by prefix.
func TestSubmissionRejectsImageOutsideCatalogue(t *testing.T) {
	reference := "registry.internal/pytorch@sha256:" + strings.Repeat("a", 64)
	other := "registry.internal/rogue@sha256:" + strings.Repeat("b", 64)
	store := &stubImageStore{images: []domain.PlatformImage{catalogImage(reference)}}

	err := submitWithCatalog(t, store, []string{"registry.internal"}, other)
	if !errors.Is(err, ErrSubmissionImageNotAllowed) {
		t.Fatalf("expected rejection outside the catalogue, got %v", err)
	}
}

// A deployment that has not populated the catalogue yet must keep working off
// the static allowlist.
func TestSubmissionFallsBackToStaticAllowlistWhenCatalogueEmpty(t *testing.T) {
	reference := "registry.internal/pytorch@sha256:" + strings.Repeat("a", 64)
	store := &stubImageStore{}

	if err := submitWithCatalog(t, store, []string{"registry.internal/pytorch"}, reference); err != nil {
		t.Fatalf("empty catalogue must fall back to the allowlist: %v", err)
	}
	err := submitWithCatalog(t, store, []string{"registry.other"}, reference)
	if !errors.Is(err, ErrSubmissionImageNotAllowed) {
		t.Fatalf("expected static allowlist to still reject, got %v", err)
	}
}
