package rayapi

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
)

func TestLogRaySubmissionFailureRecordsStageAndOpaqueIDOnly(t *testing.T) {
	var output bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	logRaySubmissionFailure("submit", "native-opaque-id", errors.New("dataset version is not ready"))

	message := output.String()
	for _, expected := range []string{"stage=submit", `submission_id="native-opaque-id"`, "dataset version is not ready"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("log %q does not contain %q", message, expected)
		}
	}
}
