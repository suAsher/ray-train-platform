package k8s

import "fmt"

func capacitySliceHostnames(encoded map[string]any, count int) ([]string,error) {
	result:=make([]string,count)
	if value,found:=encoded["universal"];found {
		hostname,ok:=value.(string)
		if !ok || hostname=="" || len(hostname)>253 {return nil,fmt.Errorf("invalid universal hostname")}
		if _,found:=encoded["individual"];found{return nil,fmt.Errorf("ambiguous hostname encoding")}
		for index:=range result {result[index]=hostname}
		return result,nil
	}
	individual,ok:=encoded["individual"].(map[string]any)
	if !ok {return nil,fmt.Errorf("missing individual hostname encoding")}
	prefix,suffix:="",""
	if value,found:=individual["prefix"];found {prefix,ok=value.(string);if !ok{return nil,fmt.Errorf("invalid hostname prefix")}}
	if value,found:=individual["suffix"];found {suffix,ok=value.(string);if !ok{return nil,fmt.Errorf("invalid hostname suffix")}}
	if len(prefix)+len(suffix)>253 {return nil,fmt.Errorf("invalid hostname affixes")}
	roots,ok:=individual["roots"].([]any)
	if !ok || len(roots)!=count {return nil,fmt.Errorf("hostname root count mismatch")}
	for index,value:=range roots {
		root,ok:=value.(string)
		if !ok || len(prefix)+len(root)+len(suffix)>253 || len(prefix)+len(root)+len(suffix)==0 {return nil,fmt.Errorf("invalid hostname root")}
		result[index]=prefix+root+suffix
	}
	return result,nil
}

func capacitySlicePodCounts(encoded map[string]any,count int)([]int64,error) {
	result:=make([]int64,count)
	if value,found:=encoded["universal"];found {
		number,valid:=capacityInteger(value)
		if !valid || number<=0 || number>100000 {return nil,fmt.Errorf("invalid universal pod count")}
		if _,found:=encoded["individual"];found{return nil,fmt.Errorf("ambiguous pod count encoding")}
		for index:=range result {result[index]=number}
		return result,nil
	}
	values,ok:=encoded["individual"].([]any)
	if !ok || len(values)!=count{return nil,fmt.Errorf("individual pod count mismatch")}
	for index,value:=range values {
		number,valid:=capacityInteger(value)
		if !valid || number<=0 || number>100000{return nil,fmt.Errorf("invalid individual pod count")}
		result[index]=number
	}
	return result,nil
}
