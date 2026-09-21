// environment-assembler appends a validated environment layer to a fixed base.
// It never executes or extracts user tar contents, and receives only pull auth.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ray-train-platform-backend/registryauth"
)

func main() {
	var request registryauth.AssembleRequest
	var resultPath string
	flag.StringVar(&request.Base, "base", "", "server-configured immutable base image")
	flag.StringVar(&request.LayerPath, "layer", "/artifacts/layer.tar", "validated environment layer")
	flag.StringVar(&request.LayerDigest, "layer-digest", "", "frozen digest from the preparation Job result")
	flag.StringVar(&request.LayoutPath, "layout", "/artifacts/oci", "new OCI layout directory")
	flag.StringVar(&request.PullConfig, "pull-config", "/pull/.dockerconfigjson", "read-only robot Docker configuration")
	flag.StringVar(&resultPath, "result", "/dev/termination-log", "small sanitized result file")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	ctx, deadline := context.WithTimeout(ctx, 45*time.Minute)
	defer deadline()
	result, err := registryauth.NewClient().Assemble(ctx, request)
	if err != nil {
		result = registryauth.AssembleResult{ErrorCode: "OCI_ASSEMBLY_FAILED"}
	}
	data, _ := json.Marshal(result)
	if writeErr := os.WriteFile(resultPath, data, 0600); writeErr != nil {
		os.Stderr.WriteString("ASSEMBLY_RESULT_WRITE_FAILED\n")
		os.Exit(1)
	}
	if err != nil {
		os.Stderr.WriteString(result.ErrorCode + "\n")
		os.Exit(1)
	}
	os.Stdout.Write(data)
	os.Stdout.WriteString("\n")
}
