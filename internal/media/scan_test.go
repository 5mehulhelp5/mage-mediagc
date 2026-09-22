package media

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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

func TestScanSeparatesCacheFromOriginals(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a/b/one.jpg", 10)
	writeFile(t, root, "a/b/two.jpg", 20)
	writeFile(t, root, "h/-/three.jpg", 30)
	writeFile(t, root, "top.jpg", 5)
	writeFile(t, root, "cache/abc123/4/f/four.jpg", 40)
	writeFile(t, root, "cache/abc123/q/a/five.jpg", 50)

	res, err := Scan(context.Background(), root, ScanOptions{Workers: 2})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if len(res.Files) != 4 {
		t.Fatalf("Files = %d, want 4 (cache excluded): %+v", len(res.Files), res.Files)
	}
	if res.Bytes != 65 {
		t.Errorf("Bytes = %d, want 65", res.Bytes)
	}
	if res.CacheFiles != 2 {
		t.Errorf("CacheFiles = %d, want 2", res.CacheFiles)
	}
	if res.CacheBytes != 90 {
		t.Errorf("CacheBytes = %d, want 90", res.CacheBytes)
	}

	// Results must be sorted so the analyzer can binary-search them.
	for i := 1; i < len(res.Files); i++ {
		if res.Files[i-1].RelPath >= res.Files[i].RelPath {
			t.Fatalf("Files is not sorted: %q then %q", res.Files[i-1].RelPath, res.Files[i].RelPath)
		}
	}

	// Paths use forward slashes regardless of platform.
	for _, f := range res.Files {
		if filepath.Separator != '/' && containsByte(f.RelPath, '\\') {
			t.Fatalf("RelPath %q should use forward slashes", f.RelPath)
		}
	}
}

func TestScanIncludesCacheWhenAsked(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a/b/one.jpg", 10)
	writeFile(t, root, "cache/abc/x/y/variant.jpg", 20)

	res, err := Scan(context.Background(), root, ScanOptions{Workers: 1, IncludeCache: true})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.Files) != 2 {
		t.Fatalf("Files = %d, want 2", len(res.Files))
	}
	if res.CacheFiles != 0 {
		t.Errorf("CacheFiles = %d, want 0 when the cache is indexed normally", res.CacheFiles)
	}
}

func TestScanExcludesNamedEntries(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a/b/one.jpg", 10)
	writeFile(t, root, "tmp/staging.jpg", 10)

	res, err := Scan(context.Background(), root, ScanOptions{Workers: 1, ExcludeNames: []string{"tmp"}})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.Files) != 1 {
		t.Fatalf("Files = %d, want 1", len(res.Files))
	}
	if len(res.SkippedDirs) != 1 || res.SkippedDirs[0] != "tmp" {
		t.Fatalf("SkippedDirs = %v", res.SkippedDirs)
	}
}

func TestScanExcludesGlobs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a/b/keep.jpg", 10)
	writeFile(t, root, "a/b/notes.tmp", 20)
	writeFile(t, root, "staging/pending.jpg", 30)

	res, err := Scan(context.Background(), root, ScanOptions{
		Workers:      1,
		ExcludeGlobs: []string{"staging", "*.tmp"},
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.Files) != 1 || res.Files[0].RelPath != "a/b/keep.jpg" {
		t.Fatalf("Files = %+v, want just a/b/keep.jpg", res.Files)
	}
	if res.Bytes != 10 {
		t.Errorf("Bytes = %d, want 10 (excluded files must not be counted)", res.Bytes)
	}
	want := []string{"a/b/notes.tmp", "staging"}
	if len(res.SkippedDirs) != len(want) ||
		res.SkippedDirs[0] != want[0] || res.SkippedDirs[1] != want[1] {
		t.Fatalf("SkippedDirs = %v, want %v", res.SkippedDirs, want)
	}
}

func TestScanInvalidGlobExcludesNothing(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a/b/keep.jpg", 10)

	// A malformed pattern must not be able to wipe out the whole tree.
	res, err := Scan(context.Background(), root, ScanOptions{
		Workers:      1,
		ExcludeGlobs: []string{"[unclosed", "a/**["},
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.Files) != 1 {
		t.Fatalf("Files = %+v, want the single file to survive", res.Files)
	}
	if len(res.SkippedDirs) != 0 {
		t.Fatalf("SkippedDirs = %v, want none", res.SkippedDirs)
	}
}

func TestScanEmptyDirectory(t *testing.T) {
	res, err := Scan(context.Background(), t.TempDir(), ScanOptions{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.Files) != 0 || res.CacheFiles != 0 {
		t.Fatalf("expected an empty result, got %+v", res)
	}
}

func TestScanMissingDirectory(t *testing.T) {
	if _, err := Scan(context.Background(), filepath.Join(t.TempDir(), "nope"), ScanOptions{}); err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}

func TestScanHonorsCanceledContext(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 50; i++ {
		writeFile(t, root, filepath.Join("a", "b", string(rune('a'+i%26))+".jpg"), 1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Scan(ctx, root, ScanOptions{Workers: 2}); err == nil {
		t.Fatal("expected a context error")
	}
}

func TestFindUsesSortedOrder(t *testing.T) {
	files := []File{
		{RelPath: "a/a/1.jpg"},
		{RelPath: "h/-/2.jpg"},
		{RelPath: "s/-/3.jpg"},
	}
	if f, ok := Find(files, "h/-/2.jpg"); !ok || f.RelPath != "h/-/2.jpg" {
		t.Fatalf("Find missed an existing entry: %+v %v", f, ok)
	}
	if _, ok := Find(files, "h/-/nope.jpg"); ok {
		t.Fatal("Find returned a hit for a missing entry")
	}
	if _, ok := Find(nil, "a/a/1.jpg"); ok {
		t.Fatal("Find on an empty slice should miss")
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:          "0 B",
		512:        "512 B",
		1024:       "1.00 KB",
		1536:       "1.50 KB",
		1048576:    "1.00 MB",
		1073741824: "1.00 GB",
	}
	for in, want := range cases {
		if got := HumanBytes(in); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestIsCachePath(t *testing.T) {
	yes := []string{"cache", "cache/abc/1/2/x.jpg"}
	no := []string{"a/b/x.jpg", "cached/x.jpg", "h/-/cache.jpg"}
	for _, p := range yes {
		if !IsCachePath(p) {
			t.Errorf("IsCachePath(%q) = false, want true", p)
		}
	}
	for _, p := range no {
		if IsCachePath(p) {
			t.Errorf("IsCachePath(%q) = true, want false", p)
		}
	}
}

func containsByte(s string, b byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return true
		}
	}
	return false
}
