// Package media walks the catalog media tree and produces an index of the
// files that actually exist on disk.
//
// Walking is sharded by top-level directory and executed in parallel, because
// a mature Magento catalog routinely holds hundreds of thousands of files
// spread over a handful of first-letter buckets. That layout makes it trivial
// to divide the work without lock contention.
package media

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// CacheDirName is the directory Magento fills with derived thumbnail variants.
const CacheDirName = "cache"

// File describes one media file on disk.
type File struct {
	// RelPath is slash-separated and relative to the media root, matching the
	// form stored in Magento's media_gallery table (minus its leading slash).
	RelPath string
	Size    int64
	ModTime time.Time
}

// ScanResult is the outcome of walking a media tree.
type ScanResult struct {
	Root string `json:"root"`

	// Files holds the original images, sorted by RelPath. Derived cache files
	// are excluded unless the caller asked for them.
	Files []File `json:"-"`
	// Bytes is the total size of Files.
	Bytes int64 `json:"bytes"`

	// CacheFiles/CacheBytes/CacheDirs describe the derived thumbnail cache,
	// which is always measured even when it is not indexed, because its size
	// is one of the headline numbers of a report.
	CacheFiles int64 `json:"cacheFiles"`
	CacheBytes int64 `json:"cacheBytes"`
	CacheDirs  int   `json:"cacheDirs"`

	SkippedDirs []string      `json:"skippedDirs,omitempty"`
	Errors      []string      `json:"errors,omitempty"`
	Duration    time.Duration `json:"duration"`
}

// ScanOptions tunes the walk.
type ScanOptions struct {
	// Workers is the number of parallel directory walkers.
	Workers int
	// IncludeCache indexes the cache directory as regular files instead of
	// measuring it separately. Almost never what you want.
	IncludeCache bool
	// ExcludeNames are top-level entry names to skip.
	ExcludeNames []string
	// ExcludeGlobs skips entries whose slash-separated path relative to the
	// root, or whose base name, matches one of these shell patterns. A
	// matching directory is skipped whole.
	ExcludeGlobs []string
	// Progress receives the running file count.
	Progress func(files int64)
}

