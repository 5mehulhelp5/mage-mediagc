// Package action implements the mutating operations: clearing the derived
// thumbnail cache, quarantining orphaned files, restoring from quarantine and
// purging it.
//
// Every operation is written so that a mistake is recoverable. Nothing in
// this package deletes an original image outright; orphaning is always
// expressed as a move into a quarantine directory on the same filesystem,
// which is effectively an inode rename and therefore instant regardless of
// file size or count.
package action

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/shuaiZend/mage-mediagc/internal/media"
)

// CacheResult summarizes a thumbnail cache cleanup.
type CacheResult struct {
	CacheDir     string `json:"cacheDir"`
	RemovedFiles int64  `json:"removedFiles"`
	RemovedBytes int64  `json:"removedBytes"`
	Recreated    bool   `json:"recreated"`
	DryRun       bool   `json:"dryRun"`
}

// CleanCache removes Magento's derived thumbnail cache.
//
// The cache is pure derived data: Magento regenerates each variant on first
// request after the directory is emptied. Nothing references these files by
// name, so this is the one operation with no downside beyond a temporary
// increase in image-resize work.
func CleanCache(ctx context.Context, mediaRoot string, dryRun bool, progress func(removed, bytes int64)) (*CacheResult, error) {
	cacheDir := filepath.Join(mediaRoot, media.CacheDirName)

	info, err := os.Stat(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return &CacheResult{CacheDir: cacheDir, DryRun: dryRun}, nil
		}
		return nil, fmt.Errorf("stat cache dir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s exists but is not a directory", cacheDir)
	}

	// Record ownership so the recreated directory keeps the web server's
	// user, which matters when running as root. Platforms without POSIX
	// ownership report ok == false, and the chown below is then skipped.
	uid, gid, haveOwner := dirOwnership(info)

	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return nil, fmt.Errorf("read cache dir: %w", err)
	}

	res := &CacheResult{CacheDir: cacheDir, DryRun: dryRun}

	// Measure before removing so a dry run reports the same numbers.
	var totalFiles, totalBytes int64
	for _, e := range entries {
		p := filepath.Join(cacheDir, e.Name())
		_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				// An unreadable entry cannot be measured; it will still be
				// removed by the RemoveAll below, so skipping is correct.
				return nil //nolint:nilerr // measurement is best-effort
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.IsDir() {
				return nil
			}
			totalFiles++
			if fi, err := d.Info(); err == nil {
				totalBytes += fi.Size()
			}
			if totalFiles%20000 == 0 && progress != nil {
				progress(totalFiles, totalBytes)
			}
			return nil
		})
	}

	res.RemovedFiles = totalFiles
	res.RemovedBytes = totalBytes
	if progress != nil {
		progress(totalFiles, totalBytes)
	}

	if dryRun {
		return res, nil
	}

	for _, e := range entries {
		p := filepath.Join(cacheDir, e.Name())
		if err := os.RemoveAll(p); err != nil {
			return res, fmt.Errorf("remove %s: %w", p, err)
		}
	}

	if err := os.MkdirAll(cacheDir, 0o775); err != nil {
		return res, fmt.Errorf("recreate cache dir: %w", err)
	}
	if haveOwner && (uid != 0 || gid != 0) {
		// Best effort: non-root users cannot chown, and that is fine.
		_ = os.Chown(cacheDir, uid, gid)
	}
	res.Recreated = true
	return res, nil
}

// CacheStats measures the cache directory without touching it.
func CacheStats(ctx context.Context, mediaRoot string) (files int64, bytes int64, err error) {
	cacheDir := filepath.Join(mediaRoot, media.CacheDirName)
	if _, statErr := os.Stat(cacheDir); statErr != nil {
		if os.IsNotExist(statErr) {
			return 0, 0, nil
		}
		return 0, 0, statErr
	}
	err = filepath.WalkDir(cacheDir, func(_ string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Missing permissions on a subtree should not fail a size report.
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
	return files, bytes, err
}

// EnsureMediaPathExists validates that a media root is usable before any
// write operation runs.
func EnsureMediaPathExists(mediaRoot string) error {
	info, err := os.Stat(mediaRoot)
	if err != nil {
		return fmt.Errorf("media path %s is not accessible: %w", mediaRoot, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("media path %s is not a directory", mediaRoot)
	}
	return nil
}

// verifySameFilesystem makes sure a move will be an inode rename rather than a
// cross-device copy, which would double disk usage and take hours.
func verifySameFilesystem(a, b string) error {
	devA, err := deviceOf(a)
	if err != nil {
		return err
	}
	devB, err := deviceOf(b)
	if err != nil {
		return err
	}
	if devA != devB {
		return fmt.Errorf(
			"quarantine directory %s is on a different filesystem than %s "+
				"(device %d vs %d): moving files would copy them and temporarily double disk usage. "+
				"Point --quarantine-dir at the same volume, or pass --allow-cross-device",
			b, a, devB, devA)
	}
	return nil
}

func deviceOf(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	dev, ok := deviceID(path, info)
	if !ok {
		return 0, fmt.Errorf("cannot determine the filesystem device for %s", path)
	}
	return dev, nil
}

// safeRelJoin joins a relative path onto a base, refusing escapes.
//
// Orphan paths come from the filesystem scan and should be trustworthy, but a
// destructive tool should never rely on that.
func safeRelJoin(base, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("empty relative path")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute path %q is not allowed here", rel)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the base directory", rel)
	}
	joined := filepath.Join(base, clean)
	// Defense in depth: the result must stay under base.
	relCheck, err := filepath.Rel(base, joined)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the base directory", rel)
	}
	return joined, nil
}
