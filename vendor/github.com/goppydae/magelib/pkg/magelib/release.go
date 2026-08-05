// Copyright (c) 2026 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package magelib

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/magefile/mage/sh"
)

// releasePlatforms is the supported cross-compile matrix. These are Unix
// daemons; Windows is excluded by design.
var releasePlatforms = [][2]string{
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"darwin", "amd64"},
	{"darwin", "arm64"},
}

// Release cross-compiles the given commands pure-Go (CGO_ENABLED=0) for
// linux/{amd64,arm64} and darwin/{amd64,arm64}, archives each platform as a
// .tar.gz under distDir, and writes a SHA256SUMS manifest beside them.
//
// The minisign signing of SHA256SUMS and the signed release tag (git tag -s)
// are operator-gated and intentionally NOT performed here; the stub prints
// the exact command the operator runs.
func Release(name, distDir, ldflags string, binaries map[string]string) error {
	version := Version()
	if err := os.MkdirAll(distDir, 0750); err != nil {
		return err
	}

	var archives []string
	for _, plat := range releasePlatforms {
		goos, goarch := plat[0], plat[1]
		stage := filepath.Join(distDir, goos+"_"+goarch)
		if err := os.MkdirAll(stage, 0750); err != nil {
			return err
		}
		env := map[string]string{"CGO_ENABLED": "0", "GOOS": goos, "GOARCH": goarch}
		for bin, pkg := range binaries {
			out := filepath.Join(stage, bin)
			fmt.Printf("Cross-compiling %s for %s/%s...\n", bin, goos, goarch)
			if err := sh.RunWith(env, "go", "build", "-ldflags", ldflags, "-o", out, pkg); err != nil {
				return fmt.Errorf("cross-compile %s for %s/%s: %w", bin, goos, goarch, err)
			}
		}
		archive := filepath.Join(distDir, fmt.Sprintf("%s_%s_%s_%s.tar.gz", name, version, goos, goarch))
		if err := tarGzDir(stage, archive); err != nil {
			return fmt.Errorf("archiving %s: %w", stage, err)
		}
		archives = append(archives, archive)
	}

	if err := writeSHA256SUMS(distDir, archives); err != nil {
		return err
	}

	fmt.Println("RELEASE: artifacts and SHA256SUMS written to " + distDir)
	fmt.Println("RELEASE: signing step stubbed (operator-gated). Operator runs:")
	fmt.Printf("RELEASE:   minisign -Sm %s/SHA256SUMS -s <release-secret-key>\n", distDir)
	fmt.Printf("RELEASE:   git tag -s v%s\n", version)
	return nil
}

func tarGzDir(dir, archive string) (err error) {
	out, err := os.Create(archive)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	defer func() {
		// Close in writer order; a flush failure means a corrupt archive,
		// so the first close error wins over a nil return.
		for _, c := range []io.Closer{tw, gz, out} {
			if cerr := c.Close(); cerr != nil && err == nil {
				err = cerr
			}
		}
	}()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := addFileToTar(tw, filepath.Join(dir, entry.Name()), entry); err != nil {
			return err
		}
	}
	return nil
}

func addFileToTar(tw *tar.Writer, path string, entry os.DirEntry) error {
	info, err := entry.Info()
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(tw, f); err != nil {
		if cerr := f.Close(); cerr != nil {
			return fmt.Errorf("%w (also failed to close: %w)", err, cerr)
		}
		return err
	}
	return f.Close()
}

func writeSHA256SUMS(distDir string, archives []string) error {
	manifest := ""
	for _, archive := range archives {
		sum, err := sha256File(archive)
		if err != nil {
			return err
		}
		manifest += sum + "  " + filepath.Base(archive) + "\n"
	}
	return os.WriteFile(filepath.Join(distDir, "SHA256SUMS"), []byte(manifest), 0600)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		if cerr := f.Close(); cerr != nil {
			return "", fmt.Errorf("%w (also failed to close: %w)", err, cerr)
		}
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
