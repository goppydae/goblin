package magelib

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// licenseSkipNames are never walked, on top of the shared skipDirs.
// These hold third-party or generated trees whose files carry other
// people's notices; inserting ours into them is the mirror image of
// what MPL section 3.4 forbids, and a re-vendor or a rebuild reverts it
// anyway.
var licenseSkipNames = map[string]bool{
	".venv": true, "node_modules": true, "result": true, "dist": true,
}

// CheckLicenseHeaders fails when any hand-written source file in the
// repo rooted at the current directory is missing the configured
// notice. Like the other gates it is meant to be wired into Lint with
// mg.Deps, so it rides a CI context that already blocks merges rather
// than becoming a target nobody invokes.
//
// What this gate is actually for is worth stating, because the usual
// justification is wrong: MPL 2.0 has no operative clause requiring a
// per-file header. Section 1.4 defines Covered Software as source to
// which the Exhibit A notice HAS BEEN ATTACHED. The header is therefore
// not an obligation being breached by its absence - it is the mechanism
// that puts a file under the licence at all. An unheadered file is
// arguably not covered, the reciprocity obligation does not attach to
// it, and a downstream consumer holding that one file cannot tell what
// terms it is under. The gate exists to make every file's licence
// status unambiguous to a reader, which is a stronger and more precise
// goal than compliance.
//
// Scope is licenseCommentPrefix (operator decision 18) minus the
// shared skipDirs, licenseSkipNames, generated files (decision 19
// covers them by a paragraph in each repo's LICENSE) and the caller's
// skips. TEST FILES COUNT: a _test.go file is hand-written creative
// expression that can be copied out of the repo, which is the whole
// test in decision 18.
//
// Every violation is reported, not just the first: a gate that stops at
// the first hit turns one fix into N build cycles - and this gate's
// first run in each repo is expected to name hundreds.
//
// Unreadable files are an ERROR, not a skip: a gate that cannot read a
// file must not report it clean.
func CheckLicenseHeaders(cfg LicenseConfig, skips ...Skip) error {
	var missing []string
	err := licenseWalk("license check", cfg, skips,
		func(path, notice, prefix string, data []byte) ([]byte, error) {
			if !hasNotice(data, prefix, notice) {
				missing = append(missing, "  "+path)
			}
			return nil, nil // a check never rewrites
		})
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf(
			"license check failed: %d file(s) missing the notice (run 'mage licenseAdd'):\n%s",
			len(missing), strings.Join(missing, "\n"))
	}
	return nil
}

// AddLicenseHeaders inserts the notice into every file the check would
// report, and RETURNS AND PRINTS the paths it modified.
//
// The return value is not a convenience. This sweep touches ~400 files
// across a silo whose repos other sessions edit concurrently, and the
// staging rule is that every path in `git add` is named explicitly -
// `git add -A` is banned twice over, once because it drags in other
// sessions' work and once because it sweeps up generated churn. The
// only safe way to satisfy that at this scale is to build the add list
// from what the tool reports it changed, so the tool has to report it.
// A sweeper that printed nothing would leave a wildcard as the only
// practical option, which is how the ban gets broken.
//
// Printing as well as returning is for the operator watching it run:
// the list is the diff they are about to review.
//
// Idempotent. A file that already carries the notice is left BYTE
// IDENTICAL and is not reported as modified, so a second run reports
// nothing and stages nothing.
func AddLicenseHeaders(cfg LicenseConfig, skips ...Skip) ([]string, error) {
	var modified []string
	err := licenseWalk("license add", cfg, skips,
		func(path, notice, prefix string, data []byte) ([]byte, error) {
			if hasNotice(data, prefix, notice) {
				return nil, nil
			}
			modified = append(modified, path)
			return insertNotice(data, notice), nil
		})
	if err != nil {
		return nil, err
	}
	sort.Strings(modified)
	for _, p := range modified {
		fmt.Println(p)
	}
	fmt.Printf("license add: %d file(s) modified\n", len(modified))
	return modified, nil
}

// licenseWalk is the one walk both the check and the sweep use, so the
// set of files they disagree about is empty by construction. A sweep
// that touched a file the check does not police would produce a diff
// nothing asked for; a check that policed a file the sweep cannot reach
// would be permanently red with no way to fix it.
//
// visit receives the path, the notice rendered in that file's comment
// style, that style's prefix, and the file's contents. It returns
// replacement content, or nil to leave the file untouched.
//
// Returning the replacement rather than writing it makes licenseWalk
// the ONLY writer, which is what makes CheckLicenseHeaders structurally
// incapable of modifying the tree: the check's visit returns nil on
// every path, so no code reachable from it can write. A gate that could
// silently repair what it is measuring would report a clean tree it had
// just edited.
//
// All file access goes through an os.Root scoped to the repo. WalkDir
// does not follow symlinks, but the gap between its Lstat and this
// function's read or write is a TOCTOU window a symlink swap could
// walk through - and this tool rewrites ~400 files in one run, which is
// the wrong place to be relying on a comment asserting the window is
// narrow. The root API closes it rather than documenting it.
func licenseWalk(gate string, cfg LicenseConfig, skips []Skip,
	visit func(path, notice, prefix string, data []byte) ([]byte, error)) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	skipPaths, skipNames, err := compileSkips(gate, skips)
	if err != nil {
		return err
	}
	for k, v := range licenseSkipNames {
		skipNames[k] = v
	}
	// Rendered once per extension rather than once per file: 400 files
	// must receive byte-identical notices, and the cheapest way to
	// guarantee that is for there to be one string.
	notices := make(map[string]string, len(licenseCommentPrefix))
	for ext := range licenseCommentPrefix {
		n, nerr := cfg.Notice(ext)
		if nerr != nil {
			return nerr
		}
		notices[ext] = n
	}

	root, err := os.OpenRoot(".")
	if err != nil {
		return fmt.Errorf("%s: open repo root: %w", gate, err)
	}
	defer func() { _ = root.Close() }()

	return filepath.WalkDir(".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if skipPaths[filepath.Clean(path)] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if skipDirs[d.Name()] || skipNames[d.Name()] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		prefix, policed := licenseCommentPrefix[filepath.Ext(path)]
		if !policed {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("%s: stat %s: %w", gate, path, err)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := root.ReadFile(path)
		if err != nil {
			return fmt.Errorf("%s: read %s: %w", gate, path, err)
		}
		if isGenerated(data) {
			return nil
		}
		replacement, err := visit(filepath.Clean(path), notices[filepath.Ext(path)], prefix, data)
		if err != nil || replacement == nil {
			return err
		}
		// The mode is carried from the walk's own stat rather than
		// defaulted: the .sh files in scope are executable, and a
		// sweep that dropped the bit would break every script it
		// touched while reporting success.
		if err := root.WriteFile(path, replacement, info.Mode().Perm()); err != nil {
			return fmt.Errorf("%s: write %s: %w", gate, path, err)
		}
		return nil
	})
}
