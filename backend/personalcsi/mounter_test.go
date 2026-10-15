package personalcsi

import (
 "context"
 "errors"
 "io"
	"strings"
	"testing"
)

type unreadableMountInfo struct{}
func(unreadableMountInfo)Read([]byte)(int,error){return 0,io.ErrUnexpectedEOF}
func TestPersonalStorageUnknownKernelMetadataCannotAuthorizeUnmount(t *testing.T) {
 target:="/fixture/mount"
 for _,input:=range []string{
  "52 21 0:99 / "+target+" rw\n",
  "52 21 0:99 / "+target+" rw fake metadata missing separator\n",
 } {
  if _,mounted,err:=inspectMountInfo(strings.NewReader(input),target);err==nil||!mounted{t.Fatal("malformed metadata accepted")}
 }
 if _,_,err:=inspectMountInfo(unreadableMountInfo{},target);!errors.Is(err,io.ErrUnexpectedEOF){t.Fatalf("kernel metadata read error lost: %v",err)}
 mounter:=NewFuseMounter();ctx,cancel:=context.WithCancel(context.Background());cancel()
 if _,err:=mounter.Mount(ctx,t.TempDir(),VolumeID("a","a","a",1),nil);!errors.Is(err,context.Canceled){t.Fatalf("canceled node mount: %v",err)}
 if len(mounter.mounts)!=0{t.Fatal("canceled node mount was registered")}
}

func TestPersonalStorageKernelMountIdentity(t *testing.T) {
	target := "/var/lib/kubelet/pods/pod/volumes/kubernetes.io~csi/pv/mount"
	id := VolumeID("owner", "binding", "tenant", 1)
	own := "52 21 0:99 / " + target + " rw - fuse.wellspiking personalstorage:" + id + " rw\n"
	tests := []struct {
		name, line, want string
		mounted, fail    bool
	}{
		{"own", own, id, true, false},
		{"none", own, "", false, false},
		{"foreign type", strings.Replace(own, "fuse.wellspiking", "ext4", 1), "foreign", true, false},
		{"malformed id", strings.Replace(own, id, "personal-"+strings.Repeat("x", 40), 1), "foreign", true, false},
		{"stacked mount", own + strings.Replace(own, "fuse.wellspiking", "ext4", 1), "", true, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := target
			if test.name == "none" {
				query += "-elsewhere"
			}
			got, mounted, err := inspectMountInfo(strings.NewReader(test.line), query)
			if (err != nil) != test.fail || mounted != test.mounted || got != test.want {
				t.Fatalf("identity %q mounted %v error %v", got, mounted, err)
			}
		})
	}
}
