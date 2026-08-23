package spkrayjob

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"ray-train-platform-backend/domain"
)

func resolveProjectCache(ctx context.Context, cache projectCache, client *Client) (domain.CacheRequest, error) {
	mode := domain.CacheMode(strings.TrimSpace(cache.Mode))
	size := strings.TrimSpace(cache.Size)
	if err := validateProjectCacheShape(cache); err != nil {
		return domain.CacheRequest{}, err
	}
	if mode == "" || mode == domain.CacheModeOff {
		return domain.CacheRequest{}, nil
	}
	limits, err := client.PlatformLimits(ctx)
	if err != nil {
		return domain.CacheRequest{}, fmt.Errorf("读取平台缓存限制失败：%w", err)
	}
	if !limits.Cache.Enabled {
		return domain.CacheRequest{}, errors.New("平台未启用 runtime 临时缓存")
	}
	if !containsTrimmed(limits.Cache.Modes, string(domain.CacheModeRuntime)) {
		return domain.CacheRequest{}, errors.New("平台当前不支持 runtime 临时缓存模式")
	}
	if size == "" {
		size = strings.TrimSpace(limits.Cache.DefaultSize)
		if size == "" {
			return domain.CacheRequest{}, errors.New("平台未提供 runtime 临时缓存默认容量，请使用 --cache-size 指定")
		}
	}
	requested, err := positiveCacheQuantity(size)
	if err != nil {
		return domain.CacheRequest{}, fmt.Errorf("缓存容量 %q 无效，必须是正的 Kubernetes 容量：%w", size, err)
	}
	maximum, err := positiveCacheQuantity(limits.Cache.MaxSize)
	if err != nil {
		return domain.CacheRequest{}, errors.New("平台返回的缓存最大容量无效")
	}
	if requested.Cmp(maximum) > 0 {
		return domain.CacheRequest{}, fmt.Errorf("缓存容量 %q 超过平台上限 %q", size, strings.TrimSpace(limits.Cache.MaxSize))
	}
	for _, configured := range limits.Cache.AllowedSizes {
		allowed, err := positiveCacheQuantity(configured)
		if err == nil && requested.Cmp(allowed) == 0 {
			return domain.CacheRequest{Mode: domain.CacheModeRuntime, Size: strings.TrimSpace(configured)}, nil
		}
	}
	return domain.CacheRequest{}, fmt.Errorf("缓存容量 %q 不在平台允许范围内", size)
}

func validateProjectCacheShape(cache projectCache) error {
	mode := domain.CacheMode(strings.TrimSpace(cache.Mode))
	size := strings.TrimSpace(cache.Size)
	switch mode {
	case "", domain.CacheModeOff:
		if size != "" {
			return errors.New("缓存关闭时不能指定容量；请删除 cache.size 或改用 mode: runtime")
		}
		return nil
	case domain.CacheModeRuntime:
		return nil
	default:
		return fmt.Errorf("不支持的缓存模式 %q；可选值为 off 或 runtime", mode)
	}
}

func containsTrimmed(values []string, want string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == want {
			return true
		}
	}
	return false
}

func positiveCacheQuantity(value string) (resource.Quantity, error) {
	quantity, err := resource.ParseQuantity(strings.TrimSpace(value))
	if err != nil || quantity.Sign() <= 0 {
		return resource.Quantity{}, errors.New("容量必须大于零")
	}
	return quantity, nil
}
