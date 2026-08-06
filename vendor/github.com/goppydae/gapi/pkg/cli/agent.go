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
	"embed"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"

	"github.com/goppydae/gapi/core/crypto"
	"github.com/goppydae/gapi/internal/logattr"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Manage agents",
	Long:  "Build, sign, and manage agents across Python and Go.",
}

var agentBuildCmd = &cobra.Command{
	Use:   "build [path]",
	Short: "Build Go agents",
	Long: `Build Go agents from source and generate checksums.

Examples:
  gapictl agent build src/agents/init.go.service
  gapictl agent build src/agents/
  gapictl agent build --watch src/agents/cluster_join.go.service
  gapictl agent build --sign --key=agent-signing.key src/agents/init.go.service`,
	Args: cobra.ExactArgs(1),
	RunE: runAgentBuild,
}

var agentCleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Clean build artifacts",
	Long:  "Remove all built binaries and checksums from agents/build/",
	RunE:  runAgentClean,
}

var agentNewCmd = &cobra.Command{
	Use:   "new [name]",
	Short: "Create a new agent from template",
	Long: `Create a new agent from template with proper structure.

Examples:
  gapictl agent new my_service
  gapictl agent new --type=timer my_timer
  gapictl agent new --lang=python --type=service my_py_service`,
	Args: cobra.ExactArgs(1),
	RunE: runAgentNew,
}

var (
	watchMode   bool
	signBuild   bool
	keyPath     string
	outputDir   string
	agentLang   string
	agentType   string
	agentOutput string

	// cgoFlag backs --cgo. THREE STATES, NOT TWO: a plain bool cannot
	// distinguish "the operator asked for cgo off" from "the operator said
	// nothing", and those defer to different things - see stagedCGOEnv.
	// Cobra's Changed() supplies the third state at parse time and this
	// pointer carries it to the builder, which has six callers and no
	// access to the command.
	cgoFlag *bool
)

func init() {
	agentBuildCmd.Flags().BoolVarP(&watchMode, "watch", "w", false, "Watch for changes and rebuild")
	// GAPI-DIV-105. The staged build defaults to CGO_ENABLED=0 because
	// nothing in the ADK runtime needs cgo - `net` is the only thing that
	// engages it - and the pure-Go resolver is the right one for a process
	// the supervisor manages. This flag is the way back for an author who
	// does want cgo, so the default removes an unstated host requirement
	// without removing the choice.
	agentBuildCmd.Flags().Bool("cgo", false,
		"Build the agent with cgo enabled (default: disabled, so no C compiler is needed)")
	agentBuildCmd.Flags().BoolVar(&signBuild, "sign", false, "Sign the built binary with ED25519")
	agentBuildCmd.Flags().StringVar(&keyPath, "key", "", "Path to ED25519 signing key")
	// Built agents land in the DEPLOY payload, not a build sub-tree.
	// agents/ is what an install ships, and a built Go agent is a
	// deployable artifact exactly as a Python agent file is - it keeps
	// the same <name>.<lang>.<type> name, so one directory holds every
	// language and type and reads like a systemd unit directory.
	agentBuildCmd.Flags().StringVarP(&outputDir, "output", "o", "agents", "Output directory for built agents")

	// The advertised sets come from the scaffold matrix rather than from
	// literals here: help that names a type with no template is the same
	// promise the fallback used to keep badly (GAPI-DIV-054).
	agentNewCmd.Flags().StringVarP(&agentLang, "lang", "l", "go",
		fmt.Sprintf("Agent language (%s)", strings.Join(scaffoldLangs(), ", ")))
	agentNewCmd.Flags().StringVarP(&agentType, "type", "t", "service",
		fmt.Sprintf("Agent type (%s)", strings.Join(scaffoldTypes(), ", ")))
	agentNewCmd.Flags().StringVarP(&agentOutput, "output", "o", "", "Output directory (default: agents/{lang}/foundational or agents/{lang}/services)")

	agentCmd.AddCommand(agentBuildCmd)
	agentCmd.AddCommand(agentCleanCmd)
	agentCmd.AddCommand(agentNewCmd)
	rootCmd.AddCommand(agentCmd)
}

func runAgentBuild(cmd *cobra.Command, args []string) error {
	sourcePath := args[0]

	// Resolve --cgo here, where the command is in scope. Changed() is the
	// distinction that matters: an unset flag leaves cgoFlag nil so the
	// environment gets its say, while `--cgo=false` is an explicit choice
	// that beats an inherited CGO_ENABLED=1.
	if cmd.Flags().Changed("cgo") {
		v, err := cmd.Flags().GetBool("cgo")
		if err != nil {
			return fmt.Errorf("read --cgo: %w", err)
		}
		cgoFlag = &v
	}

	// Validate source path
	info, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("source path error: %w", err)
	}

	if watchMode {
		return watchAndBuild(sourcePath, info.IsDir())
	}

	// Build single agent or all agents in directory
	if info.IsDir() {
		return buildDirectory(sourcePath)
	}
	return buildAgent(sourcePath)
}

