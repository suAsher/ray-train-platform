package modelevaluation

import (
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/api/resource"
	"ray-train-platform-backend/domain"
)

var identifierPattern=regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)
var sitePattern=regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var shaPattern=regexp.MustCompile(`^[0-9a-f]{64}$`)
var commitPattern=regexp.MustCompile(`^[0-9a-f]{40}$`)
var imagePattern=regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,511}$`)

func ValidateEvaluator(e Evaluator)error {
	if strings.TrimSpace(e.Name)==""||strings.TrimSpace(e.Name)!=e.Name||utf8.RuneCountInString(e.Name)>200||utf8.RuneCountInString(e.Description)>4000||strings.IndexFunc(e.Name,unicode.IsControl)>=0||!identifierPattern.MatchString(e.OwnerID)||!identifierPattern.MatchString(e.TenantID){return invalid("evaluator metadata is invalid")}
	if !imagePattern.MatchString(e.ImageReference)||strings.Contains(e.ImageReference,"://")||!strings.HasPrefix(e.ImageDigest,"sha256:")||!shaPattern.MatchString(strings.TrimPrefix(e.ImageDigest,"sha256:")){return invalid("evaluator image must have a fixed digest")}
	if _,digest,ok:=strings.Cut(e.ImageReference,"@");ok&&digest!=e.ImageDigest{return invalid("image reference and digest disagree")}
	gitURL,err:=url.Parse(e.GitURL)
	if err!=nil||len(e.GitURL)>2048||gitURL.Hostname()==""||gitURL.Path==""||gitURL.RawQuery!=""||gitURL.Fragment!=""||strings.ContainsAny(e.GitURL,"\r\n\x00 ")||(gitURL.Scheme!="https"&&gitURL.Scheme!="ssh"){return invalid("evaluator git URL is invalid")}
	if gitURL.User!=nil {if _,password:=gitURL.User.Password();password||gitURL.Scheme!="ssh"||gitURL.User.Username()!="git"{return invalid("git credentials must not be embedded")}}
	if !commitPattern.MatchString(e.GitCommit)||len(e.EntryPoint)<1||len(e.EntryPoint)>32||!identifierPattern.MatchString(e.SchemaVersion)||e.Protocol!=Protocol{return invalid("evaluator execution contract is invalid")}
	for _,arg:=range e.EntryPoint {if arg==""||len(arg)>1024||strings.IndexFunc(arg,unicode.IsControl)>=0{return invalid("evaluator argv is invalid")}}
	return nil
}
func CanonicalSites(sites []string)([]string,error) {
	if len(sites)>MaxSites{return nil,invalid("too many sites")}
	result:=append([]string{},sites...);sort.Strings(result)
	for i,site:=range result {if !sitePattern.MatchString(site)||(i>0&&site==result[i-1]){return nil,invalid("sites must be valid and unique")}}
	return result,nil
}
func ValidateResources(r domain.Resources)error {
	if r.WorkerReplicas!=1||r.GPUsPerWorker<1||r.GPUsPerWorker>8||r.CPUPerWorker<1||r.CPUPerWorker>32||len(r.MemoryPerWorker)>32{return invalid("evaluation resources exceed bounds")}
	if r.CPUPerWorker<int64(r.GPUsPerWorker){return invalid("evaluation requires at least one CPU per GPU worker")}
	memory,err:=resource.ParseQuantity(r.MemoryPerWorker);max:=resource.MustParse("128Gi")
	if err!=nil||memory.Sign()<=0||memory.Cmp(max)>0{return invalid("evaluation memory exceeds bounds")}
	return nil
}
func ValidateRequest(r Request)error {
	for _,id:=range []string{r.ModelID,r.VersionID,r.DatasetID,r.DatasetVersionID,r.EvaluatorID}{if !identifierPattern.MatchString(id){return invalid("resource selector is invalid")}}
	if r.Split!="val"&&r.Split!="test"{return invalid("evaluation requires val or test split")}
	if len(r.IdempotencyKey)<1||len(r.IdempotencyKey)>128{return invalid("idempotency key is required")}
	for _,c:=range r.IdempotencyKey {if c<33||c>126{return invalid("idempotency key is invalid")}}
	if _,err:=CanonicalSites(r.Sites);err!=nil{return err}
	if _,_,err:=CanonicalConfig(r.Config);err!=nil{return err}
	return ValidateResources(r.Resources)
}
func ValidateEvaluation(e Evaluation)error {
	for _,id:=range []string{e.ID,e.ModelID,e.VersionID,e.OwnerID,e.TenantID,e.Dataset.ID,e.Dataset.VersionID,e.Evaluator.ID}{if !identifierPattern.MatchString(id){return invalid("frozen identity is invalid")}}
	if !shaPattern.MatchString(e.ModelSHA256)||!shaPattern.MatchString(e.Dataset.ManifestSHA256)||!shaPattern.MatchString(e.ConfigSHA256){return invalid("frozen checksum is invalid")}
	if e.FileName==""||e.FileName=="."||e.FileName==".."||e.FileName!=path.Base(e.FileName)||len(e.FileName)>255||strings.ContainsAny(e.FileName,"\\\x00\r\n"){return invalid("model file name is invalid")}
	if (e.Dataset.Split!="val"&&e.Dataset.Split!="test")||e.Dataset.SampleCount<1||e.Dataset.SchemaVersion!=e.Evaluator.SchemaVersion{return invalid("dataset split or schema is incompatible")}
	if e.Dataset.Visibility!=Public&&e.Dataset.Visibility!=Team{return invalid("dataset visibility is invalid")}
	if (e.Dataset.Visibility==Public&&e.Dataset.TenantID!="")||(e.Dataset.Visibility==Team&&!identifierPattern.MatchString(e.Dataset.TenantID)){return invalid("dataset owner is invalid")}
	if _,err:=CanonicalSites(e.Dataset.Sites);err!=nil{return err}
	if err:=ValidateEvaluator(e.Evaluator);err!=nil{return err}
	_,hash,err:=CanonicalConfig(e.Config);if err!=nil{return err};if hash!=e.ConfigSHA256{return invalid("config checksum does not match")}
	switch e.State {case Creating,Submitted,Running,Succeeded,Failed,Cancelled:default:return invalid("evaluation state is invalid")}
	switch e.ReportState {case ReportPending,ReportValid,ReportInvalid,ReportMissing:default:return invalid("report state is invalid")}
	return ValidateResources(e.Resources)
}
// CanRead is a visibility projection for already authenticated, active members.
// Owning an evaluation never overrides the underlying dataset's team boundary.
func CanRead(e Evaluation,tenantID string,admin bool)bool {
	switch e.Dataset.Visibility {case Public:return e.Dataset.TenantID=="";case Team:return e.Dataset.TenantID!=""&&(admin||tenantID==e.Dataset.TenantID);default:return false}
}
func CanonicalConfigHash(raw json.RawMessage)(json.RawMessage,string,error){return CanonicalConfig(raw)}
