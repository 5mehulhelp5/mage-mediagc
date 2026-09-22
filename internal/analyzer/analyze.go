// Package analyzer decides which files on disk are still referenced and which
// are orphans, and it gathers the statistics that go into a report.
//
// The judgement is deliberately conservative: a file counts as live when any
// reference shape matches it, including a case-insensitive match. A false
// "live" costs a little disk space; a false "orphan" destroys an image.
package analyzer

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/shuaiZend/mage-mediagc/internal/magento"
	"github.com/shuaiZend/mage-mediagc/internal/media"
)

// Options tunes the analysis.
type Options struct {
	// CaseInsensitiveFallback keeps a file when a reference differs only by
	// case. Enabled by default because a few importers lowercase paths.
	CaseInsensitiveFallback bool
	// SafetyThreshold triggers a warning when the orphan ratio exceeds it.
	// A ratio this extreme usually means reference collection failed rather
	// than that the catalog really is that full of garbage.
	SafetyThreshold float64
}

// DefaultOptions returns the recommended analysis settings.
func DefaultOptions() Options {
	return Options{
		CaseInsensitiveFallback: true,
		SafetyThreshold:         0.98,
	}
}

// DirStat aggregates files below one second-level directory bucket.
type DirStat struct {
	Dir         string `json:"dir"`
	Total       int64  `json:"total"`
	Orphans     int64  `json:"orphans"`
	Bytes       int64  `json:"bytes"`
	OrphanBytes int64  `json:"orphanBytes"`
}

// Result is the full analysis outcome.
type Result struct {
	MediaRoot string `json:"mediaRoot"`

	DiskFiles int64 `json:"diskFiles"`
	DiskBytes int64 `json:"diskBytes"`

	CacheFiles int64 `json:"cacheFiles"`
	CacheBytes int64 `json:"cacheBytes"`

	RefPaths int                  `json:"refPaths"`
	RefStats []magento.SourceStat `json:"refStats"`

	LiveFiles int64 `json:"liveFiles"`
	LiveBytes int64 `json:"liveBytes"`

	OrphanFiles int64 `json:"orphanFiles"`
	OrphanBytes int64 `json:"orphanBytes"`

	// Orphans lists every orphaned file, sorted by path.
	Orphans []media.File `json:"-"`
	// Missing lists references with no file on disk.
	Missing []string `json:"missing"`

	// Dirs summarizes per-bucket totals, most orphaned first.
	Dirs []DirStat `json:"dirs"`

	Warnings []string      `json:"warnings,omitempty"`
	Duration time.Duration `json:"duration"`

	scan media.ScanResult
}

// Scan returns the underlying filesystem scan, for reporting.
func (r *Result) Scan() media.ScanResult { return r.scan }

// Analyze compares the filesystem index against the reference set.
func Analyze(ctx context.Context, scan *media.ScanResult, refs *magento.RefSet, opts Options) (*Result, error) {
	started := time.Now()
	if opts.SafetyThreshold == 0 {
		opts.SafetyThreshold = DefaultOptions().SafetyThreshold
	}

	res := &Result{
		MediaRoot:  scan.Root,
		DiskFiles:  int64(len(scan.Files)),
		DiskBytes:  scan.Bytes,
		CacheFiles: scan.CacheFiles,
		CacheBytes: scan.CacheBytes,
		RefPaths:   refs.Len(),
		RefStats:   refs.Stats,
		scan:       *scan,
	}

	// Some references point outside the scanned tree (wysiwyg assets, theme
	// images). Restrict the missing-file check to paths whose leading
	// directory actually exists under the media root, otherwise every CMS
	// image would be reported as missing.
	topLevel := make(map[string]struct{}, 64)
	for _, f := range scan.Files {
		if i := strings.IndexByte(f.RelPath, '/'); i > 0 {
			topLevel[f.RelPath[:i]] = struct{}{}
		}
	}

	var foldIndex map[string][]string
	if opts.CaseInsensitiveFallback {
		foldIndex = make(map[string][]string, len(refs.Paths)/2)
		for p := range refs.Paths {
			lp := strings.ToLower(p)
			foldIndex[lp] = append(foldIndex[lp], p)
		}
	}

	dirs := make(map[string]*DirStat, 512)

	for _, f := range scan.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		live := protectedPath(f.RelPath)
		if !live {
			if _, ok := refs.Paths[f.RelPath]; ok {
				live = true
			} else if foldIndex != nil {
				if _, ok := foldIndex[strings.ToLower(f.RelPath)]; ok {
					live = true
				}
			}
		}

		key := bucketOf(f.RelPath)
		d := dirs[key]
		if d == nil {
			d = &DirStat{Dir: key}
			dirs[key] = d
		}

		if live {
			res.LiveFiles++
			res.LiveBytes += f.Size
			d.Total++
			d.Bytes += f.Size
			continue
		}

		res.OrphanFiles++
		res.OrphanBytes += f.Size
		res.Orphans = append(res.Orphans, f)
		d.Total++
		d.Bytes += f.Size
		d.Orphans++
		d.OrphanBytes += f.Size
	}

	// Missing files: references inside the scanned tree with no counterpart.
	for p := range refs.Paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if media.IsCachePath(p) {
			continue
		}
		head := p
		if i := strings.IndexByte(p, '/'); i > 0 {
			head = p[:i]
		}
		if _, inScope := topLevel[head]; !inScope {
			continue
		}
		if _, ok := media.Find(scan.Files, p); !ok {
			res.Missing = append(res.Missing, p)
		}
	}
	sort.Strings(res.Missing)

	res.Dirs = make([]DirStat, 0, len(dirs))
	for _, d := range dirs {
		res.Dirs = append(res.Dirs, *d)
	}
	sort.Slice(res.Dirs, func(i, j int) bool {
		if res.Dirs[i].Orphans != res.Dirs[j].Orphans {
			return res.Dirs[i].Orphans > res.Dirs[j].Orphans
		}
		return res.Dirs[i].Dir < res.Dirs[j].Dir
	})

	res.Warnings = warningsFor(res, opts)
	res.Duration = time.Since(started)
	return res, nil
}

