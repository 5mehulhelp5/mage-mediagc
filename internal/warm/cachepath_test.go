package warm

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shuaiZend/mage-mediagc/internal/media"
)

const (
	hashA = "0123456789abcdef0123456789abcdef"
	hashB = "fedcba9876543210fedcba9876543210"
)

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := ParseBaseURL(raw)
	if err != nil {
		t.Fatalf("ParseBaseURL(%q): %v", raw, err)
	}
	return u
}

func TestValidHash(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{hashA, true},
		{"00000000000000000000000000000000", true},
		{"ffffffffffffffffffffffffffffffff", true},
		{strings.ToUpper(hashA), false},
		{hashA[:31], false},
		{hashA + "0", false},
		{"0123456789abcdef0123456789abcdeg", false},
		{"", false},
		{"cache", false},
		{"0123456789abcdef0123456789abcde-", false},
	}
	for _, c := range cases {
		if got := ValidHash(c.in); got != c.want {
			t.Errorf("ValidHash(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestDiscoverHashes(t *testing.T) {
	dir := t.TempDir()

	got, err := DiscoverHashes(dir)
	if err != nil {
		t.Fatalf("missing entries: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty dir: got %v, want none", got)
	}

	for _, name := range []string{hashB, hashA, "not-a-hash", strings.ToUpper(hashA), "cache"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A regular file with a hash-shaped name is not a size set.
	if err := os.WriteFile(filepath.Join(dir, "11111111111111111111111111111111"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err = DiscoverHashes(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{hashA, hashB}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v (sorted, files and junk excluded)", got, want)
	}

	if _, err := DiscoverHashes(filepath.Join(dir, "absent")); err != nil {
		t.Fatalf("absent dir must not be an error: %v", err)
	}
}

func TestCachePath(t *testing.T) {
	got := CachePath("/srv/shop/pub/media/catalog/product", hashA, "a/b/c.jpg")
	want := filepath.Join("/srv/shop/pub/media/catalog/product", "cache", hashA, "a", "b", "c.jpg")
	if got != want {
		t.Fatalf("CachePath = %q, want %q", got, want)
	}
}

func TestCacheURL(t *testing.T) {
	base := mustParseURL(t, "https://shop.example.com/")

	if got, want := CacheURL(base, hashA, "a/b/c.jpg"),
		"https://shop.example.com/media/catalog/product/cache/"+hashA+"/a/b/c.jpg"; got != want {
		t.Fatalf("CacheURL = %q, want %q", got, want)
	}

	// A storefront served from a subdirectory keeps its prefix.
	sub := mustParseURL(t, "https://shop.example.com/store")
	if got, want := CacheURL(sub, hashA, "c.jpg"),
		"https://shop.example.com/store/media/catalog/product/cache/"+hashA+"/c.jpg"; got != want {
		t.Fatalf("subdirectory base: got %q, want %q", got, want)
	}

	// Catalog file names hold spaces and non-ASCII characters routinely, and
	// the escaped form has to survive a round trip.
	for _, rel := range []string{"中 文/图 片.jpg", "a b/c d.png", "50%/x.jpg"} {
		raw := CacheURL(base, hashA, rel)
		nonASCII := false
		for _, r := range raw {
			if r > 127 {
				nonASCII = true
				break
			}
		}
		if strings.ContainsAny(raw, " \t") || nonASCII {
			t.Errorf("CacheURL(%q) = %q is not escaped", rel, raw)
		}
		back, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("CacheURL(%q) = %q does not parse back: %v", rel, raw, err)
		}
		want := "/media/catalog/product/cache/" + hashA + "/" + rel
		if back.Path != want {
			t.Errorf("CacheURL(%q) round trip gave %q, want %q", rel, back.Path, want)
		}
	}
}

func TestParseBaseURL(t *testing.T) {
	for _, raw := range []string{"https://shop.example.com", "https://shop.example.com/", "http://127.0.0.1:8080", "https://shop.example.com/store/"} {
		if _, err := ParseBaseURL(raw); err != nil {
			t.Errorf("ParseBaseURL(%q): unexpected error %v", raw, err)
		}
	}
	for _, raw := range []string{"", "   ", "shop.example.com", "ftp://shop.example.com", "https://", "https://shop.example.com/x?y=1", "https://shop.example.com/x#f"} {
		if _, err := ParseBaseURL(raw); err == nil {
			t.Errorf("ParseBaseURL(%q): expected an error", raw)
		}
	}

	u, err := ParseBaseURL("https://shop.example.com/store/")
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/store" {
		t.Errorf("trailing slash not trimmed: Path = %q", u.Path)
	}
}

func TestHashFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "hashes.txt")
	if err := WriteHashFile(path, []string{hashB, hashA}); err != nil {
		t.Fatalf("WriteHashFile: %v", err)
	}

	got, err := ReadHashFile(path)
	if err != nil {
		t.Fatalf("ReadHashFile: %v", err)
	}
	if len(got) != 2 || got[0] != hashA || got[1] != hashB {
		t.Fatalf("round trip = %v, want [%s %s] sorted", got, hashA, hashB)
	}
}

func TestReadHashFileToleratesEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hashes.txt")
	body := "# a comment\n\n" + hashA + "\n   \n# another\n" + hashA + "\n" + hashB + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadHashFile(path)
	if err != nil {
		t.Fatalf("ReadHashFile: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want duplicates and comments collapsed to 2 entries", got)
	}
}

func TestReadHashFileRejectsJunk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hashes.txt")
	if err := os.WriteFile(path, []byte(hashA+"\nnope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadHashFile(path); err == nil {
		t.Fatal("expected an error for a line that is not a cache hash")
	}
}

