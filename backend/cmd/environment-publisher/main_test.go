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
	for _, host := range []string{"", registryauth.SecondaryHost} {
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
	for _, host := range []string{"evil.invalid", "https://" + registryauth.SecondaryHost, registryauth.SecondaryHost + ":443"} {
		_, err := run(context.Background(), registryauth.PublishRequest{}, t.TempDir(), host)
		if !errors.Is(err, registryauth.ErrInvalidTarget) || errorCode(err) != "REGISTRY_TARGET_INVALID" {
			t.Fatalf("invalid registry was not rejected before credential loading: %v", err)
		}
	}
}

func TestPublisherAcceptsOnlyOperatorApprovedCustomRegistry(t *testing.T) {
	const host = "harbor.custom.example"
	_, err := run(context.Background(), registryauth.PublishRequest{}, t.TempDir(), host)
	if !errors.Is(err, registryauth.ErrInvalidTarget) {
		t.Fatalf("unapproved custom registry reached credentials: %v", err)
	}
	_, err = run(context.Background(), registryauth.PublishRequest{}, t.TempDir(), host, host)
	if !errors.Is(err, registryauth.ErrCredentials) {
		t.Fatalf("approved custom registry did not reach credential loading: %v", err)
	}
}
