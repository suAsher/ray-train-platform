package personalcsi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"ray-train-platform-backend/personalstorage"
)

type lifecycleStore struct {
	personalstorage.Store
	closed int
}

func (s *lifecycleStore) Close() error { s.closed++; return nil }

type lifecycleFactory struct {
	store *lifecycleStore
	ctx   context.Context
	err   error
}

func (f *lifecycleFactory) Open(ctx context.Context, _ personalstorage.Credential) (personalstorage.Store, error) {
	f.ctx = ctx
	return f.store, f.err
}

type faultyMounter struct {
	*testMounter
	inspectErr, mountErr, unmountErr error
}

func (m *faultyMounter) Inspect(target string) (string, bool, error) {
	if m.inspectErr != nil {
		return "", false, m.inspectErr
	}
	return m.testMounter.Inspect(target)
}
func (m *faultyMounter) Mount(ctx context.Context, target, id string, store personalstorage.Store) (Mounted, error) {
	if m.mountErr != nil {
		return nil, m.mountErr
	}
	return m.testMounter.Mount(ctx, target, id, store)
}
func (m *faultyMounter) Unmount(target string) error {
	if m.unmountErr != nil {
		return m.unmountErr
	}
	return m.testMounter.Unmount(target)
}

func TestPersonalStoragePublishValidationBeforeConnecting(t *testing.T) {
	tests := []struct {
		name   string
		change func(*csi.NodePublishVolumeRequest)
		code   codes.Code
	}{
		{"missing capability", func(r *csi.NodePublishVolumeRequest) { r.VolumeCapability = nil }, codes.InvalidArgument},
		{"block device", func(r *csi.NodePublishVolumeRequest) {
			r.VolumeCapability.AccessType = &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}
		}, codes.InvalidArgument},
		{"read only", func(r *csi.NodePublishVolumeRequest) { r.Readonly = true }, codes.InvalidArgument},
		{"many writers", func(r *csi.NodePublishVolumeRequest) {
			r.VolumeCapability.AccessMode.Mode = csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER
		}, codes.InvalidArgument},
		{"mount flags", func(r *csi.NodePublishVolumeRequest) { r.VolumeCapability.GetMount().MountFlags = []string{"bind"} }, codes.InvalidArgument},
		{"foreign filesystem", func(r *csi.NodePublishVolumeRequest) { r.VolumeCapability.GetMount().FsType = "ext4" }, codes.InvalidArgument},
		{"mount group", func(r *csi.NodePublishVolumeRequest) { r.VolumeCapability.GetMount().VolumeMountGroup = "0" }, codes.InvalidArgument},
		{"missing namespace", func(r *csi.NodePublishVolumeRequest) { delete(r.VolumeContext, "namespace") }, codes.PermissionDenied},
		{"foreign handle", func(r *csi.NodePublishVolumeRequest) { r.VolumeId = VolumeID("other", "other", "tenant-a", 1) }, codes.PermissionDenied},
		{"extra endpoint", func(r *csi.NodePublishVolumeRequest) { r.VolumeContext["endpoint"] = "untrusted" }, codes.PermissionDenied},
		{"empty key", func(r *csi.NodePublishVolumeRequest) { r.Secrets["privateKey"] = "" }, codes.PermissionDenied},
		{"large key", func(r *csi.NodePublishVolumeRequest) { r.Secrets["privateKey"] = strings.Repeat("x", 16385) }, codes.PermissionDenied},
		{"invalid generation", func(r *csi.NodePublishVolumeRequest) {
			r.VolumeContext["generation"] = "01"
			r.Secrets["generation"] = "01"
		}, codes.PermissionDenied},
		{"invalid username", func(r *csi.NodePublishVolumeRequest) {
			r.VolumeContext["username"] = "../bob"
			r.Secrets["username"] = "../bob"
		}, codes.PermissionDenied},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			driver, factory, mounter, request := publishFixture(t)
			test.change(request)
			if _, err := driver.NodePublishVolume(context.Background(), request); status.Code(err) != test.code {
				t.Fatalf("validation code: %v", err)
			}
			if factory.calls != 0 || mounter.count != 0 {
				t.Fatal("invalid request connected or mounted")
			}
		})
	}
	d, _, _, _ := publishFixture(t)
	if _, err := d.NodePublishVolume(context.Background(), nil); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	if _, err := d.NodeUnpublishVolume(context.Background(), nil); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
}
func TestPersonalStoragePublishFailuresCloseOnlyOwnSession(t *testing.T) {
	for _, failure := range []string{"inspect", "connect", "mount"} {
		t.Run(failure, func(t *testing.T) {
			d, _, base, req := publishFixture(t)
			store := &lifecycleStore{}
			factory := &lifecycleFactory{store: store}
			mounter := &faultyMounter{testMounter: base}
			d.factory = factory
			d.mounter = mounter
			want := codes.Unavailable
			switch failure {
			case "inspect":
				mounter.inspectErr = errors.New("fixture read failure")
				want = codes.Internal
			case "connect":
				factory.err = personalstorage.ErrUnavailable
			case "mount":
				mounter.mountErr = errors.New("fixture mount failure")
			}
			if _, err := d.NodePublishVolume(context.Background(), req); status.Code(err) != want {
				t.Fatal(err)
			}
			if len(base.mounts) != 0 || len(d.volumes) != 0 {
				t.Fatal("failed publish left a usable fallback")
			}
			if failure == "mount" && (store.closed != 1 || factory.ctx.Err() != context.Canceled) {
				t.Fatal("failed mount leaked its remote session")
			}
		})
	}
}
func TestPersonalStorageSessionOutlivesRPCAndClosesOnUnpublish(t *testing.T) {
	d, _, base, req := publishFixture(t)
	store := &lifecycleStore{}
	factory := &lifecycleFactory{store: store}
	d.factory = factory
	rpc, cancel := context.WithCancel(context.Background())
	if err := os.Remove(req.TargetPath); err != nil {
		t.Fatal(err)
	}
	if _, err := d.NodePublishVolume(rpc, req); err != nil {
		t.Fatal(err)
	}
	cancel()
	if factory.ctx.Err() != nil || store.closed != 0 {
		t.Fatal("successful publish tied session to finished RPC")
	}
	if info, err := os.Stat(req.TargetPath); err != nil || !info.IsDir() {
		t.Fatal("publish failed to create actual kubelet target")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if factory.ctx.Err() != context.Canceled || store.closed != 1 || base.unmounts != 1 || len(d.volumes) != 0 {
		t.Fatal("shutdown failed to cancel and close personal session")
	}
	if err := d.Close(); err != nil || base.unmounts != 1 {
		t.Fatal("shutdown not idempotent")
	}
}
func TestPersonalStorageUnmountFailuresPreserveRetryableState(t *testing.T) {
	d, _, base, req := publishFixture(t)
	store := &lifecycleStore{}
	factory := &lifecycleFactory{store: store}
	mounter := &faultyMounter{testMounter: base}
	d.factory = factory
	d.mounter = mounter
	if _, err := d.NodePublishVolume(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	mounter.inspectErr = errors.New("fixture metadata unavailable")
	if _, err := d.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{VolumeId: req.VolumeId, TargetPath: req.TargetPath}); status.Code(err) != codes.Internal {
		t.Fatal(err)
	}
	mounter.inspectErr = nil
	mounter.unmountErr = errors.New("fixture busy mount")
	if err := d.Close(); err == nil {
		t.Fatal("shutdown swallowed mount failure")
	}
	if len(d.volumes) != 1 || store.closed != 0 || factory.ctx.Err() != nil {
		t.Fatal("failed unmount discarded live session")
	}
	mounter.unmountErr = nil
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if store.closed != 1 {
		t.Fatal("retried unmount leaked session")
	}
}
func TestPersonalStorageLostMountAndSymlinkTargetFailClosed(t *testing.T) {
	d, _, m, req := publishFixture(t)
	if _, err := d.NodePublishVolume(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	delete(m.mounts, req.TargetPath)
	if _, err := d.NodePublishVolume(context.Background(), req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("lost mount served fallback: %v", err)
	}
	if _, err := d.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{VolumeId: req.VolumeId, TargetPath: req.TargetPath}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(req.TargetPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), req.TargetPath); err != nil {
		t.Fatal(err)
	}
	if _, err := d.NodePublishVolume(context.Background(), req); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	if _, err := d.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{VolumeId: req.VolumeId, TargetPath: req.TargetPath}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	req.TargetPath = filepath.Join("relative", "target")
	if _, err := d.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{VolumeId: req.VolumeId, TargetPath: req.TargetPath}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
}
func TestPersonalStoragePluginIdentityAndNodeOnlyCapabilities(t *testing.T) {
	d := NewDriver("node-fixture", "/unused", nil, nil)
	ctx := context.Background()
	info, err := d.GetPluginInfo(ctx, &csi.GetPluginInfoRequest{})
	if err != nil || info.Name != DriverName || info.VendorVersion == "" {
		t.Fatal("missing driver identity")
	}
	node, err := d.NodeGetInfo(ctx, &csi.NodeGetInfoRequest{})
	if err != nil || node.NodeId != "node-fixture" {
		t.Fatal("wrong node identity")
	}
	capabilities, err := d.GetPluginCapabilities(ctx, &csi.GetPluginCapabilitiesRequest{})
	if err != nil || len(capabilities.Capabilities) != 0 {
		t.Fatal("advertised unsupported controller")
	}
	nodeCapabilities, err := d.NodeGetCapabilities(ctx, &csi.NodeGetCapabilitiesRequest{})
	if err != nil || len(nodeCapabilities.Capabilities) != 0 {
		t.Fatal("advertised unsupported staging or expansion")
	}
	probe, err := d.Probe(ctx, &csi.ProbeRequest{})
	if err != nil || !probe.Ready.Value {
		t.Fatal("driver probe failed")
	}
}
