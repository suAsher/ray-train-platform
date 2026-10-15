package personalfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"ray-train-platform-backend/personalstorage"
)

func localFileNode(t *testing.T) (*Node, *localStore) {
	t.Helper()
	store := &localStore{dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(store.dir, "file"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	root := NewRoot(store)
	fs.NewNodeFS(root, &fs.Options{})
	inode, code := root.Lookup(context.Background(), "file", &fuse.EntryOut{})
	if code != 0 {
		t.Fatal(code)
	}
	root.AddChild("file", inode, false)
	return inode.Operations().(*Node), store
}
func TestPersonalStorageTruncateAndUnsupportedMetadata(t *testing.T) {
	for _, withHandle := range []bool{false, true} {
		t.Run(map[bool]string{false: "path", true: "handle"}[withHandle], func(t *testing.T) {
			node, store := localFileNode(t)
			ctx := context.Background()
			var handle fs.FileHandle
			if withHandle {
				var code syscall.Errno
				handle, _, code = node.Open(ctx, uint32(os.O_RDWR))
				if code != 0 {
					t.Fatal(code)
				}
				defer handle.(fs.FileReleaser).Release(ctx)
			}
			in := &fuse.SetAttrIn{}
			in.Valid = fuse.FATTR_SIZE
			in.Size = 3
			if code := node.Setattr(ctx, handle, in, &fuse.AttrOut{}); code != 0 {
				t.Fatal(code)
			}
			data, err := os.ReadFile(filepath.Join(store.dir, "file"))
			if err != nil || string(data) != "ori" {
				t.Fatalf("remote truncate: %q %v", data, err)
			}
			for _, valid := range []uint32{fuse.FATTR_MODE, fuse.FATTR_UID, fuse.FATTR_MTIME} {
				in.Valid = valid
				if code := node.Setattr(ctx, handle, in, &fuse.AttrOut{}); code != syscall.ENOTSUP {
					t.Fatalf("unsupported metadata %v: %v", valid, code)
				}
			}
			in.Valid = fuse.FATTR_SIZE
			in.Size = ^uint64(0)
			if code := node.Setattr(ctx, handle, in, &fuse.AttrOut{}); code != syscall.EFBIG {
				t.Fatalf("overflow size: %v", code)
			}
			if info, _ := os.Stat(filepath.Join(store.dir, "file")); info.Size() != 3 {
				t.Fatal("rejected metadata changed bytes")
			}
		})
	}
}

type errorFile struct {
	personalstorage.File
	err        error
	closeCount int
}

func (f *errorFile) ReadAt([]byte, int64) (int, error)  { return 0, f.err }
func (f *errorFile) WriteAt([]byte, int64) (int, error) { return 0, f.err }
func (f *errorFile) Truncate(int64) error               { return f.err }
func (f *errorFile) Close() error                       { f.closeCount++; return f.err }

type errorOpenStore struct {
	personalstorage.Store
	file personalstorage.File
	err  error
}

func (s errorOpenStore) Open(context.Context, string, int) (personalstorage.File, error) {
	return s.file, s.err
}

func TestPersonalStorageFileErrorsAndCloseOnce(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want syscall.Errno
	}{
		{"authorization", personalstorage.ErrForbidden, syscall.EACCES},
		{"missing", personalstorage.ErrNotFound, syscall.ENOENT},
		{"invalid", personalstorage.ErrInvalid, syscall.EINVAL},
		{"upload limit", personalstorage.ErrTooLarge, syscall.EFBIG},
		{"cancellation", context.Canceled, syscall.EINTR},
		{"upstream disconnected", personalstorage.ErrUnavailable, syscall.EIO},
		{"filesystem full", syscall.ENOSPC, syscall.ENOSPC},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			raw := &errorFile{err: test.err}
			file := &FileHandle{file: raw}
			ctx := context.Background()
			if _, code := file.Read(ctx, make([]byte, 1), 0); code != test.want {
				t.Fatalf("read: %v", code)
			}
			if _, code := file.Write(ctx, []byte("a"), 0); code != test.want {
				t.Fatalf("write: %v", code)
			}
			for i := 0; i < 2; i++ {
				if code := file.Release(ctx); code != test.want {
					t.Fatalf("close: %v", code)
				}
			}
			if raw.closeCount != 1 {
				t.Fatal("remote handle closed more than once")
			}
			if _, code := file.Read(ctx, nil, -1); code != syscall.EINVAL {
				t.Fatalf("negative read: %v", code)
			}
			if _, code := file.Write(ctx, nil, -1); code != syscall.EINVAL {
				t.Fatalf("negative write: %v", code)
			}
			if code := file.Fsync(ctx, 0); code != syscall.ENOTSUP {
				t.Fatal("claimed unsupported durability")
			}
		})
	}
}
func TestPersonalStorageTruncateFailsWithoutLocalMutation(t *testing.T) {
	node, store := localFileNode(t)
	ctx := context.Background()
	input := &fuse.SetAttrIn{}
	input.Valid = fuse.FATTR_SIZE
	input.Size = 1
	raw := &errorFile{err: personalstorage.ErrForbidden}
	if code := node.Setattr(ctx, &FileHandle{file: raw}, input, &fuse.AttrOut{}); code != syscall.EACCES {
		t.Fatalf("handle: %v", code)
	}
	node.store = errorOpenStore{Store: store, err: personalstorage.ErrUnavailable}
	if code := node.Setattr(ctx, nil, input, &fuse.AttrOut{}); code != syscall.EIO {
		t.Fatalf("open failure: %v", code)
	}
	node.store = errorOpenStore{Store: store, file: raw}
	if code := node.Setattr(ctx, nil, input, &fuse.AttrOut{}); code != syscall.EACCES {
		t.Fatalf("truncate failure: %v", code)
	}
	if raw.closeCount != 1 {
		t.Fatal("failed path truncate leaked handle")
	}
	if data, _ := os.ReadFile(filepath.Join(store.dir, "file")); string(data) != "original" {
		t.Fatal("failed truncate changed local file")
	}
}
func TestPersonalStorageNamespaceAndFileTypeBoundaries(t *testing.T) {
	node, store := localFileNode(t)
	root := node.root
	ctx := context.Background()
	if err := os.Mkdir(filepath.Join(store.dir, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", filepath.Join(store.dir, "link")); err != nil {
		t.Fatal(err)
	}
	if code := root.Unlink(ctx, "directory"); code != syscall.EISDIR {
		t.Fatalf("unlink directory: %v", code)
	}
	if code := root.Rmdir(ctx, "file"); code != syscall.ENOTDIR {
		t.Fatalf("rmdir file: %v", code)
	}
	if code := root.Unlink(ctx, "missing"); code != syscall.ENOENT {
		t.Fatalf("missing unlink: %v", code)
	}
	if code := root.Unlink(ctx, "link"); code != syscall.EACCES {
		t.Fatalf("link unlink: %v", code)
	}
	if _, code := root.Lookup(ctx, "link", &fuse.EntryOut{}); code != syscall.EACCES {
		t.Fatalf("link lookup: %v", code)
	}
	if code := root.Rename(ctx, "file", NewRoot(store), "new", 0); code != syscall.EXDEV {
		t.Fatalf("cross mount rename: %v", code)
	}
	if code := root.Rename(ctx, "file", root, "new", 1); code != syscall.ENOTSUP {
		t.Fatalf("rename flags: %v", code)
	}
	for _, name := range []string{".shared", ".spiking-group-shared", ".."} {
		if _, _, _, code := root.Create(ctx, name, uint32(os.O_RDWR), 0600, &fuse.EntryOut{}); code != syscall.EACCES {
			t.Fatalf("create %q: %v", name, code)
		}
		if _, code := root.Mkdir(ctx, name, 0700, &fuse.EntryOut{}); code != syscall.EACCES {
			t.Fatalf("mkdir %q: %v", name, code)
		}
		if code := root.Unlink(ctx, name); code != syscall.EACCES {
			t.Fatalf("unlink %q: %v", name, code)
		}
		if code := root.Rename(ctx, name, root, "new", 0); code != syscall.EACCES {
			t.Fatalf("rename source %q: %v", name, code)
		}
		if code := root.Rename(ctx, "file", root, name, 0); code != syscall.EACCES {
			t.Fatalf("rename target %q: %v", name, code)
		}
	}
	if _, _, _, code := root.Create(ctx, "device", uint32(os.O_RDWR), syscall.S_IFCHR|0600, &fuse.EntryOut{}); code != syscall.EPERM {
		t.Fatalf("device creation: %v", code)
	}
	if _, _, _, code := root.Create(ctx, "file", uint32(os.O_RDWR|os.O_EXCL), 0600, &fuse.EntryOut{}); code != syscall.EEXIST {
		t.Fatalf("exclusive overwrite: %v", code)
	}
	node.store = errorOpenStore{Store: store, err: personalstorage.ErrUnavailable}
	if _, _, code := node.Open(ctx, 0); code != syscall.EIO {
		t.Fatalf("disconnected open: %v", code)
	}
	if _, code := root.Mkdir(ctx, "directory", 0700, &fuse.EntryOut{}); code != syscall.EEXIST {
		t.Fatalf("mkdir exists: %v", code)
	}
}
func TestPersonalStorageCanceledMountDoesNotTouchTarget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewMount(ctx, "/not-a-real-target", "fixture", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mount: %v", err)
	}
	target := filepath.Join(t.TempDir(), "missing")
	if _, err := NewMount(context.Background(), target, "fixture", nil); err == nil {
		t.Fatal("mount unexpectedly created missing target")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("mount failure created fallback directory")
	}
}
