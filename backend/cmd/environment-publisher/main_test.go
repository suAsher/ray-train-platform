package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"testing"

	"ray-train-platform-backend/registryauth"
)

func TestPublisherFlagsPreserveLegacyDefaultAndSelectQomolo(t *testing.T) {
	for _, host := range []string{"", registryauth.QomoloHost} {
		flags := flag.NewFlagSet("publisher", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		options := publisherFlags(flags)
		args := []string{}
		want := registryauth.Host
		if host != "" {
			args = []string{"--registry-host", host}
			want = host
		}
		if err := flags.Parse(args); err != nil {
			t.Fatal(err)
		}
		if options.registryHost != want || options.credentialsDirectory != "/registry-credentials" || options.resultPath != "/dev/termination-log" {
			t.Fatal("publisher flags did not preserve selected registry/defaults")
		}
	}
}

func TestPublisherRejectsInvalidRegistryBeforeReadingCredentials(t *testing.T) {
	for _, host := range []string{"evil.invalid", "https://" + registryauth.QomoloHost, registryauth.QomoloHost + ":443"} {
		_, err := run(context.Background(), registryauth.PublishRequest{}, t.TempDir(), host)
		if !errors.Is(err, registryauth.ErrInvalidTarget) || errorCode(err) != "REGISTRY_TARGET_INVALID" {
			t.Fatalf("invalid registry was not rejected before credential loading: %v", err)
		}
	}
}
