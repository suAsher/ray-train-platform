package nodeonboarding

import (
	"os"
	"strings"
	"testing"
)

func TestDockerfileUsesVerifiedBuilderAndScratch(t *testing.T) {
	baseline, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	var builder string
	for _, line := range strings.Split(string(baseline), "\n") {
		if strings.HasPrefix(line, "ARG GO_BUILDER_IMAGE=") {
			builder = line
			break
		}
	}
	if builder == "" {
		t.Fatal("verified backend builder not found")
	}
	raw, err := os.ReadFile("../node-onboarding.Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(raw)
	for _, required := range []string{builder, "ENV PATH=/usr/local/go/bin:$PATH", "ARG GOPROXY=", "GOPROXY=\"${GOPROXY}\"", "CGO_ENABLED=0", "FROM scratch", "/etc/ssl/certs/ca-certificates.crt", "USER 65532:65532"} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("missing build contract %q", required)
		}
	}
	for _, forbidden := range []string{"FROM golang:", "gcr.io/distroless", "apk add"} {
		if strings.Contains(dockerfile, forbidden) {
			t.Errorf("unexpected external runtime/package dependency %q", forbidden)
		}
	}
}
