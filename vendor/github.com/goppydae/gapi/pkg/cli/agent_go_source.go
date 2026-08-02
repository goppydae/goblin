package cli

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/goppydae/gapi/core/crypto"
	"github.com/goppydae/gapi/core/product"
	"github.com/goppydae/gapi/internal/safeio"
)

// A Go agent is a single file named <name>.go.<type>, not a program. This
// file turns one into a buildable package: it scans the author's
// declarations and generates the main that registers them and calls the
// ADK's run loop.
//
// The generated main REFERENCES the author's identifiers rather than
// transcribing their values. That is the load-bearing choice here. A
// generator that re-encoded literals would have to reproduce Go's typing
// rules for every declaration - a float CPULimit, a raw string
// Description, an untyped const - and would fail silently on the ones it
// got wrong. Referencing them instead makes the Go compiler check the
// whole mapping, so a declaration this generator mishandles is a build
// error in the author's own file rather than a wrong value on the wire.

// goAgentDecls records WHICH declarations a source file makes. It
// deliberately does not record their values, for the reason above.
type goAgentDecls struct {
	// declared maps a canonical Spec field name to the identifier the
	// author actually used, so an agent declaring LISTEN_STREAM and one
	// declaring ListenStream both resolve.
	declared map[string]string

	// startTakesContext is true for Start(ctx context.Context) error and
	// false for Start() error. Both forms are supported; the generated
	// main adapts the second, so the runtime has one shape.
	startTakesContext bool

	// pkgStart and pkgEnd bound the "package <name>" clause, which the
	// assembler rewrites to "package main" so the author's file and the
	// generated main compile as one package.
	pkgStart, pkgEnd token.Pos
	src              []byte
	fset             *token.FileSet
}

// specFields maps each Spec field to the identifiers accepted for it.
//
// The idiomatic Go spelling comes first and the Python ADK's spelling is
// accepted as an alias, the way runner.py's META_KEYS already accepts
// several spellings per key. An author porting an agent between the two
// languages should not have to rename declarations to do it - that is the
// parity contract applied to the declaration surface rather than only to
// runtime behaviour.
var specFields = map[string][]string{
	"ID":           {"ID", "Id", "AgentID"},
	"Name":         {"Name", "NAME"},
	"Type":         {"Type", "TYPE", "UnitType", "Kind"},
	"Version":      {"Version", "VERSION"},
	"Description":  {"Description", "DESCRIPTION", "Desc"},
	"Enabled":      {"Enabled", "ENABLED"},
	"Requires":     {"Requires", "REQUIRES", "Deps", "DEPS", "Dependencies"},
	"Wants":        {"Wants", "WANTS"},
	"WantedBy":     {"WantedBy", "WANTED_BY"},
	"RequiredBy":   {"RequiredBy", "REQUIRED_BY"},
	"Schedule":     {"Schedule", "SCHEDULE"},
	"ListenStream": {"ListenStream", "LISTEN_STREAM", "Socket", "SOCKET"},
	"CPULimit":     {"CPULimit", "CPU_LIMIT", "CPU"},
	"MemoryLimit":  {"MemoryLimit", "MEMORY_LIMIT", "Memory", "MEMORY"},
	"Capabilities": {"Capabilities", "CAPABILITIES"},
}

// lifecycleFields are the func-valued Spec fields. Start is handled
// separately because it has two accepted signatures.
var lifecycleFields = []string{"Initialize", "Stop", "Reload", "Restart"}

