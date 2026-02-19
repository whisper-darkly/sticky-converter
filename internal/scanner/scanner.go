package scanner

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/whisper-darkly/sticky-refinery/internal/config"
)

// CandidateFile is a file that passed all filters for a pipeline.
type CandidateFile struct {
	Path         string
	PipelineName string
	Priority     int
	ModTime      time.Time
	Direction    string // "oldest" or "newest"
}

// ScanAll walks all pipeline paths, applies min/max age filters, and returns
// a deduplicated, priority-sorted list of candidates.
func ScanAll(pipelines []config.PipelineConfig) ([]*CandidateFile, error) {
	now := time.Now()
	seen := make(map[string]bool)
	var candidates []*CandidateFile

	for _, p := range pipelines {
		found, err := scanPipeline(p, now, seen)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, found...)
	}

	// Sort: lower priority number first; within same priority by modtime
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority < candidates[j].Priority
		}
		if candidates[i].Direction == "oldest" {
			return candidates[i].ModTime.Before(candidates[j].ModTime)
		}
		return candidates[i].ModTime.After(candidates[j].ModTime)
	})

	return candidates, nil
}

func scanPipeline(p config.PipelineConfig, now time.Time, seen map[string]bool) ([]*CandidateFile, error) {
	var out []*CandidateFile

	for _, pattern := range p.Paths {
		// doublestar requires a base path and a relative pattern
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

			if p.MinAge != nil && age < p.MinAge.Duration {
				return nil
			}
			if p.MaxAge != nil && age > p.MaxAge.Duration {
				return nil
			}

			seen[absPath] = true
			out = append(out, &CandidateFile{
				Path:         absPath,
				PipelineName: p.Name,
				Priority:     p.Priority,
				ModTime:      info.ModTime(),
				Direction:    p.Direction,
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// splitPattern separates an absolute glob pattern like /recordings/**/*.ts into
// a filesystem base (/recordings) and a doublestar pattern (**/*.ts).
// It walks forward until it hits a glob metacharacter.
func splitPattern(pattern string) (base, rel string) {
	dir := filepath.Dir(pattern)
	// Walk up until no glob chars in the directory portion
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
