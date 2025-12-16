package agentmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/anypb"

	"github.com/goppydae/gapi/core/config"
	"github.com/goppydae/gapi/core/schema"
	"github.com/goppydae/gapi/internal/eventbus"
	"github.com/goppydae/gapi/internal/lifecycle"
)

type Discovered struct {
	ID           string
	Type         string
	Lang         string
	Path         string
	Requires     []string
	Wants        []string
	WantedBy     []string
	RequiredBy   []string
	ListenStream string
	Capabilities []string
}

type pyDescribe struct {
	Describe struct {
		ID           string   `json:"id"`
		Type         string   `json:"type"`
		Requires     []string `json:"requires"`
		Wants        []string `json:"wants"`
		WantedBy     []string `json:"wanted_by"`
		RequiredBy   []string `json:"required_by"`
		Enabled      bool     `json:"enabled"`
		ListenStream string   `json:"listen_stream"`
		CPULimit     string   `json:"cpu_limit"`
		MemoryLimit  string   `json:"memory_limit"`
		Schedule     string   `json:"schedule"` // For timer agents
		Capabilities []string `json:"capabilities"`
	} `json:"describe"`
}

type Agent interface {
	ID() string
	Type() string
	Lang() string
	Dependencies() []string
	Controller() *lifecycle.Controller
	Describe() map[string]string
	Requires() []string
	Wants() []string
	SetRunID(string)
}

type AgentManager struct {
	bus    *eventbus.EventBus[*anypb.Any] // ← T is *anypb.Any
	lbus   *lifecycle.TypedBus
	pyRun  string // path to adk runner
	agents map[string]Agent
}

func NewAgentManager(bus *eventbus.EventBus[*anypb.Any], lbus *lifecycle.TypedBus, pyRunnerPath string) *AgentManager {
	return &AgentManager{
		bus: bus, lbus: lbus, pyRun: pyRunnerPath, agents: map[string]Agent{},
	}
}

func (am *AgentManager) Register(a Agent)    { am.agents[a.ID()] = a }
func (am *AgentManager) Get(id string) Agent { return am.agents[id] }
func (am *AgentManager) All() map[string]Agent {
	out := make(map[string]Agent, len(am.agents))
	for k, v := range am.agents {
		out[k] = v
	}
	return out
}

func (am *AgentManager) TopologicalSort() ([]string, error) {
	return TopologicalSort(am.agents)
}

// DiscoverFromPaths discovers agents from all configured search paths.
// Paths are searched in priority order (Development → User → System).
// First occurrence of an agent ID wins (higher priority path).
func (am *AgentManager) DiscoverFromPaths() ([]map[string]string, error) {
	searchPaths := config.AgentSearchPaths()
	discovered := make(map[string]Agent) // Track by ID to enforce first-match-wins
	var out []map[string]string

	for _, searchPath := range searchPaths {
		// Skip non-existent paths
		if _, err := os.Stat(searchPath); os.IsNotExist(err) {
			continue
		}

		pathType := config.ClassifyPath(searchPath)
		println(fmt.Sprintf("[Discovery] Scanning %s [%s]", searchPath, pathType))

		// Discover agents from this path
		agents, err := am.discoverFromSinglePath(searchPath, pathType)
		if err != nil {
			println(fmt.Sprintf("[Discovery] Warning: failed to scan %s: %v", searchPath, err))
			continue
		}

		// Register agents (first occurrence wins)
		for _, agent := range agents {
			agentID := agent.ID()
			if _, exists := discovered[agentID]; exists {
				println(fmt.Sprintf("[Discovery] Skipping %s from %s (already found in higher priority path)", agentID, searchPath))
				continue
			}

			discovered[agentID] = agent
			am.Register(agent)
			out = append(out, agent.Describe())
			println(fmt.Sprintf("[Discovery] Registered %s from %s [%s]", agentID, searchPath, pathType))
		}
	}

	println(fmt.Sprintf("[Discovery] Total agents discovered: %d", len(discovered)))
	return out, nil
}

