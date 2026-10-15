package personalstorage_test
import(
 "context"
 "errors"
 "os"
 "strings"
 "testing"
 ps "ray-train-platform-backend/personalstorage"
 "ray-train-platform-backend/personalstorage/testfixture"
)
func TestPersonalStorageRealSFTPTwoUserIsolation(t *testing.T){
 fixture,err:=testfixture.Start();if err!=nil{t.Fatal(err)};defer fixture.Close();ctx:=context.Background()
 factory,err:=ps.NewSFTPFactory(fixture.Endpoint);if err!=nil{t.Fatal(err)}
 a,err:=factory.Open(ctx,fixture.Credentials[0]);if err!=nil{t.Fatal(err)};defer a.Close();b,err:=factory.Open(ctx,fixture.Credentials[1]);if err!=nil{t.Fatal(err)};defer b.Close()
 if err:=a.Mkdir(ctx,"models");err!=nil{t.Fatal(err)};file,err:=a.Open(ctx,"models/only-a",os.O_CREATE|os.O_EXCL|os.O_RDWR);if err!=nil{t.Fatal(err)};if _,err=file.WriteAt([]byte("owner-a"),0);err!=nil{t.Fatal(err)};_ = file.Close()
 if _,err=b.Stat(ctx,"models/only-a");!errors.Is(err,ps.ErrNotFound){t.Fatalf("foreign owner file visible: %v",err)}
 wrong:=fixture.Credentials[0];wrong.PrivateKey=fixture.Credentials[1].PrivateKey;if _,err=factory.Open(ctx,wrong);!errors.Is(err,ps.ErrUnavailable){t.Fatal("cross-user credential accepted")}
 raw,err:=fixture.RawClient(0);if err!=nil{t.Fatal(err)};defer raw.Close();if err=raw.Mkdir("/.shared");err!=nil{t.Fatal(err)};if err=raw.Symlink("/models/only-a","/escape");err!=nil{t.Fatal(err)}
 entries,err:=a.List(ctx,"");if err!=nil{t.Fatal(err)};for _,entry:=range entries{if entry.Name==".shared"||entry.Name=="escape"{t.Fatal("excluded entry listed")}}
 if _,err=a.Open(ctx,"escape",os.O_RDONLY);!errors.Is(err,ps.ErrForbidden){t.Fatalf("symlink accepted: %v",err)}
 if _,err=a.Stat(ctx,"../user-b");!errors.Is(err,ps.ErrInvalid){t.Fatal("traversal accepted")}
 if err:=ps.Upload(ctx,a,"complete",strings.NewReader("payload"),7,32);err!=nil{t.Fatal(err)}
 _ = a.Close();a,err=factory.Open(ctx,fixture.Credentials[0]);if err!=nil{t.Fatal(err)};defer a.Close();if _,err=a.Stat(ctx,"models/only-a");err!=nil{t.Fatal("reconnect lost owner data")}
}
func TestPersonalStorageHostKeyPinRequired(t *testing.T){
 fixture,err:=testfixture.Start();if err!=nil{t.Fatal(err)};defer fixture.Close();other,err:=testfixture.Start();if err!=nil{t.Fatal(err)};defer other.Close()
 if _,err:=ps.NewSFTPFactory(ps.Endpoint{Host:"localhost",Port:22});err==nil{t.Fatal("missing pin accepted")}
 endpoint:=fixture.Endpoint;endpoint.KnownHosts=other.Endpoint.KnownHosts;if _,err:=ps.NewSFTPFactory(endpoint);err==nil{t.Fatal("foreign endpoint pin accepted")}
}
