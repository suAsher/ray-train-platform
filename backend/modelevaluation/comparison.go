package modelevaluation

import (
	"encoding/json"
	"math"
	"sort"

	"ray-train-platform-backend/domain"
)

// ComparisonFingerprint excludes model identity and result values, but includes
// every fixed input that can change what an evaluator measures.
func ComparisonFingerprint(e Evaluation)string {
	if err:=ValidateEvaluation(e);err!=nil{return ""}
	sites,_:=CanonicalSites(e.Dataset.Sites)
	setup:=struct {
		DatasetID,DatasetVersionID,ManifestSHA256,SchemaVersion,Split string
		Sites []string
		SampleCount int64
		EvaluatorID,ImageReference,ImageDigest,GitURL,GitCommit,EvaluatorSchema,Protocol,ConfigSHA256 string
		EntryPoint []string
		Resources domain.Resources
	}{e.Dataset.ID,e.Dataset.VersionID,e.Dataset.ManifestSHA256,e.Dataset.SchemaVersion,e.Dataset.Split,sites,e.Dataset.SampleCount,e.Evaluator.ID,e.Evaluator.ImageReference,e.Evaluator.ImageDigest,e.Evaluator.GitURL,e.Evaluator.GitCommit,e.Evaluator.SchemaVersion,e.Evaluator.Protocol,e.ConfigSHA256,e.Evaluator.EntryPoint,e.Resources}
	encoded,err:=json.Marshal(setup);if err!=nil{return ""};return hashJSON(encoded)
}
func Compare(left,right Evaluation)(Comparison,error) {
	result:=Comparison{LeftID:left.ID,RightID:right.ID,Reasons:[]string{},Metrics:[]MetricComparison{}}
	for _,evaluation:=range []Evaluation{left,right} {
		if err:=ValidateEvaluation(evaluation);err!=nil{return result,err}
		if evaluation.State!=Succeeded||evaluation.ReportState!=ReportValid||evaluation.Report==nil{result.Reasons=append(result.Reasons,"Both evaluations require a succeeded job and a valid report.");return result,nil}
		if err:=validateReportValue(*evaluation.Report,evaluation);err!=nil{return result,err}
	}
	if ComparisonFingerprint(left)!=ComparisonFingerprint(right){result.Reasons=append(result.Reasons,"Dataset selection, evaluator executable, or configuration differs.");return result,nil}
	leftMetrics,_:=metricMap(left.Report.Metrics);rightMetrics,_:=metricMap(right.Report.Metrics)
	if len(leftMetrics)!=len(rightMetrics){result.Reasons=append(result.Reasons,"Metric sets differ.");return result,nil}
	names:=make([]string,0,len(leftMetrics))
	for name,leftMetric:=range leftMetrics {rightMetric,exists:=rightMetrics[name];if !exists||leftMetric.Unit!=rightMetric.Unit||leftMetric.Direction!=rightMetric.Direction{result.Reasons=append(result.Reasons,"Metric names, units, or directions differ.");return result,nil};names=append(names,name)}
	sort.Strings(names)
	for _,name:=range names {
		l,r:=leftMetrics[name],rightMetrics[name];delta:=r.Value-l.Value
		if math.IsInf(delta,0)||math.IsNaN(delta){return result,invalid("metric difference exceeds numeric bounds")}
		outcome:="equal";if delta!=0{outcome="neutral";if l.Direction!=Neutral{outcome="regressed";if (l.Direction==Higher&&delta>0)||(l.Direction==Lower&&delta<0){outcome="improved"}}}
		result.Metrics=append(result.Metrics,MetricComparison{Name:name,Unit:l.Unit,Direction:l.Direction,Left:l.Value,Right:r.Value,Delta:delta,Outcome:outcome})
	}
	result.Comparable=true;return result,nil
}