// scanGoAgent parses a <name>.go.<type> file.
//
// The source is read and passed explicitly rather than letting the parser
// open the path: go/parser does not care about the extension, but a
// caller that assumed it did would be relying on behaviour nobody
// documented. Reading it here also gives the assembler the bytes it needs
// for the package rewrite.
func scanGoAgent(path string) (*goAgentDecls, error) {
	// safeio.ReadFile rather than os.ReadFile: it resolves the path
	// first, which is what the rest of this repo uses for operator-
	// supplied paths and what keeps this file free of the tree's only
	// inline gosec suppression.
	src, err := safeio.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read agent source: %w", err)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse agent source: %w", err)
	}

	d := &goAgentDecls{
		declared: map[string]string{},
		pkgStart: f.Package,
		pkgEnd:   f.Name.End(),
		src:      src,
		fset:     fset,
	}

	// Reverse the alias table once so the walk is a lookup rather than a
	// scan per declaration.
	byIdent := map[string]string{}
	for field, aliases := range specFields {
		for _, a := range aliases {
			byIdent[a] = field
		}
	}

	for _, decl := range f.Decls {
		switch n := decl.(type) {
		case *ast.GenDecl:
			d.scanValues(n, byIdent)
		case *ast.FuncDecl:
			d.scanFunc(n)
		}
	}

	if err := d.validate(path); err != nil {
		return nil, err
	}
	return d, nil
}

// scanValues records package-level const and var names. Only top-level
// declarations count: a name declared inside a function is not part of
// the agent's metadata, and treating it as such would let a local
// variable called Version silently become the agent's version.
func (d *goAgentDecls) scanValues(n *ast.GenDecl, byIdent map[string]string) {
	if n.Tok != token.CONST && n.Tok != token.VAR {
		return
	}
	for _, spec := range n.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for _, name := range vs.Names {
			if field, ok := byIdent[name.Name]; ok {
				d.declared[field] = name.Name
			}
		}
	}
}

// scanFunc records lifecycle functions. A method is not a lifecycle
// function - only a plain package-level func - so a receiver disqualifies
// it.
func (d *goAgentDecls) scanFunc(n *ast.FuncDecl) {
	if n.Recv != nil {
		return
	}
	switch n.Name.Name {
	case "Start":
		d.declared["Start"] = "Start"
		d.startTakesContext = n.Type.Params != nil && len(n.Type.Params.List) > 0
	default:
		for _, f := range lifecycleFields {
			if n.Name.Name == f {
				d.declared[f] = f
			}
		}
	}
}

// validate rejects a file that cannot produce a working agent, naming the
// omission. Failing here is the whole point of scanning: the alternative
// is a binary that builds, gets discovered, and then cannot be started.
func (d *goAgentDecls) validate(path string) error {
	var missing []string
	if _, ok := d.declared["ID"]; !ok {
		missing = append(missing, "ID")
	}
	if _, ok := d.declared["Start"]; !ok {
		missing = append(missing, "Start")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s declares no %s; every agent needs an ID const and a "+
			"Start function",
			path, strings.Join(missing, " and no "))
	}
	return nil
}

// sourceAsMain returns the author's file with its package clause rewritten
// to "package main".
//
// The author writes "package agent" because that is what the file reads
// like on its own; the assembled package needs one clause, and the
// generated main is what has to be package main. Rewriting by POSITION
// rather than by string replacement matters: "package agent" also appears
// in comments and import paths in a realistic agent, and a textual
// replace would corrupt one of them.
func (d *goAgentDecls) sourceAsMain() []byte {
	start := d.fset.Position(d.pkgStart).Offset
	end := d.fset.Position(d.pkgEnd).Offset

	out := make([]byte, 0, len(d.src)+8)
	out = append(out, d.src[:start]...)
	out = append(out, []byte("package main")...)
	out = append(out, d.src[end:]...)
	return out
}

// generateMain renders the main that registers the scanned declarations
// and hands control to the ADK.
//
// The author writes no flags, no describe JSON and no signal handling -
// adk.Run owns all of it. That is what makes GAPI-DIV-052 unrepresentable
// rather than fixed: an agent binary that does not understand the verb its
// supervisor invokes cannot exist if no author writes the parsing.
func (d *goAgentDecls) generateMain(adkImportPath string) []byte {
	var b strings.Builder

	fmt.Fprintf(&b, "// Code generated by '%s agent build'. DO NOT EDIT.\n", product.Name()+"ctl")
	b.WriteString("//\n")
	b.WriteString("// It is assembled beside the author's agent file, which is rewritten\n")
	b.WriteString("// into this package, and exists only for the duration of the build.\n\n")
	b.WriteString("package main\n\n")

	imports := []string{fmt.Sprintf("adk %q", adkImportPath)}
	if d.needsContextImport() {
		imports = append(imports, `"context"`)
	}
	if d.needsFmtImport() {
		imports = append(imports, `"fmt"`)
	}
	sort.Strings(imports)
	b.WriteString("import (\n")
	for _, im := range imports {
		fmt.Fprintf(&b, "\t%s\n", im)
	}
	b.WriteString(")\n\n")

	b.WriteString("func init() {\n\tadk.Register(adk.Spec{\n")
	for _, field := range sortedFields(d.declared) {
		if line := d.fieldLine(field); line != "" {
			b.WriteString(line)
		}
	}
	b.WriteString("\t})\n}\n\n")
	b.WriteString("func main() { adk.Run() }\n")

	return []byte(b.String())
}

