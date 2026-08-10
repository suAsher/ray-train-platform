package k8s

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (c *Client) ValidateRayCapabilities(ctx context.Context, clusterSpecField string) error {
	if c == nil || c.discovery == nil || c.dynamic == nil {
		return fmt.Errorf("Kubernetes discovery is not initialized")
	}
	resources, err := c.discovery.ServerResourcesForGroupVersion("ray.io/v1")
	if err != nil {
		return fmt.Errorf("discover ray.io/v1: %w", err)
	}
	if !hasResource(resources.APIResources, "rayjobs", "get", "create", "delete") {
		return fmt.Errorf("ray.io/v1 rayjobs resource does not support required verbs")
	}
	if !hasResource(resources.APIResources, "rayclusters", "get", "create", "delete") {
		return fmt.Errorf("ray.io/v1 rayclusters resource does not support required verbs")
	}
	var kueueDiscoveryErr error
	for _, version := range []string{"v1beta2", "v1beta1", "v1"} {
		resources, err := c.discovery.ServerResourcesForGroupVersion("kueue.x-k8s.io/" + version)
		if err != nil {
			kueueDiscoveryErr = err
			continue
		}
		if !hasResource(resources.APIResources, "localqueues", "get", "create") {
			kueueDiscoveryErr = fmt.Errorf("Kueue %s does not expose localqueues with required verbs", version)
			continue
		}
		c.kueueGVR = schema.GroupVersionResource{Group: "kueue.x-k8s.io", Version: version, Resource: "localqueues"}
		kueueDiscoveryErr = nil
		break
	}
	if kueueDiscoveryErr != nil {
		return fmt.Errorf("discover Kueue API: %w", kueueDiscoveryErr)
	}
	if clusterSpecField == "" {
		clusterSpecField = "rayClusterSpec"
	}
	return c.validateRayJobCRDSchema(ctx, clusterSpecField)
}

func (c *Client) validateRayJobCRDSchema(ctx context.Context, clusterSpecField string) error {
	crd, err := c.dynamic.Resource(crdGVR).Get(ctx, "rayjobs.ray.io", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get RayJob CRD schema: %w", err)
	}
	versions, found, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
	if err != nil || !found {
		return fmt.Errorf("RayJob CRD has no version schema")
	}
	for _, item := range versions {
		version, ok := item.(map[string]any)
		if !ok || version["served"] == false {
			continue
		}
		schema, found, _ := unstructured.NestedMap(version, "schema", "openAPIV3Schema", "properties", "spec", "properties")
		if found {
			if _, exists := schema[clusterSpecField]; exists {
				return nil
			}
		}
	}
	return fmt.Errorf("RayJob CRD schema does not contain spec.%s", clusterSpecField)
}

func hasResource(resources []metav1.APIResource, name string, verbs ...string) bool {
	for _, resource := range resources {
		if resource.Name != name {
			continue
		}
		for _, required := range verbs {
			found := false
			for _, verb := range resource.Verbs {
				if verb == required {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	return false
}

var crdGVR = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
