package analyzer

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/shuaiZend/mage-mediagc/internal/magento"
	"github.com/shuaiZend/mage-mediagc/internal/media"
)

func buildScan(files ...media.File) *media.ScanResult {
	var total int64
	for _, f := range files {
		total += f.Size
	}
	sorted := make([]media.File, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].RelPath < sorted[j].RelPath })
	return &media.ScanResult{Root: "/media", Files: sorted, Bytes: total}
}

func refSet(paths ...string) *magento.RefSet {
	rs := magento.NewRefSet()
	for _, p := range paths {
		rs.Add(p)
	}
	return rs
}

func TestAnalyzeClassifiesFiles(t *testing.T) {
	scan := buildScan(
		media.File{RelPath: "a/a/live-1.jpg", Size: 100},
		media.File{RelPath: "a/b/orphan-1.jpg", Size: 200},
		media.File{RelPath: "h/-/live-2.jpg", Size: 300},
		media.File{RelPath: "h/-/orphan-2.jpg", Size: 400},
	)
	refs := refSet("/a/a/live-1.jpg", "/h/-/live-2.jpg")

	res, err := Analyze(context.Background(), scan, refs, DefaultOptions())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	if res.LiveFiles != 2 {
		t.Errorf("LiveFiles = %d, want 2", res.LiveFiles)
	}
	if res.LiveBytes != 400 {
		t.Errorf("LiveBytes = %d, want 400", res.LiveBytes)
	}
	if res.OrphanFiles != 2 {
		t.Errorf("OrphanFiles = %d, want 2", res.OrphanFiles)
	}
	if res.OrphanBytes != 600 {
		t.Errorf("OrphanBytes = %d, want 600", res.OrphanBytes)
	}
	if len(res.Orphans) != 2 {
		t.Fatalf("len(Orphans) = %d, want 2", len(res.Orphans))
	}
	if res.Orphans[0].RelPath != "a/b/orphan-1.jpg" || res.Orphans[1].RelPath != "h/-/orphan-2.jpg" {
		t.Errorf("unexpected orphan set: %+v", res.Orphans)
	}
	if res.DiskFiles != 4 || res.DiskBytes != 1000 {
		t.Errorf("disk totals wrong: files=%d bytes=%d", res.DiskFiles, res.DiskBytes)
	}
}

func TestAnalyzePerDirectoryBuckets(t *testing.T) {
	scan := buildScan(
		media.File{RelPath: "h/-/live.jpg", Size: 10},
		media.File{RelPath: "h/-/dead-1.jpg", Size: 10},
		media.File{RelPath: "h/-/dead-2.jpg", Size: 10},
		media.File{RelPath: "s/-/dead-3.jpg", Size: 10},
	)
	refs := refSet("/h/-/live.jpg")

	res, err := Analyze(context.Background(), scan, refs, DefaultOptions())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Dirs) != 2 {
		t.Fatalf("expected 2 buckets, got %d (%+v)", len(res.Dirs), res.Dirs)
	}
	// Sorted by orphan count descending, so h/- (2 orphans) comes first.
	if res.Dirs[0].Dir != "h/-" || res.Dirs[0].Orphans != 2 {
		t.Errorf("first bucket = %+v, want h/- with 2 orphans", res.Dirs[0])
	}
	if res.Dirs[1].Dir != "s/-" || res.Dirs[1].Orphans != 1 {
		t.Errorf("second bucket = %+v, want s/- with 1 orphan", res.Dirs[1])
	}

	top := res.TopOrphanDirs(1)
	if len(top) != 1 || top[0].Dir != "h/-" {
		t.Errorf("TopOrphanDirs(1) = %+v", top)
	}
}

func TestAnalyzeMissingFilesOnlyInsideScannedTree(t *testing.T) {
	scan := buildScan(
		media.File{RelPath: "a/a/present.jpg", Size: 10},
		media.File{RelPath: "h/-/present2.jpg", Size: 10},
	)
	refs := refSet(
		"/a/a/present.jpg",
		"/a/a/gone.jpg",            // head "a" exists on disk -> in scope
		"/wysiwyg/home/banner.jpg", // no "wysiwyg" bucket -> out of scope
		"/ves/theme/logo.png",      // out of scope
	)

	res, err := Analyze(context.Background(), scan, refs, DefaultOptions())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Missing) != 1 || res.Missing[0] != "a/a/gone.jpg" {
		t.Fatalf("Missing = %v, want [a/a/gone.jpg]", res.Missing)
	}
	if len(res.Warnings) == 0 {
		t.Error("expected a warning about missing files")
	}
}

func TestAnalyzeCaseInsensitiveFallback(t *testing.T) {
	scan := buildScan(media.File{RelPath: "a/a/Photo.JPG", Size: 10})
	refs := refSet("/a/a/photo.jpg")

	res, err := Analyze(context.Background(), scan, refs, DefaultOptions())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.OrphanFiles != 0 {
		t.Fatalf("case-only difference must not produce an orphan, got %d", res.OrphanFiles)
	}

	// With the fallback disabled the file is reported as an orphan, which
	// documents why the fallback is on by default.
	opts := DefaultOptions()
	opts.CaseInsensitiveFallback = false
	res2, err := Analyze(context.Background(), scan, refs, opts)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res2.OrphanFiles != 1 {
		t.Fatalf("without the fallback expected 1 orphan, got %d", res2.OrphanFiles)
	}
}

