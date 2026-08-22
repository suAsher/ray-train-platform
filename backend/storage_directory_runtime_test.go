package main

import (
	"testing"

	"ray-train-platform-backend/config"
)

func TestNewStorageDirectoryListerIsOptionalButRequiresCompleteTOSConfiguration(t *testing.T) {
	disabled, err := newStorageDirectoryLister(config.Config{})
	if err != nil || disabled != nil {
		t.Fatalf("empty TOS configuration should disable browser: lister=%v err=%v", disabled, err)
	}
	if _, err := newStorageDirectoryLister(config.Config{TOSEndpoint: "https://tos.example.com"}); err == nil {
		t.Fatal("partial TOS configuration was accepted")
	}
	enabled, err := newStorageDirectoryLister(config.Config{
		TOSEndpoint: "https://tos.example.com", TOSRegion: "cn-test", TOSBucket: "bucket", TOSAccessKey: "ak", TOSSecretKey: "sk",
	})
	if err != nil || enabled == nil {
		t.Fatalf("complete TOS configuration did not create browser: lister=%v err=%v", enabled, err)
	}
}