// fieldLine renders one composite-literal entry. The value is always an
// expression over the author's identifier, never a transcribed literal.
func (d *goAgentDecls) fieldLine(field string) string {
	ident := d.declared[field]

	switch field {
	case "Start":
		if d.startTakesContext {
			return "\t\tStart: Start,\n"
		}
		// Adapt the no-context form ONCE, here, so the runtime has a
		// single shape to reason about.
		return "\t\tStart: func(context.Context) error { return Start() },\n"

	case "Initialize", "Stop", "Reload", "Restart":
		return fmt.Sprintf("\t\t%s: %s,\n", field, ident)

	case "Enabled":
		// Spec.Enabled is a *bool so ABSENT stays distinguishable from an
		// explicit false. Taking the address of the author's declaration
		// works for a var; a const is not addressable, so it goes through
		// a local.
		return fmt.Sprintf("\t\tEnabled: func() *bool { v := bool(%s); return &v }(),\n", ident)

	case "CPULimit", "MemoryLimit":
		// The wire carries these as strings and an author may declare
		// either a number or a string (CPULimit = 0.5, MemoryLimit =
		// "512MB"). fmt.Sprint accepts both, so neither form needs the
		// author to know which the schema wanted.
		return fmt.Sprintf("\t\t%s: fmt.Sprint(%s),\n", field, ident)

	default:
		return fmt.Sprintf("\t\t%s: %s,\n", field, ident)
	}
}

func (d *goAgentDecls) needsContextImport() bool {
	_, hasStart := d.declared["Start"]
	return hasStart && !d.startTakesContext
}

func (d *goAgentDecls) needsFmtImport() bool {
	for _, f := range []string{"CPULimit", "MemoryLimit"} {
		if _, ok := d.declared[f]; ok {
			return true
		}
	}
	return false
}

// adkImportPath is the Go ADK runtime's import path. It is adk/go/AGENT,
// never adk/go: gopy binds every exported symbol in adk/go into the
// Python ADK's native module, so importing the runtime from there would
// leak it into Python's C bindings.
const adkImportPath = "github.com/goppydae/gapi/adk/go/agent"