// DiscoverFromPath discovers agents from a single root path (legacy method for backward compatibility).
// Prefer DiscoverFromPaths() for new code.
func (am *AgentManager) DiscoverFromPath(root string) ([]map[string]string, error) {
	pathType := config.ClassifyPath(root)
	agents, err := am.discoverFromSinglePath(root, pathType)
	if err != nil {
		return nil, err
	}

	var out []map[string]string
	for _, agent := range agents {
		am.Register(agent)
		out = append(out, agent.Describe())
	}
	return out, nil
}

// discoverFromSinglePath scans a single directory for agents.
func (am *AgentManager) discoverFromSinglePath(root string, pathType config.PathType) ([]Agent, error) {
	var agents []Agent

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		// Python agents: *.py.service, *.py.timer, *.py.socket
		if strings.Contains(d.Name(), ".py.") && (strings.HasSuffix(d.Name(), ".service") || strings.HasSuffix(d.Name(), ".timer") || strings.HasSuffix(d.Name(), ".socket")) {
			// Handle Python Agent
			desc, err := am.pythonDescribe(p)
			if err != nil {
				// println(err.Error()) // quiet for mixed folders
				return nil
			}
			return am.processDiscovered(p, desc.Describe, &agents)
		}

		// Go/Binary Agents: executable and not a source/config file
		// Naive check: executable bit and no extension or .bin?
		// Better: try to run --describe on anything executable that isn't excluded.
		// Exclude known extensions
		ext := filepath.Ext(d.Name())
		if ext == ".go" || ext == ".md" || ext == ".json" || ext == ".b3" || ext == ".sig" {
			return nil
		}

		if info.Mode()&0111 != 0 {
			// Executable
			desc, err := am.binaryDescribe(p)
			if err != nil {
				// Not a GAPI agent binary
				return nil
			}
			return am.processDiscovered(p, desc.Describe, &agents)
		}

		return nil
	})

	return agents, err
}

func (am *AgentManager) processDiscovered(path string, d struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	Requires     []string `json:"requires"`
	Wants        []string `json:"wants"`
	WantedBy     []string `json:"wanted_by"`
	RequiredBy   []string `json:"required_by"`
	Enabled      bool     `json:"enabled"`
	ListenStream string   `json:"listen_stream"`
	CPULimit     string   `json:"cpu_limit"`
	MemoryLimit  string   `json:"memory_limit"`
	Schedule     string   `json:"schedule"`
	Capabilities []string `json:"capabilities"`
}, agents *[]Agent) error {

	// Validate agent metadata
	if err := schema.ValidateAgentDescribe(schema.AgentDescribe{
		ID:           d.ID,
		Type:         d.Type,
		CPULimit:     d.CPULimit,
		MemoryLimit:  d.MemoryLimit,
		Schedule:     d.Schedule,
		ListenStream: d.ListenStream,
		Requires:     d.Requires,
		Wants:        d.Wants,
		WantedBy:     d.WantedBy,
		RequiredBy:   d.RequiredBy,
		Capabilities: d.Capabilities,
	}); err != nil {
		println(fmt.Sprintf("validation failed for %s: %v", path, err))
		return nil
	}

	meta := Discovered{
		ID: d.ID, Type: strings.ToLower(d.Type),
		Path:         path,
		Requires:     append([]string(nil), d.Requires...),
		Wants:        append([]string(nil), d.Wants...),
		WantedBy:     append([]string(nil), d.WantedBy...),
		RequiredBy:   append([]string(nil), d.RequiredBy...),
		ListenStream: d.ListenStream,
		Capabilities: append([]string(nil), d.Capabilities...),
	}

	var a Agent
	if meta.Type == "timer" && strings.HasSuffix(path, ".py") { // Python Timer
		schedule := d.Schedule
		if schedule == "" {
			schedule = "OnUnitActiveSec=60s"
		}
		a = NewTimerAgent(meta.ID, meta.Path, schedule, am.pyRun, am.bus, am.lbus)
	} else if strings.HasSuffix(path, ".py") || strings.Contains(filepath.Base(path), ".py.") { // Python Service
		a = NewPythonAgent(
			meta.ID, meta.Type, meta.Path, am.pyRun,
			meta.Requires, meta.Wants, meta.WantedBy, meta.RequiredBy,
			d.ListenStream,
			d.CPULimit,
			d.MemoryLimit,
			meta.Capabilities,
			am.bus,
			depView{am},
		)
	} else {
		// Go/Binary Agent
		a = NewGoAgent(
			meta.ID, meta.Type, meta.Path,
			meta.Requires, meta.Wants, meta.WantedBy, meta.RequiredBy,
			d.ListenStream,
			d.CPULimit,
			d.MemoryLimit,
			meta.Capabilities,
			am.bus,
			depView{am},
		)
	}

	*agents = append(*agents, a)
	return nil
}

