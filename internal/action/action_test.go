package action

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/shuaiZend/mage-mediagc/internal/media"
)

func writeFile(t *testing.T, root, rel string, size int) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestCleanCacheDryRunChangesNothing(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "cache/abc/1/2/variant.jpg", 100)
	writeFile(t, root, "a/b/original.jpg", 10)

	res, err := CleanCache(context.Background(), root, true, nil)
	if err != nil {
		t.Fatalf("CleanCache: %v", err)
	}
	if res.RemovedFiles != 1 || res.RemovedBytes != 100 {
		t.Fatalf("unexpected measurement: %+v", res)
	}
	if !exists(filepath.Join(root, "cache", "abc", "1", "2", "variant.jpg")) {
		t.Fatal("dry run must not delete anything")
	}
}

func TestCleanCacheRemovesCacheAndKeepsOriginals(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "cache/abc/1/2/a.jpg", 100)
	writeFile(t, root, "cache/def/3/4/b.jpg", 50)
	writeFile(t, root, "a/b/original.jpg", 10)

	res, err := CleanCache(context.Background(), root, false, nil)
	if err != nil {
		t.Fatalf("CleanCache: %v", err)
	}
	if res.RemovedFiles != 2 || res.RemovedBytes != 150 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !res.Recreated {
		t.Fatal("expected the cache directory to be recreated")
	}

	entries, err := os.ReadDir(filepath.Join(root, "cache"))
	if err != nil {
		t.Fatalf("cache dir should still exist: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("cache dir should be empty, has %d entries", len(entries))
	}
	if !exists(filepath.Join(root, "a", "b", "original.jpg")) {
		t.Fatal("originals must be untouched")
	}
}

func TestCleanCacheWithoutCacheDirIsNoop(t *testing.T) {
	res, err := CleanCache(context.Background(), t.TempDir(), false, nil)
	if err != nil {
		t.Fatalf("CleanCache: %v", err)
	}
	if res.RemovedFiles != 0 {
		t.Fatalf("expected nothing to remove, got %+v", res)
	}
}

