package config

import (
	"errors"
	"regexp"
)

var assistantIdleNamespacePattern = regexp.MustCompile(`^raytrain-assistant-[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func ValidateAssistantIdleNamespace(namespace string) error {
	if namespace != "" && (len(namespace) > 63 || !assistantIdleNamespacePattern.MatchString(namespace)) {
		return errors.New("ASSISTANT_IDLE_NAMESPACE must be empty or a dedicated raytrain-assistant-* namespace")
	}
	return nil
}
