package modelserving

import (
	"encoding/json"
	"ray-train-platform-backend/domain"
	me "ray-train-platform-backend/modelevaluation"
	"strings"
	"testing"
	"time"
)

func validContract() Contract {
	return Contract{ID: "contract", Name: "Inference", OwnerID: "owner", TenantID: "team", ImageReference: "registry/image:v1", ImageDigest: "sha256:" + strings.Repeat("a", 64), Code: &me.CodeSnapshot{ID: strings.Repeat("b", 32), SHA256: strings.Repeat("c", 64), SizeBytes: 10, Format: "zip"}, EntryPoint: []string{"python", "serve.py"}, InputExample: json.RawMessage(`{"instances":[1]}`)}
}
func TestContractValidation(t *testing.T) {
	if err := ValidateContract(validContract()); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Contract){"missing-code": func(c *Contract) { c.Code = nil }, "oversized-example": func(c *Contract) {
		c.InputExample = json.RawMessage(`{"x":"` + strings.Repeat("a", MaxInputExampleBytes) + `"}`)
	}, "bad-json": func(c *Contract) { c.InputExample = json.RawMessage(`{`) }, "mutable-image": func(c *Contract) { c.ImageDigest = "latest" }, "shell-entry": func(c *Contract) { c.EntryPoint = []string{"sh", "-c", "python serve.py"} }, "flag-entry": func(c *Contract) { c.EntryPoint = []string{"python", "-c", "print(1)"} }, "escape-entry": func(c *Contract) { c.EntryPoint = []string{"python", "../serve.py"} }} {
		t.Run(name, func(t *testing.T) {
			c := validContract()
			change(&c)
			if ValidateContract(c) == nil {
				t.Fatal("invalid contract accepted")
			}
		})
	}
}
func TestDeploymentValidation(t *testing.T) {
	now := time.Now().UTC()
	d := Deployment{ID: "deployment", Name: "service", ReleaseID: "release", ModelID: "model", VersionID: "version", ModelSHA256: strings.Repeat("a", 64), ModelSizeBytes: 156, FileName: "weights.pt", OwnerID: "owner", TenantID: "team", JobID: "job-0123456789abcdef01234567", Contract: validContract(), Resources: domain.Resources{WorkerReplicas: 1, GPUsPerWorker: 1, CPUPerWorker: 4, MemoryPerWorker: "16Gi"}, ExpiresAt: now.Add(time.Hour), CreatedAt: now, IdempotencyKey: "request", RequestSHA256: strings.Repeat("b", 64)}
	if err := ValidateDeployment(d); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Deployment){"ttl-too-long": func(d *Deployment) { d.ExpiresAt = now.Add(8 * 24 * time.Hour) }, "ttl-too-short": func(d *Deployment) { d.ExpiresAt = now.Add(time.Minute) }, "multiworker": func(d *Deployment) { d.Resources.WorkerReplicas = 2 }, "multiple-gpu": func(d *Deployment) { d.Resources.GPUsPerWorker = 2 }, "no-gpu": func(d *Deployment) { d.Resources.GPUsPerWorker = 0 }, "traversal": func(d *Deployment) { d.FileName = "../weights.pt" }, "digest": func(d *Deployment) { d.ModelSHA256 = "bad" }, "missing-job": func(d *Deployment) { d.JobID = "" }, "wrong-job-shape": func(d *Deployment) { d.JobID = "job-" + strings.Repeat("0", 32) }} {
		t.Run(name, func(t *testing.T) {
			copy := d
			change(&copy)
			if ValidateDeployment(copy) == nil {
				t.Fatal("invalid deployment accepted")
			}
		})
	}
}
