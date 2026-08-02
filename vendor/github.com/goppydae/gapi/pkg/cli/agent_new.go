package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/template"

	"github.com/goppydae/gapi/core/product"
	"github.com/goppydae/gapi/internal/safeio"
	"github.com/spf13/cobra"
)

// scaffoldKey identifies one (language, type) pair of the scaffold matrix.
type scaffoldKey struct {
	lang string
	typ  string
}

// agentScaffold names the template to render and the file name to render
// it into. The file name is not cosmetic: discovery routes Python agents
// on the ".py.<type>" infix (core/agentmgr/discovery.go), so the suffix
// decides which supervision branch the agent lands in.
type agentScaffold struct {
	template string
	fileName func(agentName string) string
}

// agentScaffolds is the whole matrix, and it has NO default branch on
// purpose. The previous code selected the Go template by interpolating
// the type and then read templates/python_service.tmpl for EVERY Python
// type, so 'agent new --lang python --type timer' wrote a file declaring
// TYPE = "service", named it <name>.py.service, and dropped it in
// agents/python/timers/ - a service wearing a timer's directory,
// discovered and supervised as a service, exit status 0 (GAPI-DIV-054).
//
// A missing pair must therefore be an error naming the pair rather than a
// fallback to some other type's template. The advertised axes come from
// scaffoldLangs and scaffoldTypes below, which derive from this map, so a
// language or type cannot be advertised without a scaffold existing.
var agentScaffolds = map[scaffoldKey]agentScaffold{
	{"go", "service"}: {"templates/go_service.tmpl", langFile("go", "service")},
	{"go", "timer"}:   {"templates/go_timer.tmpl", langFile("go", "timer")},
	{"go", "socket"}:  {"templates/go_socket.tmpl", langFile("go", "socket")},

	{"python", "service"}: {"templates/python_service.tmpl", langFile("py", "service")},
	{"python", "timer"}:   {"templates/python_timer.tmpl", langFile("py", "timer")},
	{"python", "socket"}:  {"templates/python_socket.tmpl", langFile("py", "socket")},
}

// langFile renders "<name>.<lang>.<type>" - the form BOTH languages use.
//
// Go agents were "main.go" in a directory of their own until the Go ADK
// landed; a Go agent is now a single file like a Python one, and the
// suffix carries the type for the same reason. That the name does not end
// in ".go" is load-bearing rather than cosmetic: it keeps agent sources
// invisible to 'go build ./...', so the agent tree needs no build or lint
// exclusion - a property of the naming scheme instead of configuration
// somebody has to maintain.
func langFile(lang, typ string) func(string) string {
	return func(agentName string) string {
		return fmt.Sprintf("%s.%s.%s", agentName, lang, typ)
	}
}

// scaffoldLangs and scaffoldTypes report the advertised matrix axes,
// derived from agentScaffolds so the validator, the flag help and the
// test all read one source.
func scaffoldLangs() []string { return scaffoldAxis(func(k scaffoldKey) string { return k.lang }) }
func scaffoldTypes() []string { return scaffoldAxis(func(k scaffoldKey) string { return k.typ }) }

func scaffoldAxis(pick func(scaffoldKey) string) []string {
	seen := map[string]bool{}
	var out []string
	for k := range agentScaffolds {
		if v := pick(k); !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func runAgentNew(cmd *cobra.Command, args []string) error {
	agentName := args[0]

	scaffold, ok := agentScaffolds[scaffoldKey{agentLang, agentType}]
	if !ok {
		// One error for the PAIR, not two independent ones. A valid
		// language and a valid type can still be an unsupported
		// combination, and reporting them separately is what let the
		// fallback look like success.
		if !slices.Contains(scaffoldLangs(), agentLang) {
			return fmt.Errorf("unsupported language: %s (supported: %s)",
				agentLang, strings.Join(scaffoldLangs(), ", "))
		}
		if !slices.Contains(scaffoldTypes(), agentType) {
			return fmt.Errorf("unsupported type: %s (supported: %s)",
				agentType, strings.Join(scaffoldTypes(), ", "))
		}
		return fmt.Errorf("unsupported combination: --lang %s --type %s", agentLang, agentType)
	}

	// Determine output directory.
	//
	// Go SOURCE lands off the agent search path, because a built Go agent
	// keeps its source's exact name - agents/hash.go.service is the
	// binary - and source and artifact cannot share a directory when they
	// share a name. Python has no such split: its file IS the artifact,
	// so it is written straight into the deploy tree.
	outputPath := agentOutput
	if outputPath == "" {
		if agentLang == "go" {
			outputPath = filepath.Join("src", "agents")
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

	outputFile := filepath.Join(outputPath, scaffold.fileName(agentName))

	// Load template
	tmplContent, err := templatesFS.ReadFile(scaffold.template)
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

	fmt.Printf("[OK] Created %s agent: %s\n", agentLang, outputFile)
	fmt.Printf("\nNext steps:\n")
	if agentLang == "go" {
		built := filepath.Join("agents", filepath.Base(outputFile))
		fmt.Printf("  1. Edit %s\n", outputFile)
		fmt.Printf("  2. Build: %s agent build %s\n", product.Name()+"ctl", outputFile)
		fmt.Printf("  3. Test:  %s --describe\n", built)
	} else {
		fmt.Printf("  1. Edit %s\n", outputFile)
		fmt.Printf("  2. Test:  python3 adk/python/agent/runner.py --module %s --describe\n", outputFile)
	}

	// The scaffold's directory is NOT searched just because it is called
	// "agents" - there is no working-directory tier. Saying so here is
	// the difference between an author whose agent runs and one who
	// discovers the silence themselves.
	fmt.Printf("\nTo have %s discover it, name the directory:\n", product.Daemon())
	fmt.Printf("  export %s=$PWD/agents\n", product.EnvKey("DEV_AGENTS"))

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
