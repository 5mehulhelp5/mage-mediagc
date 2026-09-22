package action

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shuaiZend/mage-mediagc/internal/media"
)

// ManifestName is the file inside a quarantine directory that records every
// move, so the operation can be reversed precisely.
const ManifestName = "_mage-mediagc-manifest.jsonl"

// ManifestMeta records the parameters of the run that populated a quarantine.
const ManifestMeta = "_mage-mediagc-meta.json"

// ManifestEntry is one recorded move.
type ManifestEntry struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime,omitempty"`
}

// QuarantineMeta describes a quarantine directory.
type QuarantineMeta struct {
	CreatedAt string `json:"createdAt"`
	MediaRoot string `json:"mediaRoot"`
	Tool      string `json:"tool"`
	Version   string `json:"version"`
}

// QuarantineOptions configures an orphan-isolation run.
type QuarantineOptions struct {
	MediaRoot        string
	QuarantineDir    string
	Orphans          []media.File
	DryRun           bool
	Parallel         int
	AllowCrossDevice bool
	ToolVersion      string
	Progress         func(done, total int64, bytes int64)
}

// QuarantineResult summarizes an isolation run.
type QuarantineResult struct {
	QuarantineDir string        `json:"quarantineDir"`
	ManifestPath  string        `json:"manifestPath"`
	Requested     int64         `json:"requested"`
	Moved         int64         `json:"moved"`
	Bytes         int64         `json:"bytes"`
	Failed        int64         `json:"failed"`
	Failures      []string      `json:"failures,omitempty"`
	DryRun        bool          `json:"dryRun"`
	Duration      time.Duration `json:"duration"`
}

