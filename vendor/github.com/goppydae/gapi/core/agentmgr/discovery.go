package agentmgr

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"google.golang.org/protobuf/types/known/anypb"

	"github.com/goppydae/gapi/core/config"
	"github.com/goppydae/gapi/core/crypto"
	"github.com/goppydae/gapi/core/eventbus"
	"github.com/goppydae/gapi/core/lifecycle"
	"github.com/goppydae/gapi/core/schema"
	"github.com/goppydae/gapi/internal/logattr"
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
	bus            *eventbus.EventBus[*anypb.Any] // ← T is *anypb.Any
	lbus           *lifecycle.TypedBus
	pyRun          string // path to adk runner
	agents         map[string]Agent
	productionMode bool
	// verifyKey validates agent-binary signatures during discovery in
	// production mode (review R20). nil + productionMode means discovery
	// rejects every binary loudly - fail closed, never open.
	verifyKey ed25519.PublicKey
}

func NewAgentManager(bus *eventbus.EventBus[*anypb.Any], lbus *lifecycle.TypedBus, pyRunnerPath string, productionMode bool, verifyKey ed25519.PublicKey) *AgentManager {
	return &AgentManager{
		bus: bus, lbus: lbus, pyRun: pyRunnerPath, agents: map[string]Agent{}, productionMode: productionMode, verifyKey: verifyKey,
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
		slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "scanning agent search path", logattr.Module("discovery"), logattr.Path(searchPath), logattr.PathType(pathType.String()))

		// Discover agents from this path
		agents, err := am.discoverFromSinglePath(searchPath, pathType)
		if err != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "failed to scan agent search path", logattr.Module("discovery"), logattr.Path(searchPath), logattr.Err(err))
			continue
		}

		// Register agents (first occurrence wins)
		for _, agent := range agents {
			agentID := agent.ID()
			if _, exists := discovered[agentID]; exists {
				slog.Default().LogAttrs(context.Background(), slog.LevelDebug, "skipping agent already found in higher priority path", logattr.Module("discovery"), logattr.AgentID(agentID), logattr.Path(searchPath))
				continue
			}

			discovered[agentID] = agent
			am.Register(agent)
			out = append(out, agent.Describe())
			slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "registered agent", logattr.Module("discovery"), logattr.AgentID(agentID), logattr.Path(searchPath), logattr.PathType(pathType.String()))
		}
	}

	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "agent discovery complete", logattr.Module("discovery"), logattr.Count(len(discovered)))
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
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "agent metadata validation failed", logattr.Module("discovery"), logattr.Path(path), logattr.Err(err))
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
	// Python timers ship as either "<name>.py" (with TYPE=timer metadata) or
	// "<name>.py.timer"; matching only the ".py" suffix routed .py.timer
	// files into the Python-SERVICE branch, where the runner awaits a
	// readiness signal a timer module never sends (GAPI-DIV-021).
	isPythonTimer := strings.HasSuffix(path, ".py") || strings.HasSuffix(path, ".py.timer")
	if meta.Type == "timer" && isPythonTimer { // Python Timer
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
			am.productionMode, // added
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

// safeToExecute guards the discovery-time `--describe` execution of a candidate
// agent binary against TOCTOU / privilege-escalation via a writable agent search
// path. It rejects world-writable binaries or directories and binaries not owned
// by root or the running user, and in production mode requires a signature file
// to be present. Rejections are logged loudly because the caller treats a
// describe error as "not an agent" and silently skips it.
func (am *AgentManager) safeToExecute(binPath string) error {
	info, err := os.Stat(binPath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(binPath)
	dirInfo, err := os.Stat(dir)
	if err != nil {
		return err
	}

	reject := func(reason string) error {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "refusing to execute agent binary for --describe", logattr.Module("discovery"), logattr.Path(binPath), logattr.Reason(reason))
		return fmt.Errorf("unsafe agent binary %s: %s", binPath, reason)
	}

	if info.Mode().Perm()&0o002 != 0 {
		return reject("world-writable file")
	}
	if dirInfo.Mode().Perm()&0o002 != 0 {
		return reject("world-writable directory")
	}
	if err := checkOwner(info); err != nil {
		return reject(err.Error())
	}
	if err := checkOwner(dirInfo); err != nil {
		return reject("directory " + err.Error())
	}
	if am.productionMode {
		// Presence of a .sig is not provenance: verify it (review R20).
		if am.verifyKey == nil {
			return reject("production mode requires a verification public key (none configured)")
		}
		if err := crypto.VerifySignedBinary(binPath, am.verifyKey); err != nil {
			return reject(err.Error())
		}
	}
	return nil
}

// checkOwner requires the file to be owned by root (uid 0) or the current euid.
// On platforms without unix stat semantics, ownership is not checked.
func checkOwner(info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	euid := os.Geteuid()
	if int(st.Uid) != 0 && int(st.Uid) != euid {
		return fmt.Errorf("owned by uid %d (not root or current user %d)", st.Uid, euid)
	}
	return nil
}

func (am *AgentManager) binaryDescribe(binPath string) (*pyDescribe, error) {
	if err := am.safeToExecute(binPath); err != nil {
		return nil, err
	}

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
			return nil, fmt.Errorf("describe: timeout: %w\nstderr: %s\ncmd: %s",
				ctx.Err(), bytes.TrimSpace(stderr.Bytes()), cmdline)
		}
		return nil, fmt.Errorf("describe: exec error: %w\nstderr: %s\ncmd: %s",
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
		return nil, fmt.Errorf("describe: invalid JSON: %w\nstdout: %q\nstderr: %s\ncmd: %s",
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

// HardDepsOf and SoftDepsOf let the lifecycle controller distinguish hard
// (Requires) dependencies, whose failure blocks startup, from soft (Wants)
// dependencies, whose failure is advisory. Without this split, Dependencies()
// merges both and a failing Want would wrongly block start.
func (d depView) HardDepsOf(id string) []string {
	a := d.am.Get(id)
	if a == nil {
		return nil
	}
	return a.Requires()
}

func (d depView) SoftDepsOf(id string) []string {
	a := d.am.Get(id)
	if a == nil {
		return nil
	}
	return a.Wants()
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
