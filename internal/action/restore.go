package action

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// RestoreOptions configures a rollback.
type RestoreOptions struct {
	QuarantineDir string
	MediaRoot     string
	DryRun        bool
	Parallel      int
	// OnlyPaths limits the restore to a subset of the manifest.
	OnlyPaths []string
	Progress  func(done, total int64)
}

// RestoreResult summarizes a rollback.
type RestoreResult struct {
	QuarantineDir string        `json:"quarantineDir"`
	Entries       int64         `json:"entries"`
	Restored      int64         `json:"restored"`
	Skipped       int64         `json:"skipped"`
	Failed        int64         `json:"failed"`
	Bytes         int64         `json:"bytes"`
	Failures      []string      `json:"failures,omitempty"`
	DryRun        bool          `json:"dryRun"`
	Duration      time.Duration `json:"duration"`
}

// Restore moves quarantined files back to their original locations.
//
// Existing files at the destination are never overwritten: a skip is reported
// instead, because silently clobbering a newer upload would be worse than
// leaving a file in quarantine.
func Restore(ctx context.Context, opts RestoreOptions) (*RestoreResult, error) {
	started := time.Now()
	if opts.Parallel <= 0 {
		opts.Parallel = 8
	}

	entries, meta, err := LoadManifest(opts.QuarantineDir)
	if err != nil {
		return nil, err
	}
	if opts.MediaRoot == "" && meta != nil {
		opts.MediaRoot = meta.MediaRoot
	}
	if opts.MediaRoot == "" {
		return nil, fmt.Errorf("media root is unknown: pass --media-path (the quarantine metadata did not record one)")
	}
	if err := EnsureMediaPathExists(opts.MediaRoot); err != nil {
		return nil, err
	}

	filter := make(map[string]struct{}, len(opts.OnlyPaths))
	for _, p := range opts.OnlyPaths {
		filter[p] = struct{}{}
	}
	selected := make([]ManifestEntry, 0, len(entries))
	for _, e := range entries {
		if len(filter) > 0 {
			if _, ok := filter[e.Path]; !ok {
				continue
			}
		}
		selected = append(selected, e)
	}

	res := &RestoreResult{
		QuarantineDir: opts.QuarantineDir,
		Entries:       int64(len(selected)),
		DryRun:        opts.DryRun,
	}
	if len(selected) == 0 {
		res.Duration = time.Since(started)
		return res, nil
	}

	if opts.DryRun {
		for _, e := range selected {
			res.Bytes += e.Size
		}
		res.Duration = time.Since(started)
		return res, nil
	}

	// Recreate the original directory skeleton inside the media root.
	dirSet := make(map[string]struct{}, len(selected)/4+16)
	for _, e := range selected {
		d := path.Dir(e.Path)
		if d == "." {
			continue
		}
		dirSet[d] = struct{}{}
	}
	dirs := make([]string, 0, len(dirSet))
	for d := range dirSet {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		target, err := safeRelJoin(opts.MediaRoot, d)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(target, 0o775); err != nil {
			return nil, fmt.Errorf("create %s: %w", target, err)
		}
	}

	var (
		restored int64
		skipped  int64
		failed   int64
		bytes    int64
		mu       sync.Mutex
		failures []string
		done     int64
	)

	work := make(chan ManifestEntry)
	var wg sync.WaitGroup
	workers := opts.Parallel
	if workers > len(selected) {
		workers = len(selected)
	}
	if workers < 1 {
		workers = 1
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range work {
				if ctx.Err() != nil {
					return
				}
				src, err := safeRelJoin(opts.QuarantineDir, e.Path)
				if err != nil {
					mu.Lock()
					failures = append(failures, fmt.Sprintf("%s: %v", e.Path, err))
					failed++
					mu.Unlock()
					continue
				}
				dst, err := safeRelJoin(opts.MediaRoot, e.Path)
				if err != nil {
					mu.Lock()
					failures = append(failures, fmt.Sprintf("%s: %v", e.Path, err))
					failed++
					mu.Unlock()
					continue
				}
				if _, err := os.Stat(dst); err == nil {
					mu.Lock()
					skipped++
					mu.Unlock()
					continue
				}
				if err := os.Rename(src, dst); err != nil {
					mu.Lock()
					failures = append(failures, fmt.Sprintf("%s: %v", e.Path, err))
					failed++
					mu.Unlock()
					continue
				}
				mu.Lock()
				restored++
				bytes += e.Size
				mu.Unlock()

				n := atomic.AddInt64(&done, 1)
				if opts.Progress != nil && n%2000 == 0 {
					opts.Progress(n, res.Entries)
				}
			}
		}()
	}

	for _, e := range selected {
		select {
		case <-ctx.Done():
			close(work)
			wg.Wait()
			return res, ctx.Err()
		case work <- e:
		}
	}
	close(work)
	wg.Wait()

	mu.Lock()
	res.Restored = restored
	res.Skipped = skipped
	res.Failed = failed
	res.Bytes = bytes
	sort.Strings(failures)
	if len(failures) > 100 {
		failures = failures[:100]
	}
	res.Failures = failures
	mu.Unlock()

	// Rewrite the manifest so a second `restore` only retries what is left.
	if err := rewriteManifestAfterRestore(opts.QuarantineDir, entries, selected, res); err != nil {
		res.Failures = append(res.Failures, fmt.Sprintf("manifest rewrite: %v", err))
	}

	res.Duration = time.Since(started)
	return res, nil
}