// Quarantine moves orphaned files into an isolation directory.
//
// Files are moved, never copied: on the same filesystem os.Rename is an inode
// operation that completes in microseconds, so isolating hundreds of
// thousands of files takes seconds and consumes no additional space. The run
// refuses to proceed across filesystems unless explicitly allowed, because a
// silent copy would fill the disk.
func Quarantine(ctx context.Context, opts QuarantineOptions) (*QuarantineResult, error) {
	started := time.Now()
	if opts.Parallel <= 0 {
		opts.Parallel = 8
	}
	res := &QuarantineResult{
		QuarantineDir: opts.QuarantineDir,
		Requested:     int64(len(opts.Orphans)),
		DryRun:        opts.DryRun,
	}
	if len(opts.Orphans) == 0 {
		res.Duration = time.Since(started)
		return res, nil
	}

	if err := EnsureMediaPathExists(opts.MediaRoot); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(opts.QuarantineDir, 0o750); err != nil {
		return nil, fmt.Errorf("create quarantine dir: %w", err)
	}
	if !opts.AllowCrossDevice {
		if err := verifySameFilesystem(opts.MediaRoot, opts.QuarantineDir); err != nil {
			return nil, err
		}
	}

	// Validate every path before touching disk. A path that would escape the
	// media root (or the quarantine directory) is recorded as a failure and
	// dropped from the run: one corrupt entry must never abort an otherwise
	// valid cleanup, and it must never reach os.Rename.
	valid := make([]media.File, 0, len(opts.Orphans))
	var rejected []string
	for _, f := range opts.Orphans {
		if _, err := safeRelJoin(opts.MediaRoot, f.RelPath); err != nil {
			rejected = append(rejected, fmt.Sprintf("%s: %v", f.RelPath, err))
			continue
		}
		if _, err := safeRelJoin(opts.QuarantineDir, f.RelPath); err != nil {
			rejected = append(rejected, fmt.Sprintf("%s: %v", f.RelPath, err))
			continue
		}
		valid = append(valid, f)
	}
	sort.Strings(rejected)

	if len(valid) == 0 {
		res.Failed = int64(len(rejected))
		res.Failures = rejected
		res.Duration = time.Since(started)
		return res, nil
	}

	// Work out how many bytes are about to move so progress is meaningful.
	var planned int64
	for _, f := range valid {
		planned += f.Size
	}

	if opts.DryRun {
		res.Moved = 0
		res.Bytes = planned
		res.Failed = int64(len(rejected))
		res.Failures = rejected
		res.ManifestPath = filepath.Join(opts.QuarantineDir, ManifestName)
		res.Duration = time.Since(started)
		return res, nil
	}

	// Pre-create the directory skeleton. Doing this up front turns the hot
	// loop into pure renames.
	dirSet := make(map[string]struct{}, len(valid)/4+16)
	for _, f := range valid {
		d := path.Dir(f.RelPath)
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
		target, err := safeRelJoin(opts.QuarantineDir, d)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(target, 0o750); err != nil {
			return nil, fmt.Errorf("create quarantine subdir %s: %w", target, err)
		}
	}

	manifestPath := filepath.Join(opts.QuarantineDir, ManifestName)
	manifest, err := os.OpenFile(manifestPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open manifest: %w", err)
	}
	writer := bufio.NewWriterSize(manifest, 1<<20)
	enc := json.NewEncoder(writer)

	var (
		moved    int64
		bytes    int64
		failed   int64
		mu       sync.Mutex
		failures []string
		done     int64
	)

	work := make(chan media.File)
	var wg sync.WaitGroup
	workers := opts.Parallel
	if workers > len(valid) {
		workers = len(valid)
	}
	if workers < 1 {
		workers = 1
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range work {
				if ctx.Err() != nil {
					return
				}
				src, err := safeRelJoin(opts.MediaRoot, f.RelPath)
				if err != nil {
					mu.Lock()
					failures = append(failures, fmt.Sprintf("%s: %v", f.RelPath, err))
					failed++
					mu.Unlock()
					continue
				}
				dst, err := safeRelJoin(opts.QuarantineDir, f.RelPath)
				if err != nil {
					mu.Lock()
					failures = append(failures, fmt.Sprintf("%s: %v", f.RelPath, err))
					failed++
					mu.Unlock()
					continue
				}
				if err := os.Rename(src, dst); err != nil {
					// A file that vanished between the scan and now is not an
					// error worth stopping for.
					if os.IsNotExist(err) {
						mu.Lock()
						failures = append(failures, fmt.Sprintf("%s: disappeared before move", f.RelPath))
						failed++
						mu.Unlock()
						continue
					}
					mu.Lock()
					failures = append(failures, fmt.Sprintf("%s: %v", f.RelPath, err))
					failed++
					mu.Unlock()
					continue
				}

				entry := ManifestEntry{Path: f.RelPath, Size: f.Size}
				if !f.ModTime.IsZero() {
					entry.ModTime = f.ModTime.UTC().Format(time.RFC3339)
				}
				mu.Lock()
				if encErr := enc.Encode(entry); encErr != nil {
					failures = append(failures, fmt.Sprintf("%s: manifest write failed: %v", f.RelPath, encErr))
					failed++
				}
				moved++
				bytes += f.Size
				mu.Unlock()

				n := atomic.AddInt64(&done, 1)
				if opts.Progress != nil && n%2000 == 0 {
					opts.Progress(n, res.Requested, atomic.LoadInt64(&bytes))
				}
			}
		}()
	}

	for _, f := range valid {
		select {
		case <-ctx.Done():
			close(work)
			wg.Wait()
			return res, ctx.Err()
		case work <- f:
		}
	}
	close(work)
	wg.Wait()

	mu.Lock()
	flushErr := writer.Flush()
	closeErr := manifest.Close()
	if flushErr != nil {
		failures = append(failures, fmt.Sprintf("manifest flush: %v", flushErr))
	}
	if closeErr != nil {
		failures = append(failures, fmt.Sprintf("manifest close: %v", closeErr))
	}
	res.Moved = moved
	res.Bytes = bytes
	res.Failed = failed + int64(len(rejected))
	failures = append(rejected, failures...)
	sort.Strings(failures)
	if len(failures) > 100 {
		failures = failures[:100]
	}
	res.Failures = failures
	mu.Unlock()

	res.ManifestPath = manifestPath
	if err := writeMeta(opts, manifestPath); err != nil {
		res.Failures = append(res.Failures, fmt.Sprintf("meta write: %v", err))
	}

	res.Duration = time.Since(started)
	return res, nil
}

func writeMeta(opts QuarantineOptions, manifestPath string) error {
	metaPath := filepath.Join(opts.QuarantineDir, ManifestMeta)
	if _, err := os.Stat(metaPath); err == nil {
		return nil // keep the first run's provenance
	}
	meta := QuarantineMeta{
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		MediaRoot: opts.MediaRoot,
		Tool:      "mage-mediagc",
		Version:   opts.ToolVersion,
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	_ = manifestPath
	return os.WriteFile(metaPath, append(data, '\n'), 0o640)
}

// LoadManifest reads a quarantine manifest, returning entries in file order.
func LoadManifest(quarantineDir string) ([]ManifestEntry, *QuarantineMeta, error) {
	manifestPath := filepath.Join(quarantineDir, ManifestName)
	f, err := os.Open(manifestPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open manifest %s: %w", manifestPath, err)
	}
	defer f.Close()

	var entries []ManifestEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e ManifestEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, nil, fmt.Errorf("parse manifest line: %w", err)
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}

	var meta QuarantineMeta
	if data, err := os.ReadFile(filepath.Join(quarantineDir, ManifestMeta)); err == nil {
		_ = json.Unmarshal(data, &meta)
	}
	return entries, &meta, nil
}