func watchAndBuild(sourcePath string, isDir bool) error {
	// Initial build
	fmt.Println("Initial build...")
	if isDir {
		if err := buildDirectory(sourcePath); err != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "initial build failed", logattr.Err(err))
		}
	} else {
		if err := buildAgent(sourcePath); err != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "initial build failed", logattr.Err(err))
		}
	}

	// Setup file watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create watcher: %w", err)
	}
	// The watch loop runs until interrupt; stderr is the CLI's error surface
	// for the final cleanup close.
	defer func() {
		if cerr := watcher.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to close watcher: %v\n", cerr)
		}
	}()

	// Watch the source path
	if err := addWatchRecursive(watcher, sourcePath); err != nil {
		return fmt.Errorf("failed to watch path: %w", err)
	}

	fmt.Printf("Watching %s for changes (Ctrl+C to stop)...\n", sourcePath)

	// Debounce timer to avoid rebuilding on every file save
	var debounceTimer *time.Timer
	debounceDuration := 300 * time.Millisecond

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}

			// Only rebuild on agent sources. This tests the AGENT-FILE
			// form rather than a ".go" extension: an agent is named
			// <name>.go.<type>, so filepath.Ext returns ".service" and an
			// extension check would silently never fire - watch mode
			// would run, report nothing, and rebuild nothing.
			if !isGoAgentFile(filepath.Base(event.Name)) {
				continue
			}

			// Ignore temporary files
			if filepath.Base(event.Name)[0] == '.' {
				continue
			}

			if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				// Reset debounce timer
				if debounceTimer != nil {
					debounceTimer.Stop()
				}

				debounceTimer = time.AfterFunc(debounceDuration, func() {
					fmt.Printf("\nChange detected: %s\n", event.Name)
					fmt.Println("Rebuilding...")

					if isDir {
						if err := buildDirectory(sourcePath); err != nil {
							slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "build failed", logattr.Err(err))
						} else {
							fmt.Println("[OK] Rebuild complete")
						}
					} else {
						if err := buildAgent(sourcePath); err != nil {
							slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "build failed", logattr.Err(err))
						} else {
							fmt.Println("[OK] Rebuild complete")
						}
					}

					fmt.Printf("Watching for changes...\n")
				})
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "watcher error", logattr.Err(err))
		}
	}
}

func addWatchRecursive(watcher *fsnotify.Watcher, path string) error {
	return filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip hidden directories and build artifacts
		if info.IsDir() {
			base := filepath.Base(p)
			if base[0] == '.' || base == "build" {
				return filepath.SkipDir
			}
			return watcher.Add(p)
		}
		return nil
	})
}

func buildDirectory(dir string) error {
	fmt.Printf("Building all Go agents in %s...\n", dir)

	sources, err := findGoAgents(dir)
	if err != nil {
		return fmt.Errorf("failed to scan directory: %w", err)
	}
	if len(sources) == 0 {
		return fmt.Errorf("no Go agents found in %s: an agent is a single file named "+
			"<name>.go.<type> where type is one of %s", dir, strings.Join(goAgentTypes, ", "))
	}

	// A failure builds the rest rather than aborting: one broken agent in
	// a tree should not stop the others, and the error names which.
	failed := 0
	for _, src := range sources {
		if err := buildAgent(src); err != nil {
			failed++
			slog.Default().LogAttrs(context.Background(), slog.LevelError, "build failed", logattr.Path(src), logattr.Err(err))
		}
	}

	if failed > 0 {
		return fmt.Errorf("%d of %d agents failed to build", failed, len(sources))
	}
	fmt.Printf("[OK] Built %d agents\n", len(sources))
	return nil
}

// buildAgent builds ONE Go agent from its source file.
//
// sourcePath names a <name>.go.<type> file, not a directory containing a
// main.go. A Go agent is a single file that declares metadata and
// lifecycle functions; the main that registers them and runs the ADK is
// generated at build time, which is what makes an agent unable to
// misunderstand the verb its supervisor invokes (GAPI-DIV-052).
func buildAgent(sourcePath string) error {
	if !isGoAgentFile(filepath.Base(sourcePath)) {
		return fmt.Errorf("%s is not a Go agent: an agent is a single file named "+
			"<name>.go.<type> where type is one of %s",
			sourcePath, strings.Join(goAgentTypes, ", "))
	}

	agentName := goAgentName(sourcePath)
	fmt.Printf("Building %s...\n", agentName)

	outputBinary, _, err := buildGoAgent(sourcePath, outputDir)
	if err != nil {
		return err
	}

	// Generate BLAKE3 hash
	hash, err := crypto.HashFile(outputBinary)
	if err != nil {
		return fmt.Errorf("failed to hash binary: %w", err)
	}

	hashFile := outputBinary + ".b3"
	if err := os.WriteFile(hashFile, []byte(hash+"\n"), 0600); err != nil {
		return fmt.Errorf("failed to write hash file: %w", err)
	}

	fmt.Printf("  - Binary: %s\n", outputBinary)
	fmt.Printf("  - Hash:   %s\n", hashFile)

	// Optionally sign
	if signBuild {
		if keyPath == "" {
			return fmt.Errorf("--sign requires --key to be specified")
		}

		keypair, err := crypto.LoadPrivate(keyPath)
		if err != nil {
			return fmt.Errorf("failed to load signing key: %w", err)
		}

		signature := keypair.Sign([]byte(hash))
		sigFile := outputBinary + ".sig"
		if err := os.WriteFile(sigFile, []byte(fmt.Sprintf("%x\n", signature)), 0600); err != nil {
			return fmt.Errorf("failed to write signature: %w", err)
		}

		fmt.Printf("  - Signature: %s\n", sigFile)
	}

	return nil
}

func runAgentClean(cmd *cobra.Command, args []string) error {
	buildDirs := []string{
		"agents/build/go",
	}

	for _, dir := range buildDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}

		fmt.Printf("Cleaning %s...\n", dir)
		if err := os.RemoveAll(dir); err != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "failed to clean directory", logattr.Path(dir), logattr.Err(err))
		}
	}

	fmt.Println("[OK] Clean complete")
	return nil
}
