package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ray-train-platform-backend/registryauth"
)

func TestAssemblerRejectsUnapprovedHostAndMismatchedBaseBeforeCredentials(t *testing.T) {
	const host = "harbor.custom.example"
	for _, target := range []string{"unapproved.example", "https://" + host, host + ":443"} {
		if _, err := assemble(context.Background(), registryauth.AssembleRequest{}, target, []string{host}); !errors.Is(err, registryauth.ErrInvalidTarget) {
			t.Fatalf("invalid destination reached assembly: %v", err)
		}
	}
	request := registryauth.AssembleRequest{Base: "other.example/project/base@sha256:" + strings.Repeat("a", 64)}
	if _, err := assemble(context.Background(), request, host, []string{host}); !errors.Is(err, registryauth.ErrInvalidTarget) {
		t.Fatalf("selected registry accepted another base origin: %v", err)
	}
}
