package modelevaluation

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)
func validReport(e Evaluation) Report {
	return Report{Protocol:Protocol,EvaluationID:e.ID,ModelSHA256:e.ModelSHA256,DatasetManifestSHA256:e.Dataset.ManifestSHA256,EvaluatorID:e.Evaluator.ID,ConfigSHA256:e.ConfigSHA256,Metrics:[]Metric{{Name:"accuracy",Value:0,Unit:"%",Direction:Higher}}}
}
func TestReportBindsAllFrozenIdentitiesAndPreservesZero(t *testing.T) {
	e:=validEvaluation();report:=validReport(e);raw,_:=json.Marshal(report)
	parsed,hash,err:=ValidateReport(raw,e);if err!=nil||len(hash)!=64||parsed.Metrics[0].Value!=0{t.Fatalf("zero report invalid %+v %v",parsed,err)}
	for _,change:=range []func(*Report){func(r *Report){r.Protocol="other"},func(r *Report){r.EvaluationID="other"},func(r *Report){r.ModelSHA256=strings.Repeat("a",64)},func(r *Report){r.DatasetManifestSHA256=strings.Repeat("a",64)},func(r *Report){r.EvaluatorID="other"},func(r *Report){r.ConfigSHA256=strings.Repeat("a",64)}}{
		r:=report;change(&r);raw,_:=json.Marshal(r);if _,_,err:=ValidateReport(raw,e);!errors.Is(err,ErrInvalid){t.Fatal("report identity mismatch accepted")}
	}
}
func TestReportRejectsAmbiguousMetricsAndUnsafeText(t *testing.T) {
	e:=validEvaluation();report:=validReport(e)
	for _,metrics:=range [][]Metric{
		{},{{Name:"accuracy",Direction:Higher},{Name:"accuracy",Direction:Higher}},
		{{Name:"<script>alert(1)</script>",Direction:Higher}},{{Name:"https://evil.test",Direction:Higher}},{{Name:"accuracy",Unit:"javascript:alert(1)",Direction:Higher}},{{Name:"accuracy",Direction:"unknown"}},
	}{r:=report;r.Metrics=metrics;raw,_:=json.Marshal(r);if _,_,err:=ValidateReport(raw,e);!errors.Is(err,ErrInvalid){t.Fatalf("invalid metrics accepted: %+v",metrics)}}
	raw,_:=json.Marshal(report)
	for _,bad:=range []string{
		strings.Replace(string(raw),`"value":0`, `"value":null`,1),
		strings.Replace(string(raw),`"value":0,`, ``,1),
		strings.Replace(string(raw),`"value":0`, `"value":0,"value":1`,1),
		strings.Replace(string(raw),`"value":0`, `"value":1e999`,1),
		strings.Replace(string(raw),`"evaluationId":`, `"EvaluationId":`,1),
		strings.Replace(string(raw),`"metrics":`, `"extra":"hidden","metrics":`,1),
	}{if _,_,err:=ValidateReport([]byte(bad),e);!errors.Is(err,ErrInvalid){t.Fatalf("ambiguous report accepted: %.100s",bad)}}
}
func TestReportSlicesAreBoundedAndShareMetricMeaning(t *testing.T) {
	e:=validEvaluation();r:=validReport(e);r.Slices=[]Slice{{Name:"site-a",SampleCount:10,Metrics:[]Metric{{Name:"accuracy",Value:0,Unit:"%",Direction:Higher}}}}
	raw,_:=json.Marshal(r);if _,_,err:=ValidateReport(raw,e);err!=nil{t.Fatal(err)}
	r.Slices[0].Metrics[0].Direction=Lower;raw,_=json.Marshal(r);if _,_,err:=ValidateReport(raw,e);!errors.Is(err,ErrInvalid){t.Fatal("slice changed metric direction")}
	r.Slices[0].Metrics[0].Direction=Higher;r.Slices[0].SampleCount=11;raw,_=json.Marshal(r);if _,_,err:=ValidateReport(raw,e);!errors.Is(err,ErrInvalid){t.Fatal("slice exceeds frozen sample count")}
}
func TestCompareRequiresCompatibleSetupAndMetricMeaning(t *testing.T) {
	left:=validEvaluation();right:=validEvaluation();left.State=Succeeded;right.State=Succeeded;left.ReportState=ReportValid;right.ReportState=ReportValid
	lr:=validReport(left);rr:=validReport(right);rr.Metrics[0].Value=0.75;left.Report=&lr;right.Report=&rr
	comparison,err:=Compare(left,right);if err!=nil||!comparison.Comparable||comparison.Metrics[0].Outcome!="improved"||comparison.Metrics[0].Left!=0{t.Fatalf("bad zero comparison: %+v %v",comparison,err)}
	for _,change:=range []func(*Evaluation){func(e *Evaluation){e.Evaluator.GitCommit=strings.Repeat("f",40)},func(e *Evaluation){e.Dataset.Split="test"},func(e *Evaluation){e.Dataset.SampleCount++},func(e *Evaluation){e.Evaluator.EntryPoint=[]string{"python","other.py"}}}{r:=right;change(&r);comparison,err:=Compare(left,r);if err!=nil||comparison.Comparable||len(comparison.Reasons)==0{t.Fatalf("incompatible setup compared: %+v %v",comparison,err)}}
	rr.Metrics[0].Unit="ratio";comparison,err=Compare(left,right);if err!=nil||comparison.Comparable{t.Fatalf("units mismatch compared: %+v %v",comparison,err)}
	rr.Metrics[0].Unit="%";rr.Metrics[0].Value=math.Inf(1);if _,err:=Compare(left,right);!errors.Is(err,ErrInvalid){t.Fatal("non-finite cached report accepted")}
}

func TestComparisonOutcomesAndReadiness(t *testing.T) {
	left,right:=validEvaluation(),validEvaluation();left.State=Succeeded;right.State=Succeeded;left.ReportState=ReportValid;right.ReportState=ReportValid
	for _,tc:=range []struct{direction string;left,right float64;want string}{{Lower,1,0,"improved"},{Higher,1,0,"regressed"},{Neutral,0,1,"neutral"},{Higher,0,0,"equal"}} {
		lr,rr:=validReport(left),validReport(right);lr.Metrics[0].Direction=tc.direction;rr.Metrics[0].Direction=tc.direction;lr.Metrics[0].Value=tc.left;rr.Metrics[0].Value=tc.right;left.Report=&lr;right.Report=&rr
		result,err:=Compare(left,right);if err!=nil||!result.Comparable||result.Metrics[0].Outcome!=tc.want{t.Fatalf("wrong comparison outcome %+v %v",result,err)}
	}
	right.State=Failed;result,err:=Compare(left,right);if err!=nil||result.Comparable||len(result.Metrics)!=0{t.Fatal("failed evaluation compared")}
}
