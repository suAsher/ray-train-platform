package environmentbuild

import (
	"context"
	"ray-train-platform-backend/registryauth"
	"strings"
)

// HarborRegistry translates the fixed-origin Harbor client's verified grants.
type HarborRegistry struct{ Client *registryauth.Client }

func (h HarborRegistry) Authenticate(ctx context.Context, c Credentials) error {
	_, err := h.Client.Authenticate(ctx, registryauth.Credentials{Username: c.Username, Secret: c.Secret})
	return err
}
func (h HarborRegistry) Projects(ctx context.Context, c Credentials, page int) ([]Project, error) {
	result, err := h.Client.Projects(ctx, registryauth.Credentials{Username: c.Username, Secret: c.Secret}, page, 50)
	if err != nil {
		return nil, err
	}
	items := make([]Project, 0, len(result.Items))
	for _, p := range result.Items {
		items = append(items, Project{Name: p.Name, ProjectID: p.ProjectID, CanPush: p.CanPush})
	}
	return items, nil
}
func (h HarborRegistry) CheckPush(ctx context.Context, c Credentials, target string) error {
	project, repository, ok := strings.Cut(target, "/")
	if !ok {
		return ErrInvalid
	}
	_, err := h.Client.CheckPush(ctx, registryauth.Credentials{Username: c.Username, Secret: c.Secret}, project, repository)
	return err
}
