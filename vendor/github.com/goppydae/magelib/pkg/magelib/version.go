package magelib

import (
	"os"
	"strings"
)

// Version resolves the build version by, in order: explicit environment
// override (RELEASE_VERSION), the release tag when building from a tag ref
// (GITHUB_REF_NAME, only when GITHUB_REF_TYPE=tag), the root VERSION file,
// and finally "dev". The VERSION file is the single source of version truth
// in the repo; the earlier steps exist for release automation.
func Version() string {
	if v := os.Getenv("RELEASE_VERSION"); v != "" {
		return v
	}
	if os.Getenv("GITHUB_REF_TYPE") == "tag" {
		if v := os.Getenv("GITHUB_REF_NAME"); v != "" {
			return v
		}
	}
	if data, err := os.ReadFile("VERSION"); err == nil {
		if v := strings.TrimSpace(string(data)); v != "" {
			return v
		}
	}
	return "dev"
}
