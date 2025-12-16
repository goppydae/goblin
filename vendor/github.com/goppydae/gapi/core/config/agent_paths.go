package config

import (
	"os"
	"path/filepath"
	"strings"
)

// AgentSearchPaths returns the ordered list of directories to search for agents.
// Paths are searched in order, with earlier paths taking precedence.
//
// Priority order (highest to lowest):
//  1. Development paths (GAPI_DEV_AGENTS, ./agents)
//  2. User paths (XDG_DATA_HOME/gapi/agents, ~/.local/share/gapi/agents, ~/.gapi/agents)
//  3. System paths (/usr/local/lib/gapi/agents, /usr/lib/gapi/agents)
//
// Environment variable overrides:
//   - GAPI_AGENT_PATH: Replaces entire search path (colon-separated)
//   - GAPI_DEV_AGENTS: Adds development path (highest priority)
//   - GAPI_SKIP_SYSTEM_AGENTS: Skip system paths if set to "1" or "true"
func AgentSearchPaths() []string {
	// If GAPI_AGENT_PATH is set, use it exclusively
	if customPath := os.Getenv("GAPI_AGENT_PATH"); customPath != "" {
		return strings.Split(customPath, ":")
	}

	var paths []string
	skipSystem := os.Getenv("GAPI_SKIP_SYSTEM_AGENTS") == "1" || os.Getenv("GAPI_SKIP_SYSTEM_AGENTS") == "true"

	// 1. Development paths (highest priority)
	if devPath := os.Getenv("GAPI_DEV_AGENTS"); devPath != "" {
		paths = append(paths, devPath)
	}

	// Current directory ./agents (development)
	if cwd, err := os.Getwd(); err == nil {
		// Built Go agents (highest/dev priority)
		buildDir := filepath.Join(cwd, "agents", "build", "go")
		if _, err := os.Stat(buildDir); err == nil {
			paths = append(paths, buildDir)
		}

		agentsDir := filepath.Join(cwd, "agents")
		if _, err := os.Stat(agentsDir); err == nil {
			paths = append(paths, agentsDir)
		}
	}

	// 2. User paths
	// XDG_DATA_HOME/gapi/agents
	if xdgData := os.Getenv("XDG_DATA_HOME"); xdgData != "" {
		paths = append(paths, filepath.Join(xdgData, "gapi", "agents"))
	} else if home := os.Getenv("HOME"); home != "" {
		// Fallback: ~/.local/share/gapi/agents
		paths = append(paths, filepath.Join(home, ".local", "share", "gapi", "agents"))
	}

	// Legacy fallback: ~/.gapi/agents
	if home := os.Getenv("HOME"); home != "" {
		legacyPath := filepath.Join(home, ".gapi", "agents")
		if _, err := os.Stat(legacyPath); err == nil {
			paths = append(paths, legacyPath)
		}
	}

	// 3. System paths (lowest priority)
	if !skipSystem {
		paths = append(paths,
			"/usr/local/lib/gapi/agents",
			"/usr/lib/gapi/agents",
		)

		// Optional: /etc/gapi/agents
		if _, err := os.Stat("/etc/gapi/agents"); err == nil {
			paths = append(paths, "/etc/gapi/agents")
		}
	}

	return paths
}

// PathType represents the type of agent path
type PathType int

const (
	PathTypeDevelopment PathType = iota
	PathTypeUser
	PathTypeSystem
)

// ClassifyPath determines the type of a given agent path
func ClassifyPath(path string) PathType {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return PathTypeDevelopment // Default to development if we can't determine
	}

	// Check if it's a development path
	if cwd, err := os.Getwd(); err == nil {
		if strings.HasPrefix(absPath, cwd) {
			return PathTypeDevelopment
		}
	}

	if devPath := os.Getenv("GAPI_DEV_AGENTS"); devPath != "" {
		if strings.HasPrefix(absPath, devPath) {
			return PathTypeDevelopment
		}
	}

	// Check if it's a system path
	systemPrefixes := []string{
		"/usr/lib/gapi/agents",
		"/usr/local/lib/gapi/agents",
		"/etc/gapi/agents",
	}

	for _, prefix := range systemPrefixes {
		if strings.HasPrefix(absPath, prefix) {
			return PathTypeSystem
		}
	}

	// Everything else is user path
	return PathTypeUser
}

// PathTypeString returns a human-readable string for the path type
func (pt PathType) String() string {
	switch pt {
	case PathTypeDevelopment:
		return "DEV"
	case PathTypeUser:
		return "USER"
	case PathTypeSystem:
		return "SYSTEM"
	default:
		return "UNKNOWN"
	}
}
