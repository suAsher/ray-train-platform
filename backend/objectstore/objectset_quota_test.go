package objectstore

import (
	"context"
	"errors"
	"testing"

	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

type fakeObjectSetQuotaClient struct {
	configuration *tos.GetBucketObjectSetConfigurationOutput
	configErr     error
	prepared      *tos.PutBucketObjectSetConfigurationInput
	objectSets    map[string]bool
	quotas        map[string]string
	created       []string
}

func (client *fakeObjectSetQuotaClient) GetBucketObjectSetConfiguration(context.Context, *tos.GetBucketObjectSetConfigurationInput) (*tos.GetBucketObjectSetConfigurationOutput, error) {
	return client.configuration, client.configErr
}

func (client *fakeObjectSetQuotaClient) PutBucketObjectSetConfiguration(_ context.Context, input *tos.PutBucketObjectSetConfigurationInput) (*tos.PutBucketObjectSetConfigurationOutput, error) {
	client.prepared = input
	client.configuration = &tos.GetBucketObjectSetConfigurationOutput{PathLevel: input.PathLevel, CustomDelimiter: input.CustomDelimiter}
	client.configErr = nil
	return &tos.PutBucketObjectSetConfigurationOutput{}, nil
}

func (client *fakeObjectSetQuotaClient) GetObjectSet(_ context.Context, input *tos.GetObjectSetInput) (*tos.GetObjectSetOutput, error) {
	if client.objectSets[input.ObjectSetName] {
		return &tos.GetObjectSetOutput{ObjectSetName: input.ObjectSetName + "/"}, nil
	}
	return nil, objectSetNotFoundError{}
}

func (client *fakeObjectSetQuotaClient) PutObjectSet(_ context.Context, input *tos.PutObjectSetInput) (*tos.PutObjectSetOutput, error) {
	if client.objectSets == nil {
		client.objectSets = map[string]bool{}
	}
	client.objectSets[input.ObjectSetName] = true
	client.created = append(client.created, input.ObjectSetName)
	return &tos.PutObjectSetOutput{}, nil
}

func (client *fakeObjectSetQuotaClient) PutObjectSetQuota(_ context.Context, input *tos.PutObjectSetQuotaInput) (*tos.PutObjectSetQuotaOutput, error) {
	if client.quotas == nil {
		client.quotas = map[string]string{}
	}
	client.quotas[input.ObjectSetName] = input.StorageQuota
	return &tos.PutObjectSetQuotaOutput{}, nil
}

func (client *fakeObjectSetQuotaClient) GetObjectSetQuota(_ context.Context, input *tos.GetObjectSetQuotaInput) (*tos.GetObjectSetQuotaOutput, error) {
	return &tos.GetObjectSetQuotaOutput{StorageQuota: client.quotas[input.ObjectSetName]}, nil
}

type objectSetNotFoundError struct{}

func (objectSetNotFoundError) Error() string { return "object set not found" }

func (objectSetNotFoundError) StatusCode() int { return 404 }

func TestPersonalObjectSetQuotaCreatesOnlyTheGovernedUserRoot(t *testing.T) {
	client := &fakeObjectSetQuotaClient{
		configuration: &tos.GetBucketObjectSetConfigurationOutput{PathLevel: 5, CustomDelimiter: "/"},
		objectSets:    map[string]bool{},
		quotas:        map[string]string{},
	}
	manager := newPersonalObjectSetQuotaManager("shanghai-data-transfer", client, 100*GiB, 2*TiB)

	quota, err := manager.EnsurePersonalQuota(context.Background(), "local", "user-123", 0)
	if err != nil {
		t.Fatalf("ensure personal quota: %v", err)
	}
	if quota.Bytes != 100*GiB || quota.Enforced != true {
		t.Fatalf("quota=%#v", quota)
	}
	want := "ray-train/tenants/local/users/user-123"
	if len(client.created) != 1 || client.created[0] != want || client.quotas[want] != "107374182400" {
		t.Fatalf("created=%#v quotas=%#v", client.created, client.quotas)
	}
}

func TestPersonalObjectSetQuotaRejectsUnlimitedAndOverLimitRequests(t *testing.T) {
	manager := newPersonalObjectSetQuotaManager("bucket", &fakeObjectSetQuotaClient{
		configuration: &tos.GetBucketObjectSetConfigurationOutput{PathLevel: 5, CustomDelimiter: "/"},
		objectSets:    map[string]bool{},
		quotas:        map[string]string{},
	}, 10*GiB, 20*GiB)

	for _, value := range []int64{-1, 0, 21 * GiB} {
		if _, err := manager.SetPersonalQuota(context.Background(), "tenant-a", "user-a", value); !errors.Is(err, ErrInvalidPersonalStorageQuota) {
			t.Fatalf("quota %d error=%v, want ErrInvalidPersonalStorageQuota", value, err)
		}
	}
}

func TestPersonalObjectSetQuotaCanBackfillAnExistingAccountWhenAnAdminSetsItsQuota(t *testing.T) {
	client := &fakeObjectSetQuotaClient{
		configuration: &tos.GetBucketObjectSetConfigurationOutput{PathLevel: 5, CustomDelimiter: "/"},
		objectSets:    map[string]bool{}, quotas: map[string]string{},
	}
	manager := newPersonalObjectSetQuotaManager("bucket", client, 10*GiB, 20*GiB)

	if _, err := manager.SetPersonalQuota(context.Background(), "tenant-a", "user-a", 15*GiB); err != nil {
		t.Fatalf("backfill personal ObjectSet: %v", err)
	}
	if len(client.created) != 1 || client.created[0] != "ray-train/tenants/tenant-a/users/user-a" {
		t.Fatalf("unexpected created ObjectSets: %#v", client.created)
	}
}

func TestPersonalObjectSetQuotaRequiresCompatibleBucketConfiguration(t *testing.T) {
	manager := newPersonalObjectSetQuotaManager("bucket", &fakeObjectSetQuotaClient{
		configuration: &tos.GetBucketObjectSetConfigurationOutput{PathLevel: 4, CustomDelimiter: "/"},
	}, 10*GiB, 20*GiB)

	if _, err := manager.EnsurePersonalQuota(context.Background(), "tenant-a", "user-a", 0); !errors.Is(err, ErrObjectSetNotReady) {
		t.Fatalf("error=%v, want ErrObjectSetNotReady", err)
	}
}

func TestPersonalObjectSetQuotaExplicitlyInitializesEmptyBucketAtTheFixedRootDepth(t *testing.T) {
	client := &fakeObjectSetQuotaClient{configErr: objectSetNotFoundError{}}
	manager := newPersonalObjectSetQuotaManager("bucket", client, 10*GiB, 20*GiB)

	if err := manager.PrepareBucket(context.Background()); err != nil {
		t.Fatalf("prepare ObjectSet bucket: %v", err)
	}
	if client.prepared == nil || client.prepared.PathLevel != 5 || client.prepared.CustomDelimiter != "/" || client.prepared.EnableDefaultObjectSet {
		t.Fatalf("unexpected ObjectSet configuration: %#v", client.prepared)
	}
}
