// Copyright (c) 2026 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package magelib

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// runStreamed runs a command with its output attached to this process,
// passing every argument VERBATIM.
//
// mage's sh.RunV is the house idiom and is wrong here: it runs
// os.Expand over each argument, so a "$" in a generator command is
// substituted from the environment before the command ever sees it - an
// undefined name becoming the empty string, silently. Every other gate
// in this library invokes fixed tool names with fixed flags, where that
// never shows; DocsConfig.Generators is caller-supplied, and a build
// library that rewrites the command it was asked to run is not running
// that command.
func runStreamed(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// docsAssets carries the Hugo configuration, layouts and stylesheets that
// would otherwise be copied into four repositories and drift there.
//
// Embed-and-materialize, rather than publishing these as a Hugo module.
// The alternative composes more cleanly but adds a SECOND
// module-resolution system to a Go build library and couples a magelib
// tag to every site rebuild. What this choice costs is a generated
// directory that must exist before hugo runs, so DocsBuild and DocsServe
// always sync first and a bare `hugo --source docs` in a fresh clone
// renders a themeless site. That failure is loud on the first page,
// which is the only reason it is acceptable.
//
//go:embed docsassets
var docsAssets embed.FS

// magelibDir is the per-repo directory DocsSync materialises into. It is
// gitignored in every consuming repo: it is generated, so committing it
// would put four copies back under version control and undo the point.
const magelibDir = ".magelib"

// Generated documentation is written 0600, and its directories 0750.
//
// Nothing here needs group or world access - hugo reads these as the
// same user that wrote them - and git records only the executable bit,
// so the committed artifacts land as 100644 regardless and a fresh
// clone creates them under the reader's own umask. The tight mode
// therefore costs nothing and is not worth a gosec carve-out to avoid.

// APIPackage names one gomarkdoc target.
type APIPackage struct {
	// Path is the package pattern gomarkdoc is pointed at, e.g.
	// "./pkg/magelib/...".
	Path string
	// Out is the repo-relative markdown file to write, e.g.
	// "docs/content/reference/magelib.md".
	Out string
	// Title is the front matter title. gomarkdoc does not emit front
	// matter, so this library prepends it rather than carrying a
	// template file: the drift gate compares bytes, and a second place
	// that decides those bytes is a second place for them to move.
	Title string
}

// DocsConfig describes one repository's documentation site.
//
// Every field here has a reader. That is deliberate rather than
// incidental: a configuration field nothing consumes is the shape
// design/adk-architecture.md forbids by name, and a docs config is
// exactly where one hides, because nothing fails when a title is
// ignored.
type DocsConfig struct {
	// Dir is the site root, conventionally "docs".
	Dir string
	// Title is the site title.
	Title string
	// BaseURL is the published root, e.g.
	// "https://goppydae.github.io/magelib/".
	BaseURL string
	// Repo is the repository's canonical path without a scheme, e.g.
	// "github.com/goppydae/magelib". It supplies the sidebar's GitHub
	// link, the pkg.go.dev link, and the default EditURL.
	Repo string
	// EditURL is the base for per-page "edit this page" links. Derived
	// from Repo and Dir when empty, which is the correct value for every
	// repo in this silo; setting it is for a repo whose default branch
	// or layout differs.
	EditURL string
	// Generators are commands run during generation, each invoked with
	// ONE APPENDED ARGUMENT: the output root to write beneath.
	//
	// That argument is what makes CheckDocsDrift trustworthy. The drift
	// gate regenerates into a temporary tree and byte-compares, and it
	// can only do that if generation is redirectable; a generator that
	// hardcodes its output forces the gate to either mutate the working
	// tree or compare against something it did not produce. Under an
	// ordinary generate the argument is ".", so there is ONE code path
	// and the gate runs the same command the build does.
	Generators [][]string
	// APIPackages are the gomarkdoc targets.
	APIPackages []APIPackage
	// Committed are the repo-relative paths under drift control -
	// generated output that IS checked in, so the reference is readable
	// on the forge without a build and has a mechanical counterpart the
	// design docs can be checked against.
	Committed []string
}

// validate rejects a configuration that cannot mean what the caller
// intended, for the reason compileSkips does: a gate or a build must not
// run at all on a configuration that would succeed without doing its
// work.
func (c DocsConfig) validate() error {
	if strings.TrimSpace(c.Dir) == "" {
		return fmt.Errorf("docs config: Dir is empty; name the site root, conventionally \"docs\"")
	}
	if filepath.IsAbs(c.Dir) {
		return fmt.Errorf("docs config: Dir %q must be repo-relative, not absolute", c.Dir)
	}
	if strings.TrimSpace(c.Title) == "" {
		return fmt.Errorf("docs config: Title is empty; it is the site's name in the header and every page title")
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return fmt.Errorf("docs config: BaseURL is empty; Hugo renders absolute links against it and an empty one silently produces broken ones")
	}
	if strings.TrimSpace(c.Repo) == "" {
		return fmt.Errorf("docs config: Repo is empty; it supplies the GitHub and pkg.go.dev links and the default EditURL")
	}
	if strings.Contains(c.Repo, "://") {
		return fmt.Errorf("docs config: Repo %q carries a scheme; give the canonical module path, e.g. \"github.com/goppydae/magelib\"", c.Repo)
	}
	for _, p := range c.APIPackages {
		if strings.TrimSpace(p.Out) == "" || strings.TrimSpace(p.Path) == "" {
			return fmt.Errorf("docs config: API package %+v needs both Path and Out", p)
		}
		if err := insideRepo("API package output", p.Out); err != nil {
			return err
		}
		if strings.TrimSpace(p.Title) == "" {
			return fmt.Errorf("docs config: API package %q has no Title; the front matter title is what the sidebar shows", p.Out)
		}
	}
	for _, p := range c.Committed {
		if err := insideRepo("committed path", p); err != nil {
			return err
		}
	}
	for _, g := range c.Generators {
		if len(g) == 0 {
			return fmt.Errorf("docs config: an empty generator command would run nothing and report success")
		}
	}
	return nil
}

// insideRepo rejects a path that escapes the repository root. A gate
// that writes or compares outside the tree is not describing the tree.
func insideRepo(what, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("docs config: %s is empty", what)
	}
	if filepath.IsAbs(path) {
		return fmt.Errorf("docs config: %s %q must be repo-relative, not absolute", what, path)
	}
	cleaned := filepath.Clean(path)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("docs config: %s %q does not name a path inside the repo", what, path)
	}
	return nil
}

