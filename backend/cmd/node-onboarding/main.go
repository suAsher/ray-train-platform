package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"ray-train-platform-backend/nodeonboarding"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "prepare" {
		if err := nodeonboarding.PrepareHost(); err != nil {
			log.Fatal(err)
		}
		return
	}
	var config nodeonboarding.Config
	var lease string
	flag.StringVar(&config.Namespace, "namespace", "", "dedicated probe namespace (required)")
	flag.StringVar(&config.ConfigNamespace, "config-namespace", "", "external config namespace (defaults to probe namespace)")
	flag.StringVar(&config.ProbeServiceAccount, "probe-service-account", "node-onboarding-probe", "probe service account")
	flag.StringVar(&config.Data1ConfigMap, "data1-config-map", "", "external data1 runtime ConfigMap (required)")
	flag.StringVar(&config.Data2ConfigMap, "data2-config-map", "", "external data2 runtime ConfigMap (required)")
	flag.StringVar(&config.Data1StorageClass, "data1-storage-class", "ray-cache-local-data1", "data1 StorageClass")
	flag.StringVar(&config.Data2StorageClass, "data2-storage-class", "ray-cache-local-data2", "data2 StorageClass")
	flag.StringVar(&config.Image, "image", "", "immutable controller image (required)")
	flag.StringVar(&config.HelperImage, "helper-image", "", "immutable busybox image (required)")
	flag.StringVar(&config.NFSConfigMap, "nfs-config-map", "", "ConfigMap with shares.json (required)")
	flag.StringVar(&config.StateConfigMap, "state-config-map", "", "protected controller proof ConfigMap (required)")
	flag.StringVar(&lease, "leader-election-lease", "node-onboarding", "leader election Lease")
	flag.DurationVar(&config.RevalidateAfter, "revalidate-after", 6*time.Hour, "periodic storage revalidation interval")
	flag.DurationVar(&config.RetryAfter, "retry-after", 5*time.Minute, "failed probe retry cooldown")
	flag.Parse()
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal(err)
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		log.Fatal(err)
	}
	controller, err := nodeonboarding.NewController(client, config)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	hostname, err := os.Hostname()
	if err != nil {
		log.Fatal(err)
	}
	if err := controller.Run(ctx, lease, hostname); err != nil {
		log.Fatal(err)
	}
}