func rewriteManifestAfterRestore(dir string, all, selected []ManifestEntry, res *RestoreResult) error {
	// Everything restored and nothing left behind: clear the manifest so the
	// directory can be purged with confidence.
	if res.Failed == 0 && res.Skipped == 0 {
		_ = os.Remove(filepath.Join(dir, ManifestName))
		_ = os.Remove(filepath.Join(dir, ManifestMeta))
		pruneEmptyDirs(dir)
		return nil
	}

	remaining := make([]ManifestEntry, 0, len(all))
	restoredSet := make(map[string]struct{}, len(selected))
	if res.Failed == 0 {
		for _, e := range selected {
			restoredSet[e.Path] = struct{}{}
		}
	}
	for _, e := range all {
		if _, ok := restoredSet[e.Path]; ok {
			continue
		}
		remaining = append(remaining, e)
	}

	path_ := filepath.Join(dir, ManifestName)
	f, err := os.Create(path_)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range remaining {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------

// PurgeOptions configures permanent deletion of a quarantine directory.
type PurgeOptions struct {
	QuarantineDir string
	DryRun        bool
	// Force allows deleting a directory that carries no mage-mediagc manifest,
	// which is otherwise treated as "you pointed me at the wrong directory".
	Force    bool
	Progress func(removed int64, bytes int64)
}

// PurgeResult summarizes a purge.
type PurgeResult struct {
	QuarantineDir string        `json:"quarantineDir"`
	Removed       int64         `json:"removed"`
	Bytes         int64         `json:"bytes"`
	DryRun        bool          `json:"dryRun"`
	Duration      time.Duration `json:"duration"`
}

// Purge permanently deletes a quarantine directory, freeing the space that
// isolation only reserved.
func Purge(ctx context.Context, opts PurgeOptions) (*PurgeResult, error) {
	started := time.Now()
	res := &PurgeResult{QuarantineDir: opts.QuarantineDir, DryRun: opts.DryRun}

	info, err := os.Stat(opts.QuarantineDir)
	if err != nil {
		if os.IsNotExist(err) {
			res.Duration = time.Since(started)
			return res, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", opts.QuarantineDir)
	}

	hasManifest := false
	if _, err := os.Stat(filepath.Join(opts.QuarantineDir, ManifestName)); err == nil {
		hasManifest = true
	}

	var files, bytes int64
	walkErr := filepath.WalkDir(opts.QuarantineDir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			// The size figure is only used for reporting and for the
			// manifest guard below, which counts files independently.
			return nil //nolint:nilerr // measurement is best-effort
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		files++
		if fi, infoErr := d.Info(); infoErr == nil {
			bytes += fi.Size()
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	res.Removed = files
	res.Bytes = bytes

	if !hasManifest && files > 0 && !opts.Force {
		return nil, fmt.Errorf(
			"%s holds %d files but no %s manifest: refusing to delete a directory that "+
				"mage-mediagc did not create. Re-run with --force if you are certain",
			opts.QuarantineDir, files, ManifestName)
	}

	if opts.DryRun {
		res.Duration = time.Since(started)
		return res, nil
	}

	if err := os.RemoveAll(opts.QuarantineDir); err != nil {
		return nil, fmt.Errorf("purge %s: %w", opts.QuarantineDir, err)
	}
	res.Duration = time.Since(started)
	return res, nil
}

// pruneEmptyDirs removes empty directories bottom-up, ignoring errors: a
// directory that cannot be removed is harmless.
func pruneEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == root {
			// Best-effort tidy-up: an unreadable directory keeps its contents.
			return nil //nolint:nilerr // best-effort cleanup
		}
		dirs = append(dirs, p)
		return nil
	})
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, d := range dirs {
		_ = os.Remove(d)
	}
}
