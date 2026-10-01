package storagesync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata"
)

var ambiguousPath = regexp.MustCompile(`(?i)%(?:2e|2f|5c|00|25)`)

func ValidateRelativePath(value string) error {
	if len(value)>4096 || strings.HasPrefix(value,"/") || strings.ContainsAny(value,"\\\x00\r\n") || ambiguousPath.MatchString(value) { return fmt.Errorf("%w: unsafe relative path",ErrInvalid) }
	if value=="" { return nil }
	for _,part:=range strings.Split(value,"/") { if part=="" || part=="." || part==".." { return fmt.Errorf("%w: unsafe path component",ErrInvalid) } }
	return nil
}
func (l Location) Validate() error {
	if strings.TrimSpace(l.SpaceID)=="" || len(l.SpaceID)>128 || strings.ContainsAny(l.SpaceID,"/\\\x00\r\n") || len(l.TenantID)>128 { return fmt.Errorf("%w: invalid logical storage space",ErrInvalid) }
	return ValidateRelativePath(l.RelativePath)
}
func (c Config) Validate() error {
	if c.Mode!="FULL" && c.Mode!="INCREMENTAL" { return fmt.Errorf("%w: unsupported mode",ErrInvalid) }
	if c.ConflictPolicy!="UPDATE" && c.ConflictPolicy!="FAIL" { return fmt.Errorf("%w: unsupported conflict policy",ErrInvalid) }
	if c.Verification!="METADATA" && c.Verification!="CONTENT" { return fmt.Errorf("%w: unsupported verification",ErrInvalid) }
	if c.Concurrency<1 || c.Concurrency>64 || c.BandwidthBytesPerSecond<0 || c.BandwidthBytesPerSecond>100_000_000_000 { return fmt.Errorf("%w: invalid resource limit",ErrInvalid) }
	if len(c.Mappings)<1 || len(c.Mappings)>32 { return fmt.Errorf("%w: require 1 to 32 mappings",ErrInvalid) }
	for _,m:=range c.Mappings {
		if err:=m.Source.Validate();err!=nil{return err};if err:=m.Destination.Validate();err!=nil{return err}
		if _,err:=DestinationPrefix(m.Source.RelativePath,m.Destination.RelativePath,m.Layout);err!=nil{return err}
	}
	return c.Schedule.Validate()
}
func (s Schedule) Validate() error {
	if s.Timezone!="Asia/Shanghai" { return fmt.Errorf("%w: timezone must be Asia/Shanghai",ErrInvalid) }
	switch s.Kind {
	case "MANUAL": if s.EveryHours!=0 || s.Time!="" || s.Weekday!=0{return ErrInvalid}
	case "INTERVAL": if s.EveryHours<1 || s.EveryHours>168 || s.Time!="" || s.Weekday!=0{return ErrInvalid}
	case "DAILY","WEEKLY":
		if _,err:=time.Parse("15:04",s.Time);err!=nil{return fmt.Errorf("%w: schedule time must be HH:MM",ErrInvalid)}
		if s.EveryHours!=0 || s.Weekday<0 || s.Weekday>6 || (s.Kind=="DAILY" && s.Weekday!=0){return ErrInvalid}
	default:return fmt.Errorf("%w: unsupported schedule",ErrInvalid)
	}
	return nil
}
func (s Schedule) Next(after time.Time)(*time.Time,error) {
	if err:=s.Validate();err!=nil{return nil,err};if s.Kind=="MANUAL"{return nil,nil}
	zone,err:=time.LoadLocation("Asia/Shanghai");if err!=nil{return nil,err};local:=after.In(zone)
	var next time.Time
	if s.Kind=="INTERVAL" {
		anchor:=time.Date(1970,1,1,0,0,0,0,zone);period:=time.Duration(s.EveryHours)*time.Hour
		next=anchor.Add((local.Sub(anchor)/period+1)*period)
	} else {
		clock,_:=time.Parse("15:04",s.Time);next=time.Date(local.Year(),local.Month(),local.Day(),clock.Hour(),clock.Minute(),0,0,zone)
		if s.Kind=="WEEKLY" { days:=(s.Weekday-int(local.Weekday())+7)%7;next=next.AddDate(0,0,days) }
		if !next.After(after) { days:=1;if s.Kind=="WEEKLY"{days=7};next=next.AddDate(0,0,days) }
	}
	result:=next.UTC();return &result,nil
}
func DestinationPrefix(source,destination,layout string)(string,error) {
	if err:=ValidateRelativePath(source);err!=nil{return "",err};if err:=ValidateRelativePath(destination);err!=nil{return "",err}
	switch layout {case "CONTENTS":return destination,nil;case "DIRECTORY":if source==""{return "",fmt.Errorf("%w: directory layout needs a source name",ErrInvalid)};return strings.TrimPrefix(path.Join(destination,path.Base(source)),"./"),nil;default:return "",fmt.Errorf("%w: unsupported copy layout",ErrInvalid)}
}
func PrefixesOverlap(a,b string)bool {
	a=strings.Trim(a,"/");b=strings.Trim(b,"/")
	return a=="" || b=="" || a==b || strings.HasPrefix(a,b+"/") || strings.HasPrefix(b,a+"/")
}
func LocksConflict(a,b PathLock)bool {
	return a.Region==b.Region && a.Bucket==b.Bucket && (a.Bucket!="" || a.StorageID==b.StorageID) && (a.Mode=="WRITE"||b.Mode=="WRITE") && PrefixesOverlap(a.Prefix,b.Prefix)
}
func LocksFor(mappings []ResolvedMapping)[]PathLock {
	out:=make([]PathLock,0,len(mappings)*2)
	for _,m:=range mappings { if m.Source.Kind=="TOS" {out=append(out,PathLock{StorageID:m.Source.StorageID,Region:m.Source.Region,Bucket:m.Source.Bucket,Prefix:m.Source.Prefix,Mode:"READ"})};out=append(out,PathLock{StorageID:m.Destination.StorageID,Region:m.Destination.Region,Bucket:m.Destination.Bucket,Prefix:m.Destination.Prefix,Mode:"WRITE"}) }
	return out
}
func ValidateResolved(mappings []ResolvedMapping)error {
	locks:=LocksFor(mappings)
	for _,m:=range mappings {
		if m.Source.Kind!="IDC"&&m.Source.Kind!="TOS"{return fmt.Errorf("%w: unsupported source",ErrInvalid)}
		if m.Destination.Kind!="TOS"||m.Destination.Bucket==""||m.Destination.StorageID==""{return fmt.Errorf("%w: destination must be registered TOS",ErrInvalid)}
		if m.Source.Kind=="TOS"&&(m.Source.Bucket==""||m.Source.Region!=m.Destination.Region){return fmt.Errorf("%w: copy must stay within one TOS region",ErrInvalid)}
	}
	for i,a:=range locks {for _,b:=range locks[i+1:] {if LocksConflict(a,b){return fmt.Errorf("%w: source or destination mappings overlap",ErrConflict)}}}
	return nil
}
func resolutionDigest(mappings []ResolvedMapping)string { b,_:=json.Marshal(mappings);sum:=sha256.Sum256(b);return "sha256:"+hex.EncodeToString(sum[:]) }
func (p Progress) Validate()error {
	for _,n:=range []int64{p.DiscoveredFiles,p.SourceFiles,p.SourceBytes,p.TransferFiles,p.TransferBytes,p.ReusedFiles,p.ReusedBytes,p.CompletedFiles,p.CompletedBytes,p.VerifiedFiles,p.VerifiedBytes,p.FailedFiles,p.TargetExtraFiles,p.InFlightBytes,p.NetworkBytes}{if n<0{return fmt.Errorf("%w: negative progress",ErrInvalid)}}
	if p.CompletedFiles>p.TransferFiles||p.CompletedBytes>p.TransferBytes||p.VerifiedFiles>p.SourceFiles||p.VerifiedBytes>p.SourceBytes{return fmt.Errorf("%w: progress exceeds frozen totals",ErrInvalid)}
	if p.ScanComplete && (p.TransferFiles>p.SourceFiles||p.ReusedFiles>p.SourceFiles-p.TransferFiles||p.TransferBytes>p.SourceBytes||p.ReusedBytes>p.SourceBytes-p.TransferBytes){return fmt.Errorf("%w: invalid manifest totals",ErrInvalid)}
	return nil
}
func (p Progress) Complete()bool {return p.Validate()==nil&&p.ScanComplete&&p.FailedFiles==0&&p.CompletedFiles==p.TransferFiles&&p.CompletedBytes==p.TransferBytes&&p.VerifiedFiles==p.SourceFiles&&p.VerifiedBytes==p.SourceBytes}
func (r Run) Active()bool{return r.FinishedAt==nil}
