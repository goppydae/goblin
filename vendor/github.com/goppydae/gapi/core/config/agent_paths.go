package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/goppydae/gapi/core/product"
)

// AgentSearchPaths returns the ordered list of directories to search for agents.
// Paths are searched in order, with earlier paths taking precedence.
//
// Every directory below is namespaced by the PRODUCT rather than by the
// kernel (GAPI-DIV-061): gapid searches /usr/lib/gapi/agents, goblind
// searches /usr/lib/goblin/agents. This function is reached inside
// goblind through agentmgr's discovery, so it is one of the four kernel
// surfaces an operator who has never heard of gapi would otherwise meet.
//
// Priority order (highest to lowest), with <p> the product name:
//  1. Development paths (<PREFIX>_DEV_AGENTS, ./agents)
//  2. User paths (XDG_DATA_HOME/<p>/agents, ~/.local/share/<p>/agents, ~/.<p>/agents)
//  3. System paths (/usr/local/lib/<p>/agents, /usr/lib/<p>/agents, /etc/<p>/agents)
//
// Environment variable overrides:
//   - <PREFIX>_AGENT_PATH: Replaces entire search path (colon-separated)
//   - <PREFIX>_DEV_AGENTS: Adds development path (highest priority)
//   - <PREFIX>_SKIP_SYSTEM_AGENTS: Skip system paths if set to "1" or "true"
func AgentSearchPaths() []string {
	// If <PREFIX>_AGENT_PATH is set, use it exclusively
	if customPath := os.Getenv(product.EnvKey("AGENT_PATH")); customPath != "" {
		return strings.Split(customPath, ":")
	}

	var paths []string
	skipSystem := os.Getenv(product.EnvKey("SKIP_SYSTEM_AGENTS")) == "1" ||
		os.Getenv(product.EnvKey("SKIP_SYSTEM_AGENTS")) == "true"

	// 1. Development paths (highest priority)
	if devPath := os.Getenv(product.EnvKey("DEV_AGENTS")); devPath != "" {
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
	// XDG_DATA_HOME/<product>/agents
	if xdgData := os.Getenv("XDG_DATA_HOME"); xdgData != "" {
		paths = append(paths, filepath.Join(xdgData, product.Name(), "agents"))
	} else if home := os.Getenv("HOME"); home != "" {
		// Fallback: ~/.local/share/<product>/agents
		paths = append(paths, filepath.Join(home, ".local", "share", product.Name(), "agents"))
	}

	// Legacy fallback: ~/.<product>/agents. os.UserHomeDir (not raw $HOME)
	// so the probed path is anchored at the platform home directory.
	if home, err := os.UserHomeDir(); err == nil {
		legacyPath := filepath.Join(home, "."+product.Name(), "agents")
		if _, err := os.Stat(legacyPath); err == nil {
			paths = append(paths, legacyPath)
		}
	}

	// 3. System paths (lowest priority)
	if !skipSystem {
		paths = append(paths, systemAgentDirs()...)

		// Optional: /etc/<product>/agents
		etcAgents := filepath.Join(product.ConfigDir(), "agents")
		if _, err := os.Stat(etcAgents); err == nil {
			paths = append(paths, etcAgents)
		}
	}

	return paths
}

// systemAgentDirs are the package-manager-owned agent directories, in
// search order. Shared with ClassifyPath so the set a path is CLASSIFIED
// against cannot drift from the set that is SEARCHED - they were two
// literal lists that disagreed about /etc before GAPI-DIV-061.
func systemAgentDirs() []string {
	return []string{
		filepath.Join("/usr/local/lib", product.Name(), "agents"),
		filepath.Join("/usr/lib", product.Name(), "agents"),
	}
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

	if devPath := os.Getenv(product.EnvKey("DEV_AGENTS")); devPath != "" {
		if strings.HasPrefix(absPath, devPath) {
			return PathTypeDevelopment
		}
	}

	// Check if it's a system path
	systemPrefixes := append(systemAgentDirs(), filepath.Join(product.ConfigDir(), "agents"))

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
