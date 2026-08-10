package domain

import "fmt"

func ValidateSourceArtifactSHA256(value string) error {
	if !artifactSHA256.MatchString(value) {
		return fmt.Errorf("sha256 must be 64 lowercase hexadecimal characters")
	}
	return nil
}