// assembleGoAgent writes the author's file and the generated main into
// dir, ready to build.
//
// dir MUST be inside this module's tree. The assembled package imports the
// ADK by module path, so a directory outside the module would need its own
// go.mod and a resolvable version of the kernel - which turns an offline
// build into one that reaches the module proxy. Nesting it keeps the
// build hermetic and makes vendor-mode resolution work unchanged.
func assembleGoAgent(srcPath, dir string) error {
	d, err := scanGoAgent(srcPath)
	if err != nil {
		return err
	}

	// The author's file becomes agent.go. Its name no longer carries the
	// type because the type has already been read out of it.
	if err := os.WriteFile(filepath.Join(dir, "agent.go"), d.sourceAsMain(), 0600); err != nil {
		return fmt.Errorf("write agent source: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), d.generateMain(adkImportPath), 0600); err != nil {
		return fmt.Errorf("write generated main: %w", err)
	}
	return nil
}

// goAgentTypes are the type suffixes a Go agent file may carry. They are
// the same three the Python side uses, because discovery routes on the
// declared type rather than on the language.
var goAgentTypes = []string{"service", "timer", "socket"}

// isGoAgentFile reports whether name is a Go agent source file:
// "<name>.go.<type>". The ".go." infix mirrors Python's ".py." infix, and
// the fact that the name does NOT end in ".go" is load-bearing - it keeps
// the file invisible to 'go build ./...' with no lint or build exclusion
// to maintain.
func isGoAgentFile(name string) bool {
	if !strings.Contains(name, ".go.") {
		return false
	}
	for _, t := range goAgentTypes {
		if strings.HasSuffix(name, "."+t) {
			return true
		}
	}
	return false
}

// goAgentName strips the ".go.<type>" suffix: hash.go.service -> hash.
// It names the AGENT, not the file - used for logs and messages.
func goAgentName(path string) string {
	base := filepath.Base(path)
	if i := strings.Index(base, ".go."); i > 0 {
		return base[:i]
	}
	return base
}

// goAgentArtifact is the file name the BUILT agent takes, and it is the
// source's name unchanged: hash.go.service -> hash.go.service.
//
// The binary keeping the full name is what makes a deployed agent
// directory readable. Every agent in a search path is then
// <name>.<lang>.<type> whatever language it was written in, so 'ls' on an
// agent directory tells you what each one is - the property systemd has
// and a bare 'hash' beside a 'tick.py.timer' would lose.
//
// It does not collide with the source because sources do NOT live on the
// agent search path; that separation is what buys the naming back.
func goAgentArtifact(srcPath string) string {
	return filepath.Base(srcPath)
}

// buildGoAgent assembles the agent beside its source, compiles it into
// outDir, and returns the binary path and the hash of what was compiled.
//
// The staging directory sits next to the AUTHOR'S FILE rather than in a
// system temp dir, because the assembled package imports the ADK by
// module path: staging outside the module tree would need a generated
// go.mod and a resolvable kernel version, turning an offline build into
// one that reaches the module proxy. Staging next to the source keeps it
// inside whatever module the agent already lives in.
//
// KNOWN LIMIT, recorded rather than hidden: this therefore assumes the
// agent lives inside a checkout that can resolve the kernel. A standalone
// gapictl building an agent in an unrelated directory needs a generated
// go.mod, which this does not do.
func buildGoAgent(srcPath, outDir string) (string, string, error) {
	stage, err := os.MkdirTemp(filepath.Dir(srcPath), ".agent-build-")
	if err != nil {
		return "", "", fmt.Errorf("stage dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	if err := assembleGoAgent(srcPath, stage); err != nil {
		return "", "", err
	}

	// Hash the STAGED package, not the author's file alone: the generated
	// main is compiled into the binary too, so a provenance hash that
	// ignored it would certify an input that is not the whole input.
	sourceHash, err := crypto.HashDirectory(stage, "*.go")
	if err != nil {
		return "", "", fmt.Errorf("hash assembled package: %w", err)
	}

	if err := os.MkdirAll(outDir, 0750); err != nil {
		return "", "", fmt.Errorf("create output directory: %w", err)
	}
	outBinary, err := filepath.Abs(filepath.Join(outDir, goAgentArtifact(srcPath)))
	if err != nil {
		return "", "", fmt.Errorf("resolve output path: %w", err)
	}

	buildTime := time.Now().Format(time.RFC3339)
	ldflags := fmt.Sprintf("-X main.SourceHash=%s -X main.BuildTime=%s", sourceHash, buildTime)

	build := exec.Command("go", "build", "-ldflags", ldflags, "-o", outBinary, ".")
	build.Dir = stage
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return "", "", fmt.Errorf("build %s: %w", filepath.Base(srcPath), err)
	}

	return outBinary, sourceHash, nil
}

// findGoAgents lists the agent source files under dir.
func findGoAgents(dir string) ([]string, error) {
	var found []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip staging directories from a concurrent or interrupted
			// build; they hold a package that is not an agent source.
			if strings.HasPrefix(filepath.Base(p), ".agent-build-") {
				return filepath.SkipDir
			}
			return nil
		}
		if isGoAgentFile(info.Name()) {
			found = append(found, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(found)
	return found, nil
}

// sortedFields orders the generated entries deterministically. A map
// iteration here would emit the fields in a fresh permutation on every
// build, which changes the generated source and therefore the build's
// input - the same non-determinism that made the gopy bindings
// unversionable (GAPI-DIV-029).
func sortedFields(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
