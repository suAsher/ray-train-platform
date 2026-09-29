package environmentbuild

import "ray-train-platform-backend/registryauth"

type RegistryCapability struct {
	Host            string `json:"host"`
	Label           string `json:"label"`
	CredentialType  string `json:"credentialType"`
	CredentialLabel string `json:"credentialLabel"`
	CredentialHint  string `json:"credentialHint"`
}

func normalizedRegistryHosts(hosts []string) ([]string, error) {
	if len(hosts) == 0 {
		return []string{RegistryHost}, nil
	}
	result := make([]string, 0, len(hosts))
	seen := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		resolved, err := registryauth.NormalizeHost(host)
		if err != nil || host == "" || seen[resolved] {
			return nil, ErrInvalid
		}
		seen[resolved] = true
		result = append(result, resolved)
	}
	return result, nil
}

// registryHost validates both the fixed trusted origins and this service's
// configured subset before any user credential can reach a registry.
func (s *Service) registryHost(host string) (string, error) {
	resolved, err := registryauth.NormalizeHost(host)
	if err != nil {
		return "", ErrInvalid
	}
	if len(s.config.RegistryHosts) == 0 && resolved == RegistryHost {
		return resolved, nil
	}
	for _, allowed := range s.config.RegistryHosts {
		if allowed == resolved {
			return resolved, nil
		}
	}
	return "", ErrInvalid
}

func (s *Service) registryCapabilities() []RegistryCapability {
	items := make([]RegistryCapability, 0, len(s.config.RegistryHosts))
	for _, host := range s.config.RegistryHosts {
		item := RegistryCapability{Host: host, Label: "Wellspiking Harbor", CredentialType: "cli_secret", CredentialLabel: "CLI Secret", CredentialHint: "使用 Wellspiking Harbor 用户名和个人资料中的 CLI Secret"}
		if host == "harbor.qomolo.com" {
			item.Label = "Qomolo Harbor"
			item.CredentialType = "password"
			item.CredentialLabel = "密码"
			item.CredentialHint = "使用 Qomolo Harbor 用户名和账号密码"
		}
		items = append(items, item)
	}
	return items
}
