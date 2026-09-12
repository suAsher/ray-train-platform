package integrations

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func ValidID(value string) bool { return idPattern.MatchString(value) }
func ValidName(value string) bool {
	if value != strings.TrimSpace(value) || len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func NormalizeScopes(scopes []string) ([]string, error) {
	return normalizeAccess(scopes, "experiments:read", "experiments:write", map[string]bool{"experiments:read": true, "experiments:write": true, "artifacts:read": true, "artifacts:write": true})
}
func NormalizePermissions(permissions []string) ([]string, error) {
	return normalizeAccess(permissions, "read", "write", map[string]bool{"read": true, "write": true, "artifacts:read": true, "artifacts:write": true})
}
func normalizeAccess(values []string, read, write string, allowed map[string]bool) ([]string, error) {
	if len(values) == 0 || len(values) > 4 {
		return nil, ErrInvalid
	}
	unique := map[string]bool{}
	for _, v := range values {
		if !allowed[v] {
			return nil, ErrInvalid
		}
		unique[v] = true
	}
	if !unique[read] || (unique[write] && !unique[read]) || (unique["artifacts:write"] && !unique["artifacts:read"]) {
		return nil, ErrInvalid
	}
	result := make([]string, 0, len(unique))
	for v := range unique {
		result = append(result, v)
	}
	sort.Strings(result)
	return result, nil
}
func HasPermission(permissions []string, value string) bool {
	for _, p := range permissions {
		if p == value {
			return true
		}
	}
	return false
}
func PermissionsFromScopes(scopes []string) ([]string, error) {
	normalized, err := NormalizeScopes(scopes)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(normalized))
	for _, scope := range normalized {
		switch scope {
		case "experiments:read":
			result = append(result, "read")
		case "experiments:write":
			result = append(result, "write")
		default:
			result = append(result, scope)
		}
	}
	return NormalizePermissions(result)
}
