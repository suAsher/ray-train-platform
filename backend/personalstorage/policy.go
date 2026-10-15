package personalstorage

import (
	"os"
	"path"
	"regexp"
	"strings"
)

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func ValidateOwner(o Owner) error {
	if strings.TrimSpace(o.ID) == "" || len(o.ID) > 256 || !usernamePattern.MatchString(o.Username) {
		return ErrInvalid
	}
	return nil
}
func ValidatePath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if len(p) > 4096 || strings.ContainsAny(p, "\\\x00\r\n") || strings.HasPrefix(p, "/") {
		return "", ErrInvalid
	}
	parts := strings.Split(p, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || len(part) > 255 {
			return "", ErrInvalid
		}
	}
	// Wellspiking exposes virtual/shared and internal entries at the account
	// root. Exclude only these exact root names; private hidden files and
	// same-named directories inside a private subtree remain valid.
	switch parts[0] {
	case ".minio", ".original", ".wellspiking", ".spk-hybrid", ".spk-ssd", ".shared", ".spiking-group-shared":
		return "", ErrInvalid
	}
	if path.Clean(p) != p {
		return "", ErrInvalid
	}
	return p, nil
}
func ValidateEntry(e Entry) error {
	if e.Name == "" || strings.ContainsAny(e.Name, "/\\\x00") || e.Name == "." || e.Name == ".." {
		return ErrForbidden
	}
	if e.Mode&os.ModeType != 0 && !e.Mode.IsDir() {
		return ErrForbidden
	}
	return nil
}
func safeError(err error) error {
	if err == nil {
		return nil
	}
	if os.IsNotExist(err) {
		return ErrNotFound
	}
	if os.IsExist(err) {
		return ErrConflict
	}
	if os.IsPermission(err) {
		return ErrForbidden
	}
	return ErrUnavailable
}
