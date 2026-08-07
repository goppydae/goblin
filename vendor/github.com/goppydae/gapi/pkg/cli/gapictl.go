// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/goppydae/gapi/core/client"
	"github.com/goppydae/gapi/core/config"
	"github.com/goppydae/gapi/core/product"
	"github.com/goppydae/gapi/core/tui"
	"github.com/goppydae/gapi/core/version"
	protopkg "github.com/goppydae/gapi/pkg/proto"
)

// rootCmd is the process-wide gapictl root. It is a singleton because
// GetRoot() hands it to the orchestrator for embedding, but it is BUILT
// by the shared constructor rather than declared as a literal - so a
// test can construct an independent one and compare the two flag sets.
// Package-level initialization rather than init(): Go orders variable
// initializers before any init() in the package, so the subcommand
// registrations below cannot run against a nil root. Two init()s in one
// file would have worked only by source order.
//
// It goes through newControlTree, which does NOT claim the process's
// product identity. This initializer runs inside goblind and goblinctl
// too - they import this package - and setting "gapi" there would give
// every embedder a working default for a value that is required to have
// none (GAPI-DIV-061). gapictl declares itself in Execute() instead.
var rootCmd, controlFlags = newControlTree(
	"gapictl", version.BinaryVersion(), "Supervision kernel control CLI")

// NewGapictlRoot builds a fresh gapictl root, declaring the product.
// Exported so the parity test can construct one without depending on
// package initialization order.
//
// The orchestrator embeds this tree through GetRoot(), NOT through this
// constructor: goblinctl mounting the kernel's verbs under `agent` is
// not goblinctl becoming gapi.
func NewGapictlRoot() (*cobra.Command, *ControlFlags) {
	return NewControlRoot("gapi", "gapictl", version.BinaryVersion(), "Supervision kernel control CLI")
}

// controlConfig loads configuration and applies the control root's
// persistent flags over it, so a registered flag is a flag that is READ.
//
// GAPI-DIV-058's residual names the trap directly: a shared registrar
// makes flag DEFINITIONS agree while saying nothing about whether
// anything consumes them, which is the GAPI-DIV-034 shape (a value
// parsed and discarded). Every control command resolves its target
// through here for that reason.
//
// Flags override config; empty means "leave config alone", which is why
// --control-addr carries no default in the registrar.
func controlConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	applyControlFlags(cfg)
	resolveControlAddr(cfg)
	return cfg, nil
}

// applyControlFlags is separate so the ignore-the-error call sites keep
// their existing shape without losing the overrides.
func applyControlFlags(cfg *config.Config) {
	if cfg == nil || controlFlags == nil {
		return
	}
	if controlFlags.ControlAddr != "" {
		cfg.Transport.Address = controlFlags.ControlAddr
	}
	if controlFlags.LogLevel != "" {
		cfg.Logging.Level = controlFlags.LogLevel
	}
}

// Execute runs the root command. Bare invocation prints help and
// returns ErrNoCommand so the process exits non-zero (cli-contract.md).
//
// This is where gapictl declares its product, because this is where
// gapictl's PROCESS begins - package initialization is not that point,
// since goblinctl runs it too. Set before RunRoot: every control verb
// resolves config through controlConfig(), which reads the environment
// namespace and the search path (GAPI-DIV-061).
func Execute() error {
	product.Set("gapi")
	return RunRoot(rootCmd, os.Args[1:])
}

// GetRoot returns the root command for embedding.
func GetRoot() *cobra.Command {
	return rootCmd
}

// ping
var pingCmd = &cobra.Command{
	Use:   "ping",
	Short: "Ping the daemon",
	// RunE, and the error is RETURNED rather than printed (GAPI-DIV-120).
	//
	// This was `Run` with a bare `return` after printing "FAIL: %v", so a
	// ping that timed out EXITED 0. Nothing downstream could tell a live
	// daemon from a dead one, and the ADK harness's waitForReady is
	// exactly that caller: it treats `cmd.Run() == nil` as ready, so its
	// readiness gate could not fail. It burned the full 30s deadline,
	// reported ready, and let the suite proceed against a daemon that had
	// never answered - which surfaced later as `gapictl status` failing
	// with `context deadline exceeded`, naming the wrong subject.
	//
	// A probe that cannot report failure is not a probe. The exit status
	// IS this command's output; the printed line is for humans.
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, _ := controlConfig()
		c, err := newControlClient(cfg)
		if err != nil {
			return fmt.Errorf("failed to init client: %w", err)
		}

		fmt.Print("pinging... ")
		ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
		defer cancel()

		status, err := c.Ping(ctx)
		if err != nil {
			fmt.Println("FAIL")
			return fmt.Errorf("ping: %w", err)
		}
		fmt.Println("message:", status)
		return nil
	},
}

