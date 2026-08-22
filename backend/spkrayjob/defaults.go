package spkrayjob

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// catalogImage is the administrator-approved training environment as the
// platform's image catalogue returns it.
type catalogImage struct {
	Reference string `json:"reference"`
	Name      string `json:"name"`
	Framework string `json:"framework"`
	IsDefault bool   `json:"isDefault"`
}

func fixedClock() time.Time { return time.Date(2026, 8, 19, 21, 30, 0, 0, time.UTC) }

// defaultJobName names a run after the directory it was submitted from.
//
// The alternative is making the user invent a name for something that already
// has one. A per-run suffix is appended because RayJob names must be unique
// within a namespace, so resubmitting the same directory would otherwise
// collide with the previous run.
func defaultJobName(directory string, now func() time.Time) (string, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("resolve source directory: %w", err)
	}
	base := sanitizeDNSLabel(filepath.Base(absolute))
	suffix := now().UTC().Format("0102-1504")
	// 63 is the Kubernetes DNS label limit; reserve room for "-" and the suffix.
	if limit := 63 - len(suffix) - 1; len(base) > limit {
		base = strings.Trim(base[:limit], "-")
	}
	if base == "" {
		base = "job"
	}
	return base + "-" + suffix, nil
}

// sanitizeDNSLabel maps an arbitrary directory name onto the character set a
// Kubernetes DNS label allows. Non-ASCII names collapse to empty, which the
// caller replaces with a generic prefix.
func sanitizeDNSLabel(value string) string {
	var builder strings.Builder
	for _, char := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
			builder.WriteRune(char)
		case char == '-' || char == '_' || char == '.' || char == ' ':
			builder.WriteByte('-')
		}
	}
	collapsed := builder.String()
	for strings.Contains(collapsed, "--") {
		collapsed = strings.ReplaceAll(collapsed, "--", "-")
	}
	return strings.Trim(collapsed, "-")
}

func isDNSLabel(value string) bool {
	if value == "" || len(value) > 63 {
		return false
	}
	for index, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
		case char == '-' && index > 0 && index < len(value)-1:
		default:
			return false
		}
	}
	return true
}

// defaultImage picks the training environment when the user did not name one.
// Pasting a sha256 digest by hand is the most error-prone part of a
// submission, and the catalogue already records which entry is the default.
func defaultImage(images []catalogImage) (string, error) {
	usable := make([]catalogImage, 0, len(images))
	for _, image := range images {
		if strings.TrimSpace(image.Reference) != "" {
			usable = append(usable, image)
		}
	}
	if len(usable) == 0 {
		return "", fmt.Errorf("平台镜像目录为空；请让管理员登记训练镜像，或用 --image 指定")
	}
	for _, image := range usable {
		if image.IsDefault {
			return image.Reference, nil
		}
	}
	if len(usable) == 1 {
		return usable[0].Reference, nil
	}
	// Silently choosing among equally valid images would train in the wrong
	// environment, so the candidates are surfaced instead.
	lines := make([]string, 0, len(usable))
	for _, image := range usable {
		lines = append(lines, "  "+image.Reference+"   # "+image.Name)
	}
	return "", fmt.Errorf("镜像目录中有多个可选环境且没有默认项，请用 --image 指定：\n%s", strings.Join(lines, "\n"))
}