// editURL returns the configured edit base, or the one derived from Repo
// and Dir.
func (c DocsConfig) editURL() string {
	if strings.TrimSpace(c.EditURL) != "" {
		return c.EditURL
	}
	return "https://" + c.Repo + "/edit/main/" + c.Dir + "/content/"
}

// assetDir is where DocsSync materialises the embedded tree.
func (c DocsConfig) assetDir() string { return filepath.Join(c.Dir, magelibDir) }

// DocsSync materialises the embedded asset tree into <Dir>/.magelib and
// renders the shared Hugo configuration from this repo's DocsConfig.
//
// The target directory is REMOVED first rather than written over. A
// renamed or deleted asset that survives in a consumer's tree keeps
// being served by Hugo, so the site renders from a file no version of
// magelib produces any more - stale in a directory nobody reads because
// it is generated and gitignored.
func DocsSync(cfg DocsConfig) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	dir := cfg.assetDir()
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clearing %s: %w", dir, err)
	}
	err := fs.WalkDir(docsAssets, "docsassets", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel("docsassets", path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		data, err := docsAssets.ReadFile(path)
		if err != nil {
			return err
		}
		// The Hugo configuration is the one asset that is not the same
		// bytes in every repo, so it is a template rather than a copy.
		if rel == "hugo-base.yaml.tmpl" {
			data, err = cfg.renderBaseConfig(data)
			if err != nil {
				return err
			}
			target = filepath.Join(dir, "hugo-base.yaml")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		return fmt.Errorf("materialising docs assets into %s: %w", dir, err)
	}
	fmt.Printf("Docs assets synced to %s\n", dir)
	return nil
}

// renderBaseConfig fills the shared Hugo configuration from cfg.
//
// Option("missingkey=error") is what keeps this honest: a template
// referencing a field that no longer exists would otherwise render
// "<no value>" into a YAML file, and Hugo would start with a baseURL of
// that literal rather than failing.
func (c DocsConfig) renderBaseConfig(tmpl []byte) ([]byte, error) {
	t, err := template.New("hugo-base").Option("missingkey=error").Parse(string(tmpl))
	if err != nil {
		return nil, fmt.Errorf("parsing hugo-base template: %w", err)
	}
	var buf bytes.Buffer
	err = t.Execute(&buf, struct{ Title, BaseURL, Repo, EditURL string }{
		Title:   c.Title,
		BaseURL: c.BaseURL,
		Repo:    c.Repo,
		EditURL: c.editURL(),
	})
	if err != nil {
		return nil, fmt.Errorf("rendering hugo-base config: %w", err)
	}
	return buf.Bytes(), nil
}

// DocsGenerate syncs the assets, renders the API reference, and runs
// every configured generator, writing into the working tree.
func DocsGenerate(cfg DocsConfig) error {
	if err := DocsSync(cfg); err != nil {
		return err
	}
	return docsGenerateInto(cfg, ".")
}

// docsGenerateInto runs the generation steps with every output rebased
// under root. root is "." for an ordinary generate and a temporary
// directory for the drift gate, and both take the same path through this
// function so the gate cannot compare against something the build would
// not have produced.
func docsGenerateInto(cfg DocsConfig, root string) error {
	for _, pkg := range cfg.APIPackages {
		out := filepath.Join(root, pkg.Out)
		if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
			return err
		}
		if err := runStreamed("gomarkdoc", gomarkdocArgs(cfg, pkg, out)...); err != nil {
			return fmt.Errorf("gomarkdoc %s: %w", pkg.Path, err)
		}
		if err := prependFrontMatter(out, pkg.Title); err != nil {
			return err
		}
	}
	for _, gen := range cfg.Generators {
		args := append(append([]string{}, gen[1:]...), root)
		if err := runStreamed(gen[0], args...); err != nil {
			return fmt.Errorf("generator %s: %w", strings.Join(gen, " "), err)
		}
	}
	return nil
}

