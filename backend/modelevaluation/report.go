package modelevaluation

import (
	"encoding/json"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

func ValidateReport(raw json.RawMessage,e Evaluation)(Report,string,error) {
	if err:=ValidateEvaluation(e);err!=nil{return Report{},"",err}
	value,err:=strictJSON(raw,MaxReportBytes);if err!=nil{return Report{},"",err}
	object,ok:=value.(map[string]any);if !ok||!exactKeys(object,[]string{"protocol","evaluationId","modelSha256","datasetManifestSha256","evaluatorId","configSha256","metrics"},[]string{"slices"}){return Report{},"",invalid("report fields do not match protocol")}
	if err:=metricShape(object["metrics"]);err!=nil{return Report{},"",err}
	if value,exists:=object["slices"];exists {
		slices,ok:=value.([]any);if !ok||len(slices)>MaxSlices{return Report{},"",invalid("slices exceed limits")}
		for _,value:=range slices {slice,ok:=value.(map[string]any);if !ok||!exactKeys(slice,[]string{"name","sampleCount","metrics"},nil){return Report{},"",invalid("slice fields are invalid")};if _,ok:=slice["sampleCount"].(json.Number);!ok{return Report{},"",invalid("slice sample count is required")};if err:=metricShape(slice["metrics"]);err!=nil{return Report{},"",err}}
	}
	var report Report
	if err:=json.Unmarshal(raw,&report);err!=nil{return Report{},"",invalid("report field types are invalid")}
	if err:=validateReportValue(report,e);err!=nil{return Report{},"",err}
	canonical,err:=json.Marshal(report);if err!=nil{return Report{},"",invalid("report cannot be encoded")}
	return report,hashJSON(canonical),nil
}
func exactKeys(object map[string]any,required,optional []string)bool {
	allowed:=make(map[string]bool,len(required)+len(optional));for _,key:=range required {allowed[key]=true;if value,exists:=object[key];!exists||value==nil{return false}}
	for _,key:=range optional{allowed[key]=true};for key:=range object{if !allowed[key]{return false}};return true
}
func metricShape(value any)error {
	metrics,ok:=value.([]any);if !ok||len(metrics)<1||len(metrics)>MaxMetrics{return invalid("metrics exceed limits")}
	for _,value:=range metrics {metric,ok:=value.(map[string]any);if !ok||!exactKeys(metric,[]string{"name","value","unit","direction"},nil){return invalid("metric fields are invalid")};if _,ok:=metric["value"].(json.Number);!ok{return invalid("metric value must be a finite number")}}
	return nil
}
func safeLabel(text string,max int,allowEmpty bool)bool {
	if text==""{return allowEmpty};if strings.TrimSpace(text)!=text||utf8.RuneCountInString(text)>max{return false}
	for _,r:=range text {if unicode.IsLetter(r)||unicode.IsDigit(r)||strings.ContainsRune(" _-./()%@^",r){continue};return false}
	return !strings.Contains(strings.ToLower(text),"www.")&&!strings.Contains(text,"//")
}
func metricMap(metrics []Metric)(map[string]Metric,error) {
	if len(metrics)<1||len(metrics)>MaxMetrics{return nil,invalid("metrics exceed limits")};result:=make(map[string]Metric,len(metrics))
	for _,metric:=range metrics {
		if !safeLabel(metric.Name,128,false)||!safeLabel(metric.Unit,32,true)||math.IsNaN(metric.Value)||math.IsInf(metric.Value,0){return nil,invalid("metric name, unit, or value is invalid")}
		if metric.Direction!=Higher&&metric.Direction!=Lower&&metric.Direction!=Neutral{return nil,invalid("metric direction is invalid")}
		if _,exists:=result[metric.Name];exists{return nil,invalid("metric names must be unique")};result[metric.Name]=metric
	}
	return result,nil
}
func validateReportValue(report Report,e Evaluation)error {
	if report.Protocol!=Protocol||report.Protocol!=e.Evaluator.Protocol||report.EvaluationID!=e.ID||report.ModelSHA256!=e.ModelSHA256||report.DatasetManifestSHA256!=e.Dataset.ManifestSHA256||report.EvaluatorID!=e.Evaluator.ID||report.ConfigSHA256!=e.ConfigSHA256{return invalid("report identity does not match frozen evaluation")}
	metrics,err:=metricMap(report.Metrics);if err!=nil{return err}
	if len(report.Slices)>MaxSlices{return invalid("too many report slices")};names:=map[string]bool{}
	for _,slice:=range report.Slices {
		if !safeLabel(slice.Name,128,false)||names[slice.Name]||slice.SampleCount<0||slice.SampleCount>e.Dataset.SampleCount{return invalid("slice identity or count is invalid")};names[slice.Name]=true
		sliceMetrics,err:=metricMap(slice.Metrics);if err!=nil{return err}
		if len(sliceMetrics)!=len(metrics){return invalid("slice metric set differs from report")}
		for name,value:=range sliceMetrics{base,exists:=metrics[name];if !exists||base.Unit!=value.Unit||base.Direction!=value.Direction{return invalid("slice metric meaning differs from report")}}
	}
	return nil
}