// pingTimeout bounds a single `gapictl ping`.
//
// Named rather than inline because it is the number the ADK harness
// pays per supervisor start when a ping goes unanswered, and an
// unexplained literal is what let 30 seconds hide there (GAPI-DIV-120).
const pingTimeout = 30 * time.Second

// reload
var agentReloadCmd = &cobra.Command{
	Use:   "reload",
	Short: "Trigger a reload of registered agents",
	Run: func(cmd *cobra.Command, args []string) {
		cfg, _ := controlConfig()
		c, err := newControlClient(cfg)
		if err != nil {
			log.Fatalf("failed to init client: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := c.ReloadAgents(ctx); err != nil {
			log.Fatalf("reload failed: %v", err)
		}
		fmt.Println("reload event dispatched")
	},
}

// status
var agentStatusCmd = &cobra.Command{
	Use:   "status [PATTERN...]",
	Short: "Show current registered agents",
	Run: func(cmd *cobra.Command, args []string) {
		treeView, _ := cmd.Flags().GetBool("tree")
		if len(args) == 0 && !cmd.Flags().Changed("tree") {
			treeView = true
		}
		cfg, err := controlConfig()
		if err != nil {
			log.Fatalf("failed to load config: %v", err)
		}

		c, err := newControlClient(cfg)
		if err != nil {
			log.Fatalf("failed to init client: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		agents, err := c.AgentStatus(ctx)
		if err != nil {
			log.Fatalf("failed to get status: %v", err)
		}

		// Filter
		var filtered []*protopkg.AgentStatus
		if len(args) == 0 {
			filtered = agents
		} else {
			for _, agent := range agents {
				for _, pat := range args {
					if strings.Contains(agent.Id, pat) {
						filtered = append(filtered, agent)
						break
					}
				}
			}
		}

		if !treeView {
			// Flat view
			for _, agent := range filtered {
				deps := ""
				if len(agent.Dependencies) > 0 {
					deps = fmt.Sprintf(" (deps: %s)", strings.Join(agent.Dependencies, ", "))
				}
				caps := ""
				if len(agent.Capabilities) > 0 {
					caps = fmt.Sprintf(" [caps: %s]", strings.Join(agent.Capabilities, ", "))
				}
				fmt.Printf(" - %s (%s) [%s]%s%s\n", agent.Id, agent.Type, toProtoState(agent.State), deps, caps)
			}
		} else {
			// Tree View
			printTree(filtered)
		}
	},
}

// TUI command
var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Interactive TUI for monitoring agents",
	Long:  "Start an interactive terminal UI for real-time agent monitoring and control.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := controlConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		c, err := newControlClient(cfg)
		if err != nil {
			return fmt.Errorf("failed to init client: %w", err)
		}

		ctrl := &LocalController{client: c}
		return tui.Run(ctrl)
	},
}

var cryptoCmd = &cobra.Command{
	Use:   "crypto",
	Short: "Cryptography utilities",
}

func init() {
	rootCmd.AddCommand(pingCmd)
	rootCmd.AddCommand(shutdownCmd)
	rootCmd.AddCommand(tuiCmd)
	rootCmd.AddCommand(cryptoCmd)

	agentCmd.AddCommand(agentReloadCmd)
	agentCmd.AddCommand(agentStatusCmd)

	agentStatusCmd.Flags().BoolP("tree", "t", false, "Show dependency tree")
}

func printTree(agents []*protopkg.AgentStatus) {
	byId := make(map[string]*protopkg.AgentStatus)
	inDegree := make(map[string]int)

	for _, a := range agents {
		byId[a.Id] = a
		if _, ok := inDegree[a.Id]; !ok {
			inDegree[a.Id] = 0
		}
		for _, dep := range a.Dependencies {
			inDegree[dep]++
		}
	}

	var roots []*protopkg.AgentStatus
	for _, a := range agents {
		if inDegree[a.Id] == 0 {
			roots = append(roots, a)
		}
	}
	if len(roots) == 0 && len(agents) > 0 {
		roots = agents
	}

	var printNode func(id string, prefix string, isLast bool, visited map[string]bool)
	printNode = func(id string, prefix string, isLast bool, visited map[string]bool) {
		agent, ok := byId[id]
		if !ok {
			nodeStr := fmt.Sprintf("%s [missing]", id)
			marker := "|-- "
			if isLast {
				marker = "`-- "
			}
			fmt.Printf("%s%s%s\n", prefix, marker, nodeStr)
			return
		}

		status := toProtoState(agent.State)
		marker := "|-- "
		if isLast {
			marker = "`-- "
		}
		fmt.Printf("%s%s%s (%s) [%s]\n", prefix, marker, agent.Id, agent.Type, status)

		if visited[id] {
			return
		}
		visited[id] = true

		newPrefix := prefix + "|   "
		if isLast {
			newPrefix = prefix + "    "
		}

		for i, dep := range agent.Dependencies {
			isLastDep := i == len(agent.Dependencies)-1
			printNode(dep, newPrefix, isLastDep, copyMap(visited))
		}
	}

	for _, root := range roots {
		printNode(root.Id, "", true, make(map[string]bool))
	}
}

func copyMap(m map[string]bool) map[string]bool {
	out := make(map[string]bool)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func toProtoState(s protopkg.AgentState) string {
	switch s {
	case protopkg.AgentState_AGENT_STATE_INITIALIZING:
		return "initializing"
	case protopkg.AgentState_AGENT_STATE_INITIALIZED:
		return "initialized"
	case protopkg.AgentState_AGENT_STATE_STARTING:
		return "starting"
	case protopkg.AgentState_AGENT_STATE_RUNNING:
		return "running"
	case protopkg.AgentState_AGENT_STATE_STOPPING:
		return "stopping"
	case protopkg.AgentState_AGENT_STATE_STOPPED:
		return "stopped"
	case protopkg.AgentState_AGENT_STATE_FAILED:
		return "failed"
	case protopkg.AgentState_AGENT_STATE_RELOADING:
		return "reloading"
	default:
		return "unknown"
	}
}

// LocalController implements tui.AgentControl using a local client
type LocalController struct {
	client *client.Client
}

func (l *LocalController) FetchStatus(ctx context.Context) ([]tui.AgentStatus, error) {
	agents, err := l.client.AgentStatus(ctx)
	if err != nil {
		return nil, err
	}

	var status []tui.AgentStatus
	for _, a := range agents {
		// Convert proto state to string
		// Reuse toProtoState helper? It returns lowercase.
		// TUI expects specific strings or we just use what we get.
		// TUI uses "RUNNING" or "running" check.
		// Let's use toProtoState but uppercase it for consistency if needed,
		// or just use the helper.
		stateStr := toProtoState(a.State)

		// Format CPU usage
		cpuStr := "-"
		if a.CpuUsage > 0 {
			cpuStr = fmt.Sprintf("%.1f%%", a.CpuUsage)
		}

		// Format memory usage
		memStr := "-"
		if a.MemoryUsage > 0 {
			memStr = formatBytes(a.MemoryUsage)
		}

		// Format uptime
		var uptime time.Duration
		if a.UptimeNs > 0 {
			uptime = time.Duration(a.UptimeNs)
		}

		status = append(status, tui.AgentStatus{
			ID:     a.Id,
			State:  stateStr,
			Type:   a.Type,
			CPU:    cpuStr,
			Memory: memStr,
			Uptime: uptime,
		})
	}
	return status, nil
}

func formatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%dB", bytes)
	}
	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func (l *LocalController) Lifecycle(ctx context.Context, id, action string) (bool, error) {
	switch action {
	case "start":
		res := l.client.Start(ctx, []string{id})
		// Check for errors in results
		for _, r := range res {
			if r.Err != nil {
				return false, r.Err
			}
		}
		return true, nil
	case "stop":
		res := l.client.Stop(ctx, []string{id})
		for _, r := range res {
			if r.Err != nil {
				return false, r.Err
			}
		}
		return true, nil
	case "reload":
		// Reload single agent not strictly supported by client.ReloadAgents (which reloads all?)
		// Checking client.go... client.ReloadAgents() is global.
		// If we want single agent reload, we might need a new method or assume global.
		// For now, let's call global reload if id is empty, or warn.
		// The TUI passes an ID.
		// Client doesn't seem to have Reload(id).
		// Let's implement it as a global reload for now or return error "not supported"
		return false, fmt.Errorf("reload single agent not supported yet")
	}
	return false, fmt.Errorf("unknown action: %s", action)
}

func (l *LocalController) GetLogs(ctx context.Context, id string) (<-chan string, error) {
	return l.client.GetLogs(ctx, id)
}
