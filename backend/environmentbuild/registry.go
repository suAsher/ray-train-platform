package environmentbuild

import (
	"context"
	"ray-train-platform-backend/registryauth"
	"strings"
)

// HarborRegistry translates the selected trusted Harbor client's verified grants.
type HarborRegistry struct{ Client *registryauth.Client }

func (h HarborRegistry) clientForHost(host string) (*registryauth.Client, error) {
	resolved, err := registryauth.NormalizeHost(host)
	if err != nil {
		return nil, ErrInvalid
	}
	if h.Client != nil && h.Client.Host() == resolved {
		return h.Client, nil
	}
	return registryauth.NewClientForHost(resolved)
}

func (h HarborRegistry) Authenticate(ctx context.Context, host string, c Credentials) error {
	client, err := h.clientForHost(host)
	if err != nil {
		return err
	}
	_, err = client.Authenticate(ctx, registryauth.Credentials{Username: c.Username, Secret: c.Secret})
	return err
}
func (h HarborRegistry) Projects(ctx context.Context, host string, c Credentials, page int) ([]Project, error) {
	client, err := h.clientForHost(host)
	if err != nil {
		return nil, err
	}
	result, err := client.Projects(ctx, registryauth.Credentials{Username: c.Username, Secret: c.Secret}, page, 50)
	if err != nil {
		return nil, err
	}
	items := make([]Project, 0, len(result.Items))
	for _, p := range result.Items {
		items = append(items, Project{Name: p.Name, ProjectID: p.ProjectID, CanPush: p.CanPush})
	}
	return items, nil
}
func (h HarborRegistry) CheckPush(ctx context.Context, host string, c Credentials, target string) error {
	project, repository, ok := strings.Cut(target, "/")
	if !ok {
		return ErrInvalid
	}
	client, err := h.clientForHost(host)
	if err != nil {
		return err
	}
	_, err = client.CheckPush(ctx, registryauth.Credentials{Username: c.Username, Secret: c.Secret}, project, repository)
	return err
}