// Scan walks root and returns the media index.
func Scan(ctx context.Context, root string, opts ScanOptions) (*ScanResult, error) {
	started := time.Now()
	if opts.Workers <= 0 {
		opts.Workers = runtime.NumCPU()
		if opts.Workers > 16 {
			opts.Workers = 16
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read media root %s: %w", root, err)
	}

	excluded := make(map[string]struct{}, len(opts.ExcludeNames))
	for _, n := range opts.ExcludeNames {
		excluded[n] = struct{}{}
	}

	res := &ScanResult{Root: root}
	type job struct {
		abs   string
		isCac bool
	}

	jobs := make([]job, 0, len(entries))

	// Top-level files are rare but legal in some catalogs, so handle them
	// here rather than silently dropping them.
	var topFiles []File
	for _, e := range entries {
		name := e.Name()
		if _, skip := excluded[name]; skip {
			res.SkippedDirs = append(res.SkippedDirs, name)
			continue
		}
		if !e.IsDir() {
			info, err := e.Info()
			if err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", name, err))
				continue
			}
			topFiles = append(topFiles, File{
				RelPath: name,
				Size:    info.Size(),
				ModTime: info.ModTime(),
			})
			continue
		}
		jobs = append(jobs, job{
			abs: filepath.Join(root, name),
			// Only treat the cache as a separate tree when the caller wants it
			// kept out of the index; IncludeCache means "index it normally".
			isCac: name == CacheDirName && !opts.IncludeCache,
		})
	}

	var (
		mu           sync.Mutex
		files        []File
		cacheN       int64
		cacheB       int64
		cacheD       int
		errs         []string
		skipped      []string
		counted      int64
		excludeGlobs = opts.ExcludeGlobs
		jobQueue     = make(chan job)
		wg           sync.WaitGroup
	)

	workers := opts.Workers
	if workers > len(jobs) && len(jobs) > 0 {
		workers = len(jobs)
	}
	if workers < 1 {
		workers = 1
	}

	reportProgress := func() {
		if opts.Progress == nil {
			return
		}
		opts.Progress(atomic.LoadInt64(&counted))
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local []File
			var localCacheN, localCacheB int64
			var localCacheD int
			var localErrs []string
			var localSkipped []string

			for j := range jobQueue {
				if ctx.Err() != nil {
					return
				}
				walkErr := filepath.WalkDir(j.abs, func(p string, d fs.DirEntry, err error) error {
					if err != nil {
						localErrs = append(localErrs, fmt.Sprintf("%s: %v", p, err))
						if d != nil && d.IsDir() {
							return fs.SkipDir
						}
						return nil
					}
					if ctx.Err() != nil {
						return ctx.Err()
					}

					rel, relErr := filepath.Rel(root, p)
					if relErr != nil {
						localErrs = append(localErrs, fmt.Sprintf("%s: %v", p, relErr))
						if d.IsDir() {
							return fs.SkipDir
						}
						return nil
					}
					rel = filepath.ToSlash(rel)

					if len(excludeGlobs) > 0 && matchesAnyGlob(excludeGlobs, rel, d.Name()) {
						localSkipped = append(localSkipped, rel)
						if d.IsDir() {
							return fs.SkipDir
						}
						return nil
					}

					if d.IsDir() {
						if j.isCac {
							localCacheD++
						}
						return nil
					}

					if j.isCac {
						localCacheN++
						if info, infoErr := d.Info(); infoErr == nil {
							localCacheB += info.Size()
						} else {
							localCacheB += 0
						}
						n := atomic.AddInt64(&counted, 1)
						if n%20000 == 0 {
							reportProgress()
						}
						return nil
					}

					info, infoErr := d.Info()
					if infoErr != nil {
						localErrs = append(localErrs, fmt.Sprintf("%s: %v", p, infoErr))
						return nil
					}
					local = append(local, File{RelPath: rel, Size: info.Size(), ModTime: info.ModTime()})
					n := atomic.AddInt64(&counted, 1)
					if n%20000 == 0 {
						reportProgress()
					}
					return nil
				})
				if walkErr != nil && !errors.Is(walkErr, context.Canceled) {
					localErrs = append(localErrs, fmt.Sprintf("%s: %v", j.abs, walkErr))
				}
			}

			mu.Lock()
			files = append(files, local...)
			cacheN += localCacheN
			cacheB += localCacheB
			cacheD += localCacheD
			errs = append(errs, localErrs...)
			skipped = append(skipped, localSkipped...)
			mu.Unlock()
		}()
	}

	for _, j := range jobs {
		jobQueue <- j
	}
	close(jobQueue)
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	files = append(files, topFiles...)
	sort.Slice(files, func(i, j int) bool { return files[i].RelPath < files[j].RelPath })

	var total int64
	for _, f := range files {
		total += f.Size
	}

	res.Files = files
	res.Bytes = total
	res.CacheFiles = cacheN
	res.CacheBytes = cacheB
	res.CacheDirs = cacheD
	res.Errors = errs
	res.SkippedDirs = append(res.SkippedDirs, skipped...)
	sort.Strings(res.SkippedDirs)
	res.SkippedDirs = dedupeSorted(res.SkippedDirs)
	res.Duration = time.Since(started)
	if opts.Progress != nil {
		opts.Progress(int64(len(files)))
	}
	return res, nil
}

// Find looks up a relative path in a sorted file slice.
//
// Binary search keeps the orphan analysis allocation-free regardless of how
// many files the catalog holds.
func Find(sorted []File, relPath string) (File, bool) {
	i := sort.Search(len(sorted), func(i int) bool { return sorted[i].RelPath >= relPath })
	if i < len(sorted) && sorted[i].RelPath == relPath {
		return sorted[i], true
	}
	return File{}, false
}

// HumanBytes renders a byte count using binary units.
func HumanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	return fmt.Sprintf("%.2f %s", float64(b)/float64(div), units[exp])
}

// IsCachePath reports whether a slash-separated relative path lives inside the
// derived cache tree.
func IsCachePath(rel string) bool {
	return rel == CacheDirName || strings.HasPrefix(rel, CacheDirName+"/")
}

// matchesAnyGlob reports whether a slash-separated relative path, or an
// entry's base name, matches one of the shell patterns.
//
// An invalid pattern simply never matches: a typo in a config file must not
// be able to silently exclude an entire tree.
func matchesAnyGlob(patterns []string, rel, base string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, rel); err == nil && ok {
			return true
		}
		if ok, err := path.Match(p, base); err == nil && ok {
			return true
		}
	}
	return false
}

// dedupeSorted collapses adjacent duplicates in a sorted slice, in place.
func dedupeSorted(in []string) []string {
	if len(in) < 2 {
		return in
	}
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}
