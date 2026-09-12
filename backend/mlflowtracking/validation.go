package mlflowtracking

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var keyPattern = regexp.MustCompile(`^[a-zA-Z0-9._:-]{1,128}$`)
var metricPattern = regexp.MustCompile(`^[a-zA-Z0-9._/-]{1,128}$`)

func validID(id string) bool   { return idPattern.MatchString(id) }
func validKey(key string) bool { return keyPattern.MatchString(key) }
func validName(name string) bool {
	if name == "" || name != strings.TrimSpace(name) || len(name) > 128 || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validActor(actor Actor) bool { return validName(actor.TenantID) && validName(actor.UserID) }

func (pair *Pair) UnmarshalJSON(data []byte) error {
	var raw struct {
		Key   *string `json:"key"`
		Value *string `json:"value"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	if raw.Key == nil || raw.Value == nil {
		return ErrInvalid
	}
	*pair = Pair{Key: *raw.Key, Value: *raw.Value}
	return nil
}

func (batch Batch) Validate() error {
	if len(batch.Metrics)+len(batch.Params)+len(batch.Tags) == 0 || len(batch.Metrics) > 100 || len(batch.Params) > 100 || len(batch.Tags) > 100 {
		return ErrInvalid
	}
	for _, metric := range batch.Metrics {
		if !writableKey(metric.Key) || metric.Value == nil || metric.Timestamp == nil || metric.Step == nil {
			return ErrInvalid
		}
		if math.IsNaN(*metric.Value) || math.IsInf(*metric.Value, 0) || *metric.Timestamp < 0 || *metric.Timestamp > 253402300799999 || *metric.Step < 0 {
			return ErrInvalid
		}
	}
	for _, group := range []struct {
		pairs []Pair
		max   int
	}{{batch.Params, 1024}, {batch.Tags, 5000}} {
		seen := map[string]bool{}
		for _, pair := range group.pairs {
			if !writableKey(pair.Key) || len(pair.Value) > group.max || !utf8.ValidString(pair.Value) || seen[pair.Key] {
				return ErrInvalid
			}
			seen[pair.Key] = true
		}
	}
	encoded, err := json.Marshal(batch)
	if err != nil || len(encoded) > 256*1024 {
		return ErrInvalid
	}
	return nil
}

func writableKey(key string) bool {
	if !metricPattern.MatchString(key) {
		return false
	}
	lower := strings.ToLower(key)
	if strings.HasPrefix(lower, "platform.") || strings.HasPrefix(lower, "mlflow.") {
		return false
	}
	switch strings.NewReplacer("_", "", "-", "").Replace(lower) {
	case "runid", "runname", "experimentid", "experimentname", "jobid", "tenantid", "submitteruserid", "userid", "ownerid", "artifacturi", "provenance", "status", "starttime", "endtime":
		return false
	}
	return true
}