// bucketOf returns the grouping key used for per-directory statistics.
//
// Magento stores originals under `catalog/product/<c>/<c>/<file>`, so the
// first two path segments already partition the tree exactly the way the
// storage layout does. Files sitting closer to the root get a finer key
// (directory plus file stem) so that one bucket cannot swallow an entire
// catalog and hide where the garbage actually lives.
func bucketOf(rel string) string {
	parts := strings.SplitN(rel, "/", 3)
	switch len(parts) {
	case 1:
		return "(root)"
	case 2:
		return parts[0] + "/" + trimExtension(parts[1])
	default:
		return parts[0] + "/" + parts[1]
	}
}

// protectedPath reports whether a path is live by construction, without
// consulting the database.
//
// Magento keeps its placeholder images under catalog/product/placeholder/ and
// references them only from core_config_data (the catalog/placeholder/* paths),
// which is a configuration table rather than a media table. Reference
// collection reads media tables, so it cannot see them, and without this guard a
// stock installation would have its placeholders reported as orphans and moved
// out of the media tree — the exact false-orphan failure the rest of the design
// exists to prevent.
//
// The directory holds a handful of images and every file in it is live by
// definition, so the prefix is protected outright rather than resolved through
// configuration.
func protectedPath(rel string) bool {
	rel = strings.TrimPrefix(rel, "./")
	return rel == placeholderDir || strings.HasPrefix(rel, placeholderDir+"/")
}

// placeholderDir is the placeholder directory name, relative to the scan root.
const placeholderDir = "placeholder"

// trimExtension drops a trailing ".ext" from a file name, leaving dotfiles
// and extension-less names untouched.
func trimExtension(name string) string {
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		return name[:i]
	}
	return name
}

func warningsFor(res *Result, opts Options) []string {
	var warns []string

	if res.DiskFiles == 0 {
		warns = append(warns, "no files were found under the media root: check --media-path")
		return warns
	}
	if res.RefPaths == 0 {
		warns = append(warns,
			"the database returned no media references at all, which almost certainly means "+
				"the database name or table prefix is wrong; refusing to call anything an orphan")
		return warns
	}

	ratio := float64(res.OrphanFiles) / float64(res.DiskFiles)
	if ratio > opts.SafetyThreshold {
		warns = append(warns, fmt.Sprintf(
			"%.1f%% of files look orphaned (threshold %.0f%%): verify that every reference source "+
				"was collected before removing anything", ratio*100, opts.SafetyThreshold*100))
	}
	if len(res.Missing) > 0 {
		warns = append(warns, fmt.Sprintf(
			"%d referenced files are missing from disk; run `mage-mediagc scan --format markdown` "+
				"and inspect them before cleaning the database", len(res.Missing)))
	}
	return warns
}

// OrphanSet returns the orphan paths as a set, which is what the cleanup
// commands need.
func (r *Result) OrphanSet() map[string]struct{} {
	out := make(map[string]struct{}, len(r.Orphans))
	for _, f := range r.Orphans {
		out[f.RelPath] = struct{}{}
	}
	return out
}

// TopOrphanDirs returns up to n directory buckets with the highest orphan
// counts, useful for highlighting where garbage concentrates.
func (r *Result) TopOrphanDirs(n int) []DirStat {
	if n > len(r.Dirs) {
		n = len(r.Dirs)
	}
	out := make([]DirStat, 0, n)
	for _, d := range r.Dirs {
		if d.Orphans == 0 {
			continue
		}
		out = append(out, d)
		if len(out) == n {
			break
		}
	}
	return out
}

// BucketKeyOf exposes bucketOf for callers that need to map a path the same
// way the analyzer does.
func BucketKeyOf(rel string) string { return bucketOf(path.Clean(rel)) }
