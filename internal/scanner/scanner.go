package scanner

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

type fileEntry struct {
	path    string
	modTime time.Time
}

// ScanAll walks all glob patterns, applies min/max age filters, and returns
// matched file paths sorted by direction ("oldest" first or "newest" first).
// Zero-value minAge or maxAge means no limit.
func ScanAll(patterns []string, direction string, minAge, maxAge time.Duration) ([]string, error) {
	now := time.Now()
	seen := make(map[string]bool)
	var entries []fileEntry

	for _, pattern := range patterns {
		base, rel := splitPattern(pattern)
		fsys := os.DirFS(base)

		err := doublestar.GlobWalk(fsys, rel, func(path string, d fs.DirEntry) error {
			if d.IsDir() {
				return nil
			}
			absPath := filepath.Join(base, path)
			if seen[absPath] {
				return nil
			}

			info, err := d.Info()
			if err != nil {
				return nil // skip unreadable entries
			}
			age := now.Sub(info.ModTime())

			if minAge > 0 && age < minAge {
				return nil
			}
			if maxAge > 0 && age > maxAge {
				return nil
			}

			seen[absPath] = true
			entries = append(entries, fileEntry{path: absPath, modTime: info.ModTime()})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if direction == "newest" {
			return entries[i].modTime.After(entries[j].modTime)
		}
		return entries[i].modTime.Before(entries[j].modTime)
	})

	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.path
	}
	return paths, nil
}

// splitPattern separates an absolute glob pattern like /recordings/**/*.ts into
// a filesystem base (/recordings) and a doublestar pattern (**/*.ts).
func splitPattern(pattern string) (base, rel string) {
	dir := filepath.Dir(pattern)
	for dir != "/" && dir != "." && containsGlob(dir) {
		dir = filepath.Dir(dir)
	}
	rel, _ = filepath.Rel(dir, pattern)
	return dir, rel
}

func containsGlob(s string) bool {
	for _, c := range s {
		if c == '*' || c == '?' || c == '[' || c == '{' {
			return true
		}
	}
	return false
}