func TestAnalyzeWarnsWhenNoReferencesCollected(t *testing.T) {
	scan := buildScan(media.File{RelPath: "a/a/x.jpg", Size: 10})
	res, err := Analyze(context.Background(), scan, magento.NewRefSet(), DefaultOptions())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected a loud warning when the database yielded no references")
	}
	found := false
	for _, w := range res.Warnings {
		if len(w) > 0 && (contains(w, "no media references") || contains(w, "refusing to call anything an orphan")) {
			found = true
		}
	}
	if !found {
		t.Fatalf("warning text not found in %v", res.Warnings)
	}
}

func TestAnalyzeWarnsWhenOrphanRatioIsImplausible(t *testing.T) {
	var files []media.File
	for i := 0; i < 100; i++ {
		files = append(files, media.File{RelPath: "a/a/f" + string(rune('a'+i%26)) + ".jpg", Size: 1})
	}
	scan := buildScan(files...)
	refs := refSet("/a/a/only-one.jpg")

	res, err := Analyze(context.Background(), scan, refs, DefaultOptions())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	found := false
	for _, w := range res.Warnings {
		if contains(w, "look orphaned") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a threshold warning, got %v", res.Warnings)
	}
}

func TestAnalyzeEmptyTree(t *testing.T) {
	res, err := Analyze(context.Background(), buildScan(), refSet("/a/a/x.jpg"), DefaultOptions())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.DiskFiles != 0 || res.OrphanFiles != 0 {
		t.Fatalf("expected empty result, got %+v", res)
	}
	if len(res.Warnings) == 0 {
		t.Error("expected a warning about an empty media root")
	}
}

func TestOrphanSet(t *testing.T) {
	scan := buildScan(
		media.File{RelPath: "a/a/live.jpg", Size: 1},
		media.File{RelPath: "a/a/dead.jpg", Size: 1},
	)
	res, err := Analyze(context.Background(), scan, refSet("/a/a/live.jpg"), DefaultOptions())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	set := res.OrphanSet()
	if len(set) != 1 {
		t.Fatalf("OrphanSet size = %d, want 1", len(set))
	}
	if _, ok := set["a/a/dead.jpg"]; !ok {
		t.Fatalf("expected a/a/dead.jpg in the orphan set: %v", set)
	}
	if _, ok := set["a/a/live.jpg"]; ok {
		t.Fatal("live file must not appear in the orphan set")
	}
}

func TestBucketKeyOf(t *testing.T) {
	cases := map[string]string{
		"h/-/x.jpg":    "h/-",
		"a/b/c/d.jpg":  "a/b",
		"top.jpg":      "(root)",
		"single/x.jpg": "single/x",
	}
	for in, want := range cases {
		if got := BucketKeyOf(in); got != want {
			t.Errorf("BucketKeyOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// Magento's placeholder images are referenced only from core_config_data, which
// is a configuration table rather than a media table, so reference collection
// cannot see them. Without an explicit guard a stock installation would have its
// placeholders moved out of the media tree as orphans.
func TestAnalyzeProtectsPlaceholderDirectory(t *testing.T) {
	scan := buildScan(
		media.File{RelPath: "placeholder/default/placeholder.jpg", Size: 100},
		media.File{RelPath: "placeholder/.htaccess", Size: 1},
		media.File{RelPath: "a/b/real-orphan.jpg", Size: 200},
	)
	// The database contributes no reference at all for these paths.
	refs := refSet("/a/b/something-else.jpg")

	res, err := Analyze(context.Background(), scan, refs, DefaultOptions())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.LiveFiles != 2 {
		t.Errorf("LiveFiles = %d, want 2 (both placeholder entries)", res.LiveFiles)
	}
	if res.OrphanFiles != 1 {
		t.Fatalf("OrphanFiles = %d, want 1", res.OrphanFiles)
	}
	if res.Orphans[0].RelPath != "a/b/real-orphan.jpg" {
		t.Errorf("the wrong file was called an orphan: %q", res.Orphans[0].RelPath)
	}
	for _, f := range res.Orphans {
		if strings.HasPrefix(f.RelPath, placeholderDir) {
			t.Errorf("placeholder file %q must never be an orphan", f.RelPath)
		}
	}
}

// Only the placeholder directory itself is protected: a directory that merely
// starts with the same letters must still be cleaned.
func TestAnalyzeDoesNotOverProtectPlaceholderLookalikes(t *testing.T) {
	scan := buildScan(
		media.File{RelPath: "placeholder-cache/a.jpg", Size: 10},
		media.File{RelPath: "placeholders/b.jpg", Size: 10},
	)
	res, err := Analyze(context.Background(), scan, refSet("/nothing.jpg"), DefaultOptions())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.OrphanFiles != 2 {
		t.Errorf("OrphanFiles = %d, want 2: lookalike directories are not protected", res.OrphanFiles)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
