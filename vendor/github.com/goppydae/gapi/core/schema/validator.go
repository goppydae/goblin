// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package schema

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/goppydae/gapi/core/cgroups"
)

// Version represents a semantic version
type Version struct {
	Major uint32
	Minor uint32
	Patch uint32
}

// Current schema version
var CurrentVersion = Version{Major: 1, Minor: 0, Patch: 0}

// ValidAgentTypes are the supported agent unit types
var ValidAgentTypes = []string{"service", "timer", "socket", "pipe", "event", "init", "oneshot"}

// AgentDescribe represents agent metadata for validation
type AgentDescribe struct {
	SchemaVersion string
	ID            string
	Type          string
	CPULimit      string
	MemoryLimit   string
	Schedule      string
	ListenStream  string
	Requires      []string
	Wants         []string
	WantedBy      []string
	RequiredBy    []string
	Capabilities  []string
}

// ValidateAgentDescribe validates agent metadata
func ValidateAgentDescribe(desc AgentDescribe) error {
	// Validate required fields
	if err := validateID(desc.ID); err != nil {
		return fmt.Errorf("invalid id: %w", err)
	}

	if err := validateType(desc.Type); err != nil {
		return fmt.Errorf("invalid type: %w", err)
	}

	// Validate dependencies
	if err := validateDependencies("requires", desc.Requires); err != nil {
		return err
	}
	if err := validateDependencies("wants", desc.Wants); err != nil {
		return err
	}
	if err := validateDependencies("wanted_by", desc.WantedBy); err != nil {
		return err
	}
	if err := validateDependencies("required_by", desc.RequiredBy); err != nil {
		return err
	}

	// Validate capabilities (if any specific format is needed, currently just alphanumeric check like IDs)
	if err := validateCapabilities(desc.Capabilities); err != nil {
		return err
	}

	// Validate optional fields if present
	if desc.CPULimit != "" {
		if err := ValidateCPULimit(desc.CPULimit); err != nil {
			return fmt.Errorf("invalid cpu_limit: %w", err)
		}
	}

	if desc.MemoryLimit != "" {
		if err := ValidateMemoryLimit(desc.MemoryLimit); err != nil {
			return fmt.Errorf("invalid memory_limit: %w", err)
		}
	}

	if desc.Schedule != "" {
		if err := ValidateSchedule(desc.Schedule); err != nil {
			return fmt.Errorf("invalid schedule: %w", err)
		}
	}

	if desc.ListenStream != "" {
		if err := validateListenStream(desc.ListenStream); err != nil {
			return fmt.Errorf("invalid listen_stream: %w", err)
		}
	}

	return nil
}

// validateID ensures the agent ID is valid
func validateID(id string) error {
	if id == "" {
		return fmt.Errorf("id cannot be empty")
	}

	// Valid identifier: alphanumeric, underscore, hyphen
	validID := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	if !validID.MatchString(id) {
		return fmt.Errorf("id must contain only alphanumeric characters, underscores, and hyphens")
	}

	return nil
}

// validateDependencies ensures all dependency IDs are valid
func validateDependencies(field string, deps []string) error {
	for _, dep := range deps {
		if err := validateID(dep); err != nil {
			return fmt.Errorf("invalid dependency in %s: %q: %w", field, dep, err)
		}
	}
	return nil
}

// validateCapabilities ensures all capabilities are valid strings
func validateCapabilities(caps []string) error {
	for _, c := range caps {
		if len(c) == 0 {
			return fmt.Errorf("capability cannot be empty")
		}
		// We can be more permissive with capabilities, but let's stick to basic sanity
		if len(c) > 64 {
			return fmt.Errorf("capability too long: %q", c)
		}
	}
	return nil
}

// validateType ensures the agent type is supported
func validateType(typ string) error {
	if typ == "" {
		return fmt.Errorf("type cannot be empty")
	}

	typ = strings.ToLower(typ)
	for _, valid := range ValidAgentTypes {
		if typ == valid {
			return nil
		}
	}

	return fmt.Errorf("type must be one of: %s", strings.Join(ValidAgentTypes, ", "))
}

// ValidateCPULimit validates CPU limit format
// Accepts: "0.5", "500m", "1", "1.5"
//
// It does not implement the format. It asks cgroups.ParseResourceSpec -
// the same function that converts the string for cgroups.Create at
// start - and accepts exactly what that function can represent as a
// POSITIVE quantity. Acceptance therefore means the limit will actually
// be applied, which is a property nothing enforced while the two sides
// were separate implementations (GAPI-DIV-049).
//
// The cycle noted in ValidateSchedule below is schema -> agentmgr and
// does not apply here: core/cgroups imports only internal/safeio.
func ValidateCPULimit(limit string) error {
	spec, err := cgroups.ParseResourceSpec(limit, "")
	if err != nil {
		return fmt.Errorf("%w (use format: 0.5, 500m, or 1)", err)
	}
	// A blank limit parses without error and means "no limit". As a
	// validated FIELD it is still wrong: the caller only reaches here
	// when the manifest set cpu_limit to something.
	if spec.CPU <= 0 {
		return fmt.Errorf("invalid cpu limit: %q (use format: 0.5, 500m, or 1)", limit)
	}
	return nil
}