// docsDefaultBranch is the branch every source link points at.
//
// A constant rather than a DocsConfig field because all four repositories
// use main, and an optional field defaulting to "main" would be a second
// place for the answer to live without ever holding a different one.
const docsDefaultBranch = "main"

// gomarkdocArgs builds the gomarkdoc invocation for one package.
//
// The three repository flags are the point. gomarkdoc DETECTS the
// repository from git state and emits source links only when detection
// succeeds, so the same source renders two ways: a developer checkout
// produces linked headings, a CI checkout produces bare ones. Both are
// legitimate output; a byte-comparing drift gate cannot tell them apart
// from staleness, and reports whichever it did not generate as stale.
//
// Measured on magelib PR #33 - green locally, red in CI, and byte
// identical once these are pinned. Detection makes generated output a
// function of how the tree was obtained, which is the one thing an
// artifact under a byte gate must not be.
//
// Repo is validated to carry no scheme, which is what lets the URL be
// derived from it rather than configured beside it.
func gomarkdocArgs(cfg DocsConfig, pkg APIPackage, out string) []string {
	return []string{
		pkg.Path,
		"--output", out,
		"--repository.url", "https://" + cfg.Repo,
		"--repository.default-branch", docsDefaultBranch,
		"--repository.path", "/",
	}
}

// prependFrontMatter puts a relearn front matter block in front of
// gomarkdoc's output.
//
// It is idempotent by construction - it rewrites the whole file from
// gomarkdoc's bytes each time - and it carries no date. A generated
// artifact under a byte-comparing drift gate cannot contain a clock, and
// front matter is the obvious place to reach for one.
func prependFrontMatter(path, title string) error {
	body, err := os.ReadFile(path) // #nosec G304 -- path is repo-relative config, validated by DocsConfig.validate
	if err != nil {
		return fmt.Errorf("reading generated %s: %w", path, err)
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "---\ntitle: %q\n---\n\n", title)
	buf.Write(body)
	return os.WriteFile(path, buf.Bytes(), 0o600)
}

// DocsBuild generates the reference and renders the static site.
func DocsBuild(cfg DocsConfig) error {
	if err := DocsGenerate(cfg); err != nil {
		return err
	}
	return runStreamed("hugo", append(hugoArgs(cfg), "--minify")...)
}

// docsServeArgs is separate because `server` is a SUBCOMMAND, and cobra
// resolves a subcommand that appears after the persistent flags
// differently from one that leads. Building the two invocations from one
// slice put `server` after --source and produced a build, not a server.

// DocsServe generates the reference and runs Hugo's own server, which is
// how the private hub is read: GitHub Pages needs a public repository or
// an Enterprise plan, and the hub has neither by decision.
func DocsServe(cfg DocsConfig) error {
	if err := DocsGenerate(cfg); err != nil {
		return err
	}
	return runStreamed("hugo", append([]string{"server"}, append(hugoArgs(cfg), "--buildDrafts")...)...)
}

// hugoArgs names the source directory and the configuration files.
//
// Order is load-bearing and the wrong way round is silent: Hugo merges
// left to right, so the shared base comes FIRST and the repo's own
// config.yaml second, or every repo would render magelib's defaults over
// its own title and base URL.
//
// config.yaml is listed only when it EXISTS. hugo-base.yaml already
// carries every value a site needs, so a repo with nothing to override
// legitimately has no config of its own - and naming a missing file in
// --config is a hard Hugo error, which would make "I had nothing to add"
// indistinguishable from a broken setup.
func hugoArgs(cfg DocsConfig) []string {
	configs := filepath.Join(magelibDir, "hugo-base.yaml")
	if _, err := os.Stat(filepath.Join(cfg.Dir, "config.yaml")); err == nil {
		configs += ",config.yaml"
	}
	return []string{"--source", cfg.Dir, "--config", configs}
}
