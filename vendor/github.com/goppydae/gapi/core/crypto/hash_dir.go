package crypto

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/zeebo/blake3"
)

// HashDirectory computes a single BLAKE3 hash for all matching files in a directory
func HashDirectory(dir string, pattern string) (string, error) {
	var files []string

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			matched, _ := filepath.Match(pattern, filepath.Base(path))
			if matched {
				files = append(files, path)
			}
		}
		return nil
	})

	if err != nil {
		return "", err
	}

	if len(files) == 0 {
		return "", fmt.Errorf("no files matching pattern %s in %s", pattern, dir)
	}

	// Sort for deterministic hashing
	sort.Strings(files)

	// Hash each file and combine
	hasher := blake3.New()
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		// Include relative path for uniqueness
		relPath, _ := filepath.Rel(dir, file)
		hasher.Write([]byte(relPath))
		hasher.Write(data)
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}
