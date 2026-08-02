package cli

import (
	"context"
	"embed"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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
  gapictl agent build agents/go/foundational/init/
  gapictl agent build --watch agents/go/coordination/cluster_join/
  gapictl agent build --sign --key=agent-signing.key agents/go/foundational/init/`,
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
)

func init() {
	agentBuildCmd.Flags().BoolVarP(&watchMode, "watch", "w", false, "Watch for changes and rebuild")
	agentBuildCmd.Flags().BoolVar(&signBuild, "sign", false, "Sign the built binary with ED25519")
	agentBuildCmd.Flags().StringVar(&keyPath, "key", "", "Path to ED25519 signing key")
	agentBuildCmd.Flags().StringVarP(&outputDir, "output", "o", "agents/build/go", "Output directory for built binaries")

	agentNewCmd.Flags().StringVarP(&agentLang, "lang", "l", "go", "Agent language (go, python)")
	agentNewCmd.Flags().StringVarP(&agentType, "type", "t", "service", "Agent type (service, timer, socket)")
	agentNewCmd.Flags().StringVarP(&agentOutput, "output", "o", "", "Output directory (default: agents/{lang}/foundational or agents/{lang}/services)")

	agentCmd.AddCommand(agentBuildCmd)
	agentCmd.AddCommand(agentCleanCmd)
	agentCmd.AddCommand(agentNewCmd)
	rootCmd.AddCommand(agentCmd)
}

func runAgentBuild(cmd *cobra.Command, args []string) error {
	sourcePath := args[0]

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

			// Only rebuild on .go file changes
			if filepath.Ext(event.Name) != ".go" {
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

	// Find all directories with main.go
	var agentDirs []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && info.Name() == "main.go" {
			agentDirs = append(agentDirs, filepath.Dir(path))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to scan directory: %w", err)
	}

	if len(agentDirs) == 0 {
		return fmt.Errorf("no Go agents found in %s (looking for main.go)", dir)
	}

	for _, agentDir := range agentDirs {
		if err := buildAgent(agentDir); err != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelError, "build failed", logattr.Path(agentDir), logattr.Err(err))
		}
	}

	fmt.Printf("[OK] Built %d agents\n", len(agentDirs))
	return nil
}

func buildAgent(sourcePath string) error {
	// Determine agent name from directory
	agentName := filepath.Base(sourcePath)
	if agentName == "." {
		agentName = filepath.Base(filepath.Dir(sourcePath))
	}

	// Create output directory
	if err := os.MkdirAll(outputDir, 0750); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	outputBinary := filepath.Join(outputDir, agentName)

	fmt.Printf("Building %s...\n", agentName)

	// Compute source hash for verification chain
	sourceHash, err := crypto.HashDirectory(sourcePath, "*.go")
	if err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "failed to compute source hash", logattr.Err(err))
		sourceHash = "unknown"
	}

	// Build with embedded metadata
	buildTime := time.Now().Format(time.RFC3339)
	ldflags := fmt.Sprintf("-X main.SourceHash=%s -X main.BuildTime=%s", sourceHash, buildTime)

	// Make output path absolute
	absOutputBinary, err := filepath.Abs(outputBinary)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Build from source directory
	buildCmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", absOutputBinary, ".")
	buildCmd.Dir = sourcePath
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr

	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("build failed: %w", err)
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
		"agents/build/plugins",
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
