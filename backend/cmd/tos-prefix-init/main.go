package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path"
	"strings"

	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

var trainingPrefixes = []string{
	"ray-train/tenants/local/datasets/",
	"ray-train/tenants/local/checkpoints/",
	"ray-train/tenants/local/outputs/",
}

type config struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	Prefixes  []string
}

func main() {
	settings, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatal("invalid TOS prefix initializer configuration")
	}
	client, err := tos.NewClientV2(settings.Endpoint,
		tos.WithRegion(settings.Region),
		tos.WithCredentials(tos.NewStaticCredentials(settings.AccessKey, settings.SecretKey)),
	)
	if err != nil {
		log.Fatal("initialize TOS prefix client")
	}
	if err := createPrefixList(context.Background(), settings.Bucket, settings.Prefixes, func(ctx context.Context, bucket, key string) error {
		_, err := client.PutObjectV2(ctx, &tos.PutObjectV2Input{
			PutObjectBasicInput: tos.PutObjectBasicInput{Bucket: bucket, Key: key, ContentLength: 0},
			Content:             bytes.NewReader(nil),
		})
		return err
	}); err != nil {
		log.Printf("TOS prefix initialization rejected (status=%d code=%s request_id=%s message=%q)", tos.StatusCode(err), tos.Code(err), tos.RequestID(err), tosErrorMessage(err))
		log.Fatal("initialize TOS training prefixes")
	}
	if err := verifyPrefixList(context.Background(), settings.Bucket, settings.Prefixes, func(ctx context.Context, bucket, prefix string) error {
		_, err := client.ListObjectsType2(ctx, &tos.ListObjectsType2Input{
			Bucket: bucket, Prefix: prefix, Delimiter: "/", MaxKeys: 1,
		})
		return err
	}); err != nil {
		log.Printf("TOS prefix browse validation rejected (status=%d code=%s request_id=%s message=%q)", tos.StatusCode(err), tos.Code(err), tos.RequestID(err), tosErrorMessage(err))
		log.Fatal("verify TOS training prefix browse access")
	}
	fmt.Printf("initialized %d ray-train TOS prefixes\n", len(settings.Prefixes))
}

func tosErrorMessage(err error) string {
	if serverErr, ok := err.(*tos.TosServerError); ok {
		return serverErr.Message
	}
	return ""
}

func loadConfig(getenv func(string) string) (config, error) {
	settings := config{
		Endpoint:  strings.TrimSpace(getenv("TOS_ENDPOINT")),
		Region:    strings.TrimSpace(getenv("TOS_REGION")),
		Bucket:    strings.TrimSpace(getenv("TOS_BUCKET")),
		AccessKey: strings.TrimSpace(getenv("TOS_ACCESS_KEY")),
		SecretKey: strings.TrimSpace(getenv("TOS_SECRET_KEY")),
		Prefixes:  append([]string(nil), trainingPrefixes...),
	}
	if settings.Endpoint == "" || settings.Region == "" || settings.Bucket == "" || settings.AccessKey == "" || settings.SecretKey == "" {
		return config{}, fmt.Errorf("required TOS settings are missing")
	}
	if raw := strings.TrimSpace(getenv("TOS_PREFIXES")); raw != "" {
		settings.Prefixes = nil
		for _, item := range strings.Split(raw, ",") {
			prefix := strings.TrimSpace(item)
			base := strings.TrimSuffix(prefix, "/")
			if base == "" || strings.HasPrefix(base, "/") || strings.Contains(base, "\\") || path.Clean(base) != base || len(prefix) > 1024 {
				return config{}, fmt.Errorf("TOS_PREFIXES contains an unsafe object prefix")
			}
			settings.Prefixes = append(settings.Prefixes, base+"/")
		}
	}
	return settings, nil
}

func createPrefixes(ctx context.Context, bucket string, put func(context.Context, string, string) error) error {
	return createPrefixList(ctx, bucket, trainingPrefixes, put)
}

func createPrefixList(ctx context.Context, bucket string, prefixes []string, put func(context.Context, string, string) error) error {
	for _, prefix := range prefixes {
		if err := put(ctx, bucket, prefix); err != nil {
			return err
		}
	}
	return nil
}

// verifyPrefixes proves the minimum ListBucket capability needed by the
// Portal directory picker before any root becomes eligible for publication.
func verifyPrefixes(ctx context.Context, bucket string, list func(context.Context, string, string) error) error {
	return verifyPrefixList(ctx, bucket, trainingPrefixes, list)
}

func verifyPrefixList(ctx context.Context, bucket string, prefixes []string, list func(context.Context, string, string) error) error {
	for _, prefix := range prefixes {
		if err := list(ctx, bucket, prefix); err != nil {
			return err
		}
	}
	return nil
}