func TestQuarantineAndRestoreRoundTrip(t *testing.T) {
	mediaRoot := t.TempDir()
	quarantine := t.TempDir()

	writeFile(t, mediaRoot, "a/b/dead-1.jpg", 10)
	writeFile(t, mediaRoot, "h/-/dead-2.jpg", 20)
	writeFile(t, mediaRoot, "a/b/live.jpg", 30)

	orphans := []media.File{
		{RelPath: "a/b/dead-1.jpg", Size: 10},
		{RelPath: "h/-/dead-2.jpg", Size: 20},
	}

	res, err := Quarantine(context.Background(), QuarantineOptions{
		MediaRoot:     mediaRoot,
		QuarantineDir: quarantine,
		Orphans:       orphans,
		Parallel:      2,
	})
	if err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	if res.Moved != 2 || res.Failed != 0 {
		t.Fatalf("unexpected quarantine result: %+v", res)
	}
	if exists(filepath.Join(mediaRoot, "a/b/dead-1.jpg")) {
		t.Fatal("orphan should have been moved away")
	}
	if !exists(filepath.Join(quarantine, "a/b/dead-1.jpg")) {
		t.Fatal("orphan should now live in quarantine")
	}
	if !exists(filepath.Join(mediaRoot, "a/b/live.jpg")) {
		t.Fatal("live file must stay put")
	}
	if !exists(res.ManifestPath) {
		t.Fatalf("manifest not written at %s", res.ManifestPath)
	}

	// The manifest must describe exactly what moved.
	entries, meta, err := LoadManifest(quarantine)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("manifest has %d entries, want 2", len(entries))
	}
	if meta == nil || meta.MediaRoot != mediaRoot {
		t.Fatalf("manifest metadata is wrong: %+v", meta)
	}

	// Now roll back.
	restoreRes, err := Restore(context.Background(), RestoreOptions{
		QuarantineDir: quarantine,
		MediaRoot:     mediaRoot,
		Parallel:      2,
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if restoreRes.Restored != 2 || restoreRes.Failed != 0 {
		t.Fatalf("unexpected restore result: %+v", restoreRes)
	}
	if !exists(filepath.Join(mediaRoot, "a/b/dead-1.jpg")) {
		t.Fatal("file was not restored to its original location")
	}
	if exists(filepath.Join(quarantine, "a/b/dead-1.jpg")) {
		t.Fatal("file should no longer be in quarantine")
	}
	// A fully restored quarantine clears its manifest.
	if exists(filepath.Join(quarantine, ManifestName)) {
		t.Fatal("manifest should be removed once everything is restored")
	}
}

func TestQuarantineDryRunMovesNothing(t *testing.T) {
	mediaRoot := t.TempDir()
	quarantine := t.TempDir()
	writeFile(t, mediaRoot, "a/b/dead.jpg", 10)

	res, err := Quarantine(context.Background(), QuarantineOptions{
		MediaRoot:     mediaRoot,
		QuarantineDir: quarantine,
		Orphans:       []media.File{{RelPath: "a/b/dead.jpg", Size: 10}},
		DryRun:        true,
	})
	if err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	if res.Moved != 0 || res.Bytes != 10 {
		t.Fatalf("unexpected dry-run result: %+v", res)
	}
	if !exists(filepath.Join(mediaRoot, "a/b/dead.jpg")) {
		t.Fatal("dry run must not move files")
	}
}

func TestQuarantineRejectsPathTraversal(t *testing.T) {
	mediaRoot := t.TempDir()
	quarantine := t.TempDir()

	res, err := Quarantine(context.Background(), QuarantineOptions{
		MediaRoot:     mediaRoot,
		QuarantineDir: quarantine,
		Orphans:       []media.File{{RelPath: "../../etc/passwd", Size: 1}},
	})
	if err != nil {
		t.Fatalf("Quarantine should not fail hard on a bad path: %v", err)
	}
	if res.Failed != 1 || res.Moved != 0 {
		t.Fatalf("expected the malicious path to be rejected: %+v", res)
	}
}

func TestQuarantineEmptyListIsNoop(t *testing.T) {
	res, err := Quarantine(context.Background(), QuarantineOptions{
		MediaRoot:     t.TempDir(),
		QuarantineDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	if res.Moved != 0 || res.Requested != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestRestoreSkipsExistingFiles(t *testing.T) {
	mediaRoot := t.TempDir()
	quarantine := t.TempDir()

	writeFile(t, mediaRoot, "a/b/file.jpg", 10)
	writeFile(t, quarantine, "a/b/file.jpg", 99)

	// Hand-write a manifest pointing at the quarantined copy.
	manifest := filepath.Join(quarantine, ManifestName)
	if err := os.WriteFile(manifest, []byte(`{"path":"a/b/file.jpg","size":99}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Restore(context.Background(), RestoreOptions{
		QuarantineDir: quarantine,
		MediaRoot:     mediaRoot,
		Parallel:      1,
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.Skipped != 1 || res.Restored != 0 {
		t.Fatalf("expected the existing file to be skipped: %+v", res)
	}

	// The original must not have been overwritten.
	info, err := os.Stat(filepath.Join(mediaRoot, "a/b/file.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 10 {
		t.Fatalf("existing file was overwritten (size %d)", info.Size())
	}
	// A skipped entry stays in the manifest so it can be retried.
	if !exists(manifest) {
		t.Fatal("manifest should be kept when entries were skipped")
	}
}

func TestPurgeRefusesDirectoryWithoutManifest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "important/notes.txt", 10)

	if _, err := Purge(context.Background(), PurgeOptions{QuarantineDir: dir}); err == nil {
		t.Fatal("expected purge to refuse a directory it did not create")
	}
	if !exists(filepath.Join(dir, "important", "notes.txt")) {
		t.Fatal("the refused directory must be left untouched")
	}

	// --force overrides the guard.
	if _, err := Purge(context.Background(), PurgeOptions{QuarantineDir: dir, Force: true}); err != nil {
		t.Fatalf("Purge with force: %v", err)
	}
	if exists(dir) {
		t.Fatal("directory should be gone after a forced purge")
	}
}

func TestPurgeWithManifestSucceeds(t *testing.T) {
	mediaRoot := t.TempDir()
	quarantine := t.TempDir()
	writeFile(t, mediaRoot, "a/b/dead.jpg", 10)

	if _, err := Quarantine(context.Background(), QuarantineOptions{
		MediaRoot:     mediaRoot,
		QuarantineDir: quarantine,
		Orphans:       []media.File{{RelPath: "a/b/dead.jpg", Size: 10}},
	}); err != nil {
		t.Fatalf("Quarantine: %v", err)
	}

	res, err := Purge(context.Background(), PurgeOptions{QuarantineDir: quarantine})
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if res.Removed == 0 {
		t.Fatal("expected files to be reported for removal")
	}
	if exists(quarantine) {
		t.Fatal("quarantine directory should be gone")
	}
}

func TestPurgeDryRun(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "data/x.jpg", 10)

	res, err := Purge(context.Background(), PurgeOptions{QuarantineDir: dir, DryRun: true, Force: true})
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if res.Removed != 1 {
		t.Fatalf("Removed = %d, want 1", res.Removed)
	}
	if !exists(dir) {
		t.Fatal("dry run must not delete anything")
	}
}

func TestEnsureMediaPathExists(t *testing.T) {
	if err := EnsureMediaPathExists(t.TempDir()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := EnsureMediaPathExists(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected an error for a missing path")
	}
}

func TestSafeRelJoinRejectsEscapes(t *testing.T) {
	base := t.TempDir()
	if _, err := safeRelJoin(base, "a/b/c.jpg"); err != nil {
		t.Fatalf("normal path rejected: %v", err)
	}
	for _, bad := range []string{"", "/abs/path.jpg", "../sibling.jpg", "a/../../etc/passwd"} {
		if _, err := safeRelJoin(base, bad); err == nil {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
}

// The same-filesystem guard is what stops a cross-volume quarantine from
// silently turning an instant rename into a full copy that doubles disk usage.
// It is implemented per platform (st_dev on Unix, volume name elsewhere), so
// this asserts the behavior rather than the mechanism.
func TestDeviceOfIsStableForOneDirectory(t *testing.T) {
	dir := t.TempDir()

	first, err := deviceOf(dir)
	if err != nil {
		t.Fatalf("deviceOf(%s): %v", dir, err)
	}
	second, err := deviceOf(dir)
	if err != nil {
		t.Fatalf("deviceOf(%s) (second call): %v", dir, err)
	}
	if first != second {
		t.Errorf("deviceOf is not stable for one directory: %d then %d", first, second)
	}

	if err := verifySameFilesystem(dir, dir); err != nil {
		t.Errorf("a directory must be on the same filesystem as itself: %v", err)
	}
}

// Two subdirectories of one temporary directory are necessarily on the same
// filesystem, which is the situation a normal quarantine lives in.
func TestVerifySameFilesystemAcceptsSiblings(t *testing.T) {
	parent := t.TempDir()
	media := filepath.Join(parent, "media")
	quarantine := filepath.Join(parent, "quarantine")
	for _, d := range []string{media, quarantine} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := verifySameFilesystem(media, quarantine); err != nil {
		t.Errorf("sibling directories should pass the check: %v", err)
	}
}