func TestWarmableFile(t *testing.T) {
	cases := []struct {
		rel  string
		want bool
	}{
		{"a/b/c.jpg", true},
		{"a/b/c.JPG", true},
		{"a/b/c.jpeg", true},
		{"a/b/c.png", true},
		{"a/b/c.gif", true},
		{"a/b/c.webp", false},
		{"a/b/c.txt", false},
		{"a/b/noext", false},
		{".htaccess", false},
		{"", false},
		{"cache/" + hashA + "/a/b/c.jpg", false},
		{"placeholder/placeholder.jpg", false},
		{"placeholder", false},
		// Magento keeps the last three segments of the request path to find
		// the original, so anything that is not exactly two directories and a
		// file name would be requested against a different image.
		{"a/b/c/d.jpg", false},
		{"a/c.jpg", false},
		{"c.jpg", false},
		{"placeholderish/c/d.jpg", true},
	}
	for _, c := range cases {
		if got := warmableFile(c.rel); got != c.want {
			t.Errorf("warmableFile(%q) = %v, want %v", c.rel, got, c.want)
		}
	}
}

// The count a caller prints has to multiply out against the plan, so it must
// apply exactly the same predicate the plan does.
func TestWarmableFilesCountsWhatThePlanUses(t *testing.T) {
	files := []media.File{
		{RelPath: "a/b/one.jpg"},
		{RelPath: "a/b/two.png"},
		{RelPath: "a/b/notes.txt"},
		{RelPath: "placeholder/logo.png"},
		{RelPath: "cache/" + hashA + "/a/b/one.jpg"},
		{RelPath: "a/b/three.gif"},
		{RelPath: "a/one.jpg"},
	}
	if got := WarmableFiles(files); got != 3 {
		t.Fatalf("WarmableFiles = %d, want 3 (the jpg, the png and the gif at a valid depth)", got)
	}
	if got := WarmableFiles(nil); got != 0 {
		t.Fatalf("WarmableFiles(nil) = %d, want 0", got)
	}
}

func TestParseMethod(t *testing.T) {
	for _, raw := range []string{"get", "GET", " get ", ""} {
		got, err := ParseMethod(raw)
		if err != nil {
			t.Errorf("ParseMethod(%q): %v", raw, err)
			continue
		}
		if got != MethodGet {
			t.Errorf("ParseMethod(%q) = %q, want %q", raw, got, MethodGet)
		}
	}
	if got, err := ParseMethod("head"); err != nil || got != MethodHead {
		t.Errorf("ParseMethod(head) = %q, %v", got, err)
	}
	if _, err := ParseMethod("post"); err == nil {
		t.Error("ParseMethod(post) should fail")
	}
}

// SeedHash has to be something Magento accepts as a path component. It is
// never validated — the shop resizes whatever it is handed — but a value of the
// wrong shape would at least look like a mistake to whoever inspects the logs.
func TestSeedHashIsAWellFormedHash(t *testing.T) {
	if !ValidHash(SeedHash) {
		t.Fatalf("SeedHash = %q is not a valid size-set name", SeedHash)
	}
}

// A size set is live only if it holds the variant, which is the same test the
// plan applies to each item. Directory age says nothing: Magento writes into
// the sets it wants and never removes the ones it has stopped wanting, so the
// most recently written directory is often a stale one.
func TestLiveHashFindsTheSetHoldingTheVariant(t *testing.T) {
	root := t.TempDir()
	// A stale set, written most recently, and the live one.
	seedCache(t, root, hashB, "a/b/c.jpg")
	if err := os.Chtimes(filepath.Join(CacheDir(root), hashB), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}

	got, err := LiveHash(CacheDir(root), "a/b/c.jpg")
	if err != nil {
		t.Fatalf("LiveHash: %v", err)
	}
	if got != hashB {
		t.Fatalf("LiveHash = %q, want %q", got, hashB)
	}
}

func TestLiveHashIgnoresSetsWithoutTheVariant(t *testing.T) {
	root := t.TempDir()
	seedCache(t, root, hashA, "a/b/other.jpg")
	seedCache(t, root, hashB, "a/b/c.jpg")

	got, err := LiveHash(CacheDir(root), "a/b/c.jpg")
	if err != nil {
		t.Fatalf("LiveHash: %v", err)
	}
	if got != hashB {
		t.Fatalf("LiveHash = %q, want the set that holds the file, %q", got, hashB)
	}
}

func TestLiveHashReportsWhenNoSetHoldsIt(t *testing.T) {
	root := t.TempDir()
	seedCache(t, root, hashA, "a/b/other.jpg")
	if _, err := LiveHash(CacheDir(root), "a/b/c.jpg"); !errors.Is(err, ErrNoLiveHash) {
		t.Fatalf("err = %v, want ErrNoLiveHash", err)
	}
	// An empty or absent cache tree is the same situation, not a crash.
	if _, err := LiveHash(CacheDir(t.TempDir()), "a/b/c.jpg"); !errors.Is(err, ErrNoLiveHash) {
		t.Fatalf("err = %v, want ErrNoLiveHash for a missing cache dir", err)
	}
}

// CachePath and VariantPath describe the same file, one from a media root and
// one from a cache directory. If they ever disagreed, LiveHash would confirm a
// set the plan then fails to find.
func TestCachePathAndVariantPathAgree(t *testing.T) {
	root := t.TempDir()
	for _, h := range []string{hashA, hashB} {
		for _, rel := range []string{"a/b/c.jpg", "x/y/z name.png", "d/e/f.gif"} {
			if CachePath(root, h, rel) != VariantPath(CacheDir(root), h, rel) {
				t.Errorf("CachePath and VariantPath disagree for %s under %s:\n  %s\n  %s",
					rel, h, CachePath(root, h, rel), VariantPath(CacheDir(root), h, rel))
			}
		}
	}
}