func (am *AgentManager) binaryDescribe(binPath string) (*pyDescribe, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "--describe")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, err
	}

	out := bytes.TrimSpace(stdout.Bytes())
	if len(out) == 0 {
		return nil, fmt.Errorf("empty output")
	}

	var d pyDescribe
	if err := json.Unmarshal(out, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (am *AgentManager) pythonDescribe(modulePath string) (*pyDescribe, error) {
	runnerAbs, err := filepath.Abs(am.pyRun)
	if err != nil {
		return nil, fmt.Errorf("describe: abs runner: %w", err)
	}
	modAbs, err := filepath.Abs(modulePath)
	if err != nil {
		return nil, fmt.Errorf("describe: abs module: %w", err)
	}
	if _, err := os.Stat(runnerAbs); err != nil {
		return nil, fmt.Errorf("describe: runner not found: %s (%w)", runnerAbs, err)
	}
	if _, err := os.Stat(modAbs); err != nil {
		return nil, fmt.Errorf("describe: module not found: %s (%w)", modAbs, err)
	}

	pythonBin := "python"
	if _, err := exec.LookPath(pythonBin); err != nil {
		if _, err3 := exec.LookPath("python3"); err3 == nil {
			pythonBin = "python3"
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, pythonBin, runnerAbs, "--module", modAbs, "--describe")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	cmdline := fmt.Sprintf("%s %s --module %s --describe", pythonBin, runnerAbs, modAbs)

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("describe: runner failed (code=%d): %s\ncmd: %s",
				exitErr.ExitCode(), bytes.TrimSpace(stderr.Bytes()), cmdline)
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("describe: timeout: %v\nstderr: %s\ncmd: %s",
				ctx.Err(), bytes.TrimSpace(stderr.Bytes()), cmdline)
		}
		return nil, fmt.Errorf("describe: exec error: %v\nstderr: %s\ncmd: %s",
			err, bytes.TrimSpace(stderr.Bytes()), cmdline)
	}

	out := bytes.TrimSpace(stdout.Bytes())
	out = bytes.TrimPrefix(out, []byte{0xEF, 0xBB, 0xBF})
	if len(out) == 0 {
		return nil, fmt.Errorf("describe: empty stdout; stderr: %s\ncmd: %s",
			bytes.TrimSpace(stderr.Bytes()), cmdline)
	}

	var d pyDescribe
	if err := json.Unmarshal(out, &d); err != nil {
		return nil, fmt.Errorf("describe: invalid JSON: %v\nstdout: %q\nstderr: %s\ncmd: %s",
			err, string(out), bytes.TrimSpace(stderr.Bytes()), cmdline)
	}
	return &d, nil
}

// DependencyResolver for lifecycle
type depView struct{ am *AgentManager }

func (d depView) DepsOf(id string) []string {
	a := d.am.Get(id)
	if a == nil {
		return nil
	}
	return a.Dependencies()
}
func (d depView) IsRunning(id string) bool {
	a := d.am.Get(id)
	if a == nil {
		return false
	}
	return a.Controller().State() == lifecycle.StateRunning
}

func (d depView) EnsureStarted(ctx context.Context, id string) error {
	a := d.am.Get(id)
	if a == nil {
		return fmt.Errorf("dependency %q not found", id)
	}
	// Recursive call via controller
	return a.Controller().ApplyWithContext(ctx, lifecycle.ActionStart)
}
