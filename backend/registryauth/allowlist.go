package registryauth

// AllowedHost validates a destination against deployment-owned configuration.
// A syntactically valid hostname is never sufficient authorization to send
// credentials. Empty configuration only retains the non-routable legacy default.
func AllowedHost(host string, allowed []string) (string, error) {
	if len(allowed) == 0 {
		allowed = []string{Host}
	}
	if host == "" {
		host = allowed[0]
	}
	resolved, err := NormalizeHost(host)
	if err != nil {
		return "", ErrInvalidTarget
	}
	for _, candidate := range allowed {
		if candidate == resolved && candidate != "" {
			return resolved, nil
		}
	}
	return "", ErrInvalidTarget
}
