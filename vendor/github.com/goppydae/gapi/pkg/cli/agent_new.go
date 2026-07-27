package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/goppydae/gapi/internal/safeio"
	"github.com/spf13/cobra"
)

func runAgentNew(cmd *cobra.Command, args []string) error {
	agentName := args[0]

	// Validate language
	if agentLang != "go" && agentLang != "python" {
		return fmt.Errorf("unsupported language: %s (supported: go, python)", agentLang)
	}

	// Validate type
	validTypes := map[string]bool{"service": true, "timer": true, "socket": true}
	if !validTypes[agentType] {
		return fmt.Errorf("unsupported type: %s (supported: service, timer, socket)", agentType)
	}

	// Determine output directory
	outputPath := agentOutput
	if outputPath == "" {
		if agentLang == "go" {
			outputPath = filepath.Join("agents", "go", "foundational", agentName)
		} else {
			// Python agents go in type-specific directories
			typeDir := agentType + "s" // service -> services, timer -> timers
			outputPath = filepath.Join("agents", "python", typeDir)
		}
	}

	// Create output directory
	if err := os.MkdirAll(outputPath, 0750); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Determine template file and output filename
	var templateFile string
	var outputFile string

	if agentLang == "go" {
		templateFile = fmt.Sprintf("templates/go_%s.tmpl", agentType)
		outputFile = filepath.Join(outputPath, "main.go")
	} else {
		templateFile = "templates/python_service.tmpl"
		outputFile = filepath.Join(outputPath, fmt.Sprintf("%s.py.service", agentName))
	}

	// Load template
	tmplContent, err := templatesFS.ReadFile(templateFile)
	if err != nil {
		return fmt.Errorf("failed to read template: %w", err)
	}

	tmpl, err := template.New("agent").Parse(string(tmplContent))
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	// Prepare template data
	data := struct {
		Name        string
		ID          string
		Description string
	}{
		Name:        titleCase(strings.ReplaceAll(agentName, "_", " ")),
		ID:          agentName,
		Description: fmt.Sprintf("%s agent", titleCase(strings.ReplaceAll(agentName, "_", " "))),
	}

	// Create output file
	outFile, err := safeio.Create(outputFile)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}

	// Render template. Close is part of the write path: a close error means
	// the scaffold may be truncated, so it fails the command.
	err = tmpl.Execute(outFile, data)
	if cerr := outFile.Close(); cerr != nil && err == nil {
		err = fmt.Errorf("failed to close output file: %w", cerr)
	}
	if err != nil {
		return fmt.Errorf("failed to render template: %w", err)
	}

	fmt.Printf("✅ Created %s agent: %s\n", agentLang, outputFile)
	fmt.Printf("\nNext steps:\n")
	if agentLang == "go" {
		fmt.Printf("  1. Edit %s\n", outputFile)
		fmt.Printf("  2. Build: gapictl agent build %s\n", outputPath)
		fmt.Printf("  3. Test:  %s --describe\n", filepath.Join("agents/build/go", agentName))
	} else {
		fmt.Printf("  1. Edit %s\n", outputFile)
		fmt.Printf("  2. Test:  python3 adk/python/agent/runner.py --module %s --describe\n", outputFile)
	}

	return nil
}

// titleCase ASCII-capitalizes each space-separated word. It replaces the
// deprecated strings.Title for agent-name scaffolding without promoting
// golang.org/x/text to a direct dependency (go.mod is operator-gated).
func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}
