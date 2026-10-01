package storagesync

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{Mode: "INCREMENTAL", ConflictPolicy: "UPDATE", Verification: "METADATA", Concurrency: 2,
		Schedule: Schedule{Kind: "MANUAL", Timezone: "Asia/Shanghai"},
		Mappings: []Mapping{{Source: Location{SpaceID: "idc", RelativePath: "images"}, Destination: Location{SpaceID: "personal", RelativePath: "sync-test"}, Layout: "CONTENTS"}}}
}

func TestConfigPathsAndPolicies(t *testing.T) {
	if err := testConfig().Validate(); err != nil { t.Fatal(err) }
	for _, invalid := range []string{"../escape", "a/../b", "/absolute", "a//b", "a/./b", "a\\b", "a\x00b", "%2e%2e/x", "a%2fb"} {
		t.Run(invalid, func(t *testing.T) { c := testConfig(); c.Mappings[0].Source.RelativePath = invalid; if c.Validate() == nil { t.Fatalf("accepted unsafe path %q", invalid) } })
	}
	for _, allowed := range []string{"", "含 空格/100% complete", "ab/a.json"} {
		c := testConfig(); c.Mappings[0].Source.RelativePath = allowed; if err := c.Validate(); err != nil { t.Fatalf("rejected valid path %q: %v", allowed, err) }
	}
	c := testConfig(); c.Concurrency = 65; if c.Validate() == nil { t.Fatal("unbounded concurrency accepted") }
	c = testConfig(); c.ConflictPolicy = "DELETE"; if c.Validate() == nil { t.Fatal("destructive policy accepted") }
	c = testConfig(); c.Schedule.Timezone = "UTC"; if c.Validate() == nil { t.Fatal("unsupported timezone accepted") }
}

func TestPrefixLocksAndLayouts(t *testing.T) {
	for _, tc := range []struct { a,b string; want bool }{{"a","a/b",true},{"a","ab",false},{"","a",true},{"a/b","a/b",true},{"a/b","a/c",false}} {
		if got := PrefixesOverlap(tc.a,tc.b); got != tc.want { t.Fatalf("overlap(%q,%q)=%v", tc.a,tc.b,got) }
	}
	if got,err := DestinationPrefix("folder/images", "dest", "DIRECTORY"); err != nil || got != "dest/images" { t.Fatalf("directory layout: %q %v",got,err) }
	if got,err := DestinationPrefix("folder/images", "dest", "CONTENTS"); err != nil || got != "dest" { t.Fatalf("contents layout: %q %v",got,err) }
	if _,err := DestinationPrefix("", "dest", "DIRECTORY"); err == nil { t.Fatal("root directory has no basename") }
	a := PathLock{StorageID:"tos",Region:"cn",Bucket:"bucket",Prefix:"a",Mode:"READ"}
	b := a; b.Prefix = "a/b"
	if LocksConflict(a,b) { t.Fatal("read/read conflict") }
	b.Mode="WRITE"; if !LocksConflict(a,b) { t.Fatal("hierarchical read/write not blocked") }
	b.Bucket="other"; if LocksConflict(a,b) { t.Fatal("independent roots conflict") }
}

func TestScheduleShanghaiAndMissedSlots(t *testing.T) {
	now := time.Date(2026,10,1,3,15,0,0,time.UTC)
	for _, tc := range []struct{s Schedule; want string}{
		{Schedule{Kind:"INTERVAL",Timezone:"Asia/Shanghai",EveryHours:3},"2026-10-01T04:00:00Z"},
		{Schedule{Kind:"DAILY",Timezone:"Asia/Shanghai",Time:"11:15"},"2026-10-02T03:15:00Z"},
		{Schedule{Kind:"WEEKLY",Timezone:"Asia/Shanghai",Time:"10:00",Weekday:4},"2026-10-08T02:00:00Z"},
	} { got,err:=tc.s.Next(now); if err != nil || got == nil || got.Format(time.RFC3339)!=tc.want { t.Fatalf("next %#v: %v %v",tc.s,got,err) } }
	if got,err:=(Schedule{Kind:"MANUAL",Timezone:"Asia/Shanghai"}).Next(now); err!=nil || got!=nil { t.Fatalf("manual schedule: %v %v",got,err) }
}

func TestProgressNoOverflowAndVerifiedSuccess(t *testing.T) {
	p := Progress{ScanComplete:true, SourceFiles:2,SourceBytes:10,TransferFiles:1,TransferBytes:10,CompletedFiles:1,CompletedBytes:10,VerifiedFiles:2,VerifiedBytes:10,ReusedFiles:1,NetworkBytes:30}
	if err:=p.Validate();err!=nil {t.Fatal(err)}
	if !p.Complete() {t.Fatal("empty reused object should permit verified success")}
	p.CompletedBytes=11; if p.Validate()==nil {t.Fatal("progress exceeded 100%")}
	p=Progress{ScanComplete:true};if !p.Complete(){t.Fatal("empty manifest did not complete")}
	p=Progress{SourceFiles:1,VerifiedFiles:1};if p.Complete(){t.Fatal("unfrozen scan completed")}
	p=Progress{ScanComplete:true,SourceFiles:1,SourceBytes:10,VerifiedFiles:1,VerifiedBytes:10};if p.Complete(){t.Fatal("source without transfer or reuse assignment completed")}
}

func TestPublicSnapshotsDoNotExposePhysicalStorage(t *testing.T) {
	r:=Run{ID:"r",Resolved:[]ResolvedMapping{{Source:ResolvedLocation{Bucket:"private-bucket",NFSServer:"private-server",NFSRoot:"/secret-root"}}}}
	b,err:=json.Marshal(r);if err!=nil{t.Fatal(err)}
	for _,secret:=range []string{"private-bucket","private-server","secret-root"}{if strings.Contains(string(b),secret){t.Fatalf("leaked %s",secret)}}
}