// ValidateMemoryLimit validates memory limit format
// Accepts: "100MB", "1GB", "512M", "1G", "1024B"
//
// Delegates to cgroups.ParseResourceSpec for the same reason
// ValidateCPULimit does: the accepted set and the representable set
// must be one set. The overflow rejection GAPI-DIV-042 added lives
// there now, next to the multiplication that overflows.
func ValidateMemoryLimit(limit string) error {
	spec, err := cgroups.ParseResourceSpec("", limit)
	if err != nil {
		return fmt.Errorf("%w (use format: 100MB, 1GB, etc.)", err)
	}
	if spec.Memory <= 0 {
		return fmt.Errorf("invalid memory limit: %q (use format: 100MB, 1GB, etc.)", limit)
	}
	return nil
}

// ValidateSchedule validates systemd-style timer schedule or cron expression
// Accepts: "OnUnitActiveSec=5s", "OnBootSec=30s", "OnStartupSec=1m", "*/5 * * * *", "@hourly", etc.
func ValidateSchedule(schedule string) error {
	schedule = strings.TrimSpace(schedule)

	if schedule == "" {
		return fmt.Errorf("schedule cannot be empty")
	}

	// Use the agentmgr.ParseSchedule function for validation
	// This validates all supported formats: systemd-style, cron, and raw durations
	// Note: This creates a circular dependency, so we'll duplicate the validation logic

	// Try systemd-style
	if strings.HasPrefix(schedule, "OnUnitActiveSec=") ||
		strings.HasPrefix(schedule, "OnBootSec=") ||
		strings.HasPrefix(schedule, "OnStartupSec=") {
		durStr := strings.TrimPrefix(schedule, "OnUnitActiveSec=")
		durStr = strings.TrimPrefix(durStr, "OnBootSec=")
		durStr = strings.TrimPrefix(durStr, "OnStartupSec=")
		_, err := time.ParseDuration(durStr)
		return err
	}

	// Try raw duration
	if _, err := time.ParseDuration(schedule); err == nil {
		return nil
	}

	// Assume it's a cron expression - basic validation
	// Reject anything that looks like "Key=Value" but wasn't caught above
	if strings.Contains(schedule, "=") {
		return fmt.Errorf("invalid schedule format: unknown prefix in %q", schedule)
	}

	if len(schedule) == 0 {
		return fmt.Errorf("invalid schedule format")
	}

	return nil
}

// validateListenStream validates socket address format
// Accepts: "0.0.0.0:8080", "127.0.0.1:9000", ":8080"
func validateListenStream(addr string) error {
	addr = strings.TrimSpace(addr)

	if addr == "" {
		return fmt.Errorf("listen_stream cannot be empty")
	}

	// Basic format check: should contain ":"
	if !strings.Contains(addr, ":") {
		return fmt.Errorf("invalid address format (use host:port or :port)")
	}

	parts := strings.Split(addr, ":")
	if len(parts) != 2 {
		return fmt.Errorf("invalid address format (use host:port or :port)")
	}

	// Validate port
	port, err := strconv.Atoi(parts[1])
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid port: %s (must be 1-65535)", parts[1])
	}

	return nil
}

// ParseVersion parses a version string like "1.0.0"
func ParseVersion(v string) (Version, error) {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("invalid version format: %s (expected major.minor.patch)", v)
	}

	major, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil {
		return Version{}, fmt.Errorf("invalid major version: %s", parts[0])
	}

	minor, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return Version{}, fmt.Errorf("invalid minor version: %s", parts[1])
	}

	patch, err := strconv.ParseUint(parts[2], 10, 32)
	if err != nil {
		return Version{}, fmt.Errorf("invalid patch version: %s", parts[2])
	}

	return Version{
		Major: uint32(major),
		Minor: uint32(minor),
		Patch: uint32(patch),
	}, nil
}

// CheckSchemaVersion validates schema version compatibility
func CheckSchemaVersion(v Version) error {
	// Major version must match
	if v.Major != CurrentVersion.Major {
		return fmt.Errorf("incompatible schema version: %d.%d.%d (expected %d.x.x)",
			v.Major, v.Minor, v.Patch, CurrentVersion.Major)
	}

	// Minor version can be older (backward compatible)
	if v.Minor > CurrentVersion.Minor {
		return fmt.Errorf("schema version too new: %d.%d.%d (current: %d.%d.%d)",
			v.Major, v.Minor, v.Patch,
			CurrentVersion.Major, CurrentVersion.Minor, CurrentVersion.Patch)
	}

	return nil
}
