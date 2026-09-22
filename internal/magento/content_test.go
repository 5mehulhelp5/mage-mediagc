package magento

import (
	"reflect"
	"sort"
	"testing"
)

func TestNormalizeMediaPath(t *testing.T) {
	cases := map[string]string{
		"/h/-/h-abc-b1.jpg":                                 "h/-/h-abc-b1.jpg",
		"h/-/h-abc-b1.jpg":                                  "h/-/h-abc-b1.jpg",
		"/catalog/product/h/-/h-abc-b1.jpg":                 "h/-/h-abc-b1.jpg",
		"catalog/product/h/-/h-abc-b1.jpg":                  "h/-/h-abc-b1.jpg",
		"/media/catalog/product/h/-/x.jpg":                  "h/-/x.jpg",
		"https://shop.test/media/catalog/product/h/-/x.jpg": "h/-/x.jpg",
		"/media/wysiwyg/home/banner.jpg":                    "wysiwyg/home/banner.jpg",
		"https://shop.test/media/wysiwyg/a.png?v=2":         "wysiwyg/a.png",
		"  /h/-/padded.jpg  ":                               "h/-/padded.jpg",
	}
	for in, want := range cases {
		if got := NormalizeMediaPath(in); got != want {
			t.Errorf("NormalizeMediaPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCandidatePathsIncludesDecodedVariant(t *testing.T) {
	got := CandidatePaths("/h/-/my%20image.jpg")
	sort.Strings(got)

	if !contains(got, "h/-/my%20image.jpg") {
		t.Fatalf("expected the literal form to be a candidate, got %v", got)
	}
	if !contains(got, "h/-/my image.jpg") {
		t.Fatalf("expected the percent-decoded form to be a candidate, got %v", got)
	}
}

func TestCandidatePathsDeduplicates(t *testing.T) {
	got := CandidatePaths("/h/-/plain.jpg")
	seen := map[string]int{}
	for _, c := range got {
		seen[c]++
	}
	for c, n := range seen {
		if n > 1 {
			t.Fatalf("candidate %q produced %d times", c, n)
		}
	}
}

func TestExtractMediaPathsDirectives(t *testing.T) {
	content := `<p>Look at this:</p>
<img src="{{media url="wysiwyg/home/hero.jpg"}}" alt="">
<img src="{{media url='wysiwyg/home/quoted.png'}}">
<p>{{media url=catalog/product/h/-/unquoted-b1.jpg}}</p>`
	got := ExtractMediaPaths(content)
	want := []string{
		"wysiwyg/home/hero.jpg",
		"wysiwyg/home/quoted.png",
		"catalog/product/h/-/unquoted-b1.jpg",
	}
	for _, w := range want {
		if !contains(got, w) {
			t.Errorf("missing %q in %v", w, got)
		}
	}
}

func TestExtractMediaPathsBareURLs(t *testing.T) {
	content := `Some copy with inline markup.
<img src="https://shop.example.com/media/catalog/product/a/c/ac-001.jpg" width="100">
<a href="https://shop.example.com/media/wysiwyg/docs/manual.pdf">manual</a>
<img src="https://shop.example.com/media/catalog/product/a/c/other.JPEG">
<style>body{background:url('/media/wysiwyg/bg/pattern.png')}</style>`
	got := ExtractMediaPaths(content)
	for _, want := range []string{
		"catalog/product/a/c/ac-001.jpg",
		"catalog/product/a/c/other.JPEG",
		"wysiwyg/bg/pattern.png",
	} {
		if !containsFold(got, want) {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	// A PDF link is not an image we would ever remove, but it must not be
	// mistaken for one either.
	for _, g := range got {
		if g == "wysiwyg/docs/manual.pdf" {
			t.Errorf("pdf should not be extracted as a media image: %v", got)
		}
	}
}

func TestExtractMediaPathsEmpty(t *testing.T) {
	if got := ExtractMediaPaths(""); got != nil {
		t.Fatalf("expected nil for empty content, got %v", got)
	}
	if got := ExtractMediaPaths("<p>no images here</p>"); len(got) != 0 {
		t.Fatalf("expected no paths, got %v", got)
	}
}

func TestExtractMediaPathsDeduplicates(t *testing.T) {
	content := `<img src="/media/catalog/product/a/c/x.jpg"><img src="/media/catalog/product/a/c/x.jpg">`
	got := ExtractMediaPaths(content)
	count := 0
	for _, g := range got {
		if g == "catalog/product/a/c/x.jpg" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected one occurrence after dedup, got %d (%v)", count, got)
	}
}

func TestRefSetAddAndContains(t *testing.T) {
	rs := NewRefSet()
	rs.Add("/h/-/a.jpg")
	rs.Add("/catalog/product/s/-/b.jpg")

	if !rs.Contains("h/-/a.jpg") {
		t.Fatal("expected h/-/a.jpg to be referenced")
	}
	if !rs.Contains("s/-/b.jpg") {
		t.Fatal("expected s/-/b.jpg to be referenced (prefix should be stripped)")
	}
	if rs.Contains("h/-/nope.jpg") {
		t.Fatal("unexpected reference match")
	}
	if rs.Len() == 0 {
		t.Fatal("expected a non-empty path set")
	}
}

func TestRefSetAddReturnsNewCounts(t *testing.T) {
	rs := NewRefSet()
	first := rs.Add("/h/-/dup.jpg")
	if first == 0 {
		t.Fatal("first insert should add at least one candidate")
	}
	second := rs.Add("/h/-/dup.jpg")
	if second != 0 {
		t.Fatalf("re-adding the same reference added %d candidates", second)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func containsFold(list []string, want string) bool {
	for _, s := range list {
		if equalFold(s, want) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func TestOrphanTargetsAreCoherent(t *testing.T) {
	targets := orphanTargets()
	if len(targets) == 0 {
		t.Fatal("expected orphan targets")
	}
	seen := make(map[string]bool, len(targets))
	for _, tg := range targets {
		if tg.Table == "" || tg.RefTable == "" || tg.RefCol == "" || tg.RefKey == "" {
			t.Fatalf("incomplete target: %+v", tg)
		}
		if len(tg.PkCols) == 0 {
			t.Fatalf("target %s has no primary key columns", tg.Table)
		}
		if seen[tg.Table] {
			t.Fatalf("duplicate target for table %s", tg.Table)
		}
		seen[tg.Table] = true
	}

	// Relationship tables must be cleaned before the tables they point at,
	// otherwise a later pass would still see stale references.
	index := make(map[string]int, len(targets))
	for i, tg := range targets {
		index[tg.Table] = i
	}
	for i, tg := range targets {
		if j, ok := index[tg.RefTable]; ok && j < i {
			t.Errorf("target %s (position %d) references %s which is cleaned earlier (position %d)",
				tg.Table, i, tg.RefTable, j)
		}
	}
}

func TestOrphanTargetsCoverCoreTables(t *testing.T) {
	var tables []string
	for _, t2 := range orphanTargets() {
		tables = append(tables, t2.Table)
	}
	for _, want := range []string{
		"catalog_product_entity_media_gallery",
		"catalog_product_entity_media_gallery_value_to_entity",
		"catalog_product_entity_varchar",
		"catalog_product_entity_int",
		"cataloginventory_stock_item",
	} {
		if !contains(tables, want) {
			t.Errorf("expected %s to be covered by orphan cleanup", want)
		}
	}
}

func TestPlaceholders(t *testing.T) {
	if got := placeholders(3); got != "?,?,?" {
		t.Fatalf("placeholders(3) = %q", got)
	}
	if got := placeholders(0); got != "" {
		t.Fatalf("placeholders(0) = %q", got)
	}
}

func TestQuoteIdent(t *testing.T) {
	if _, err := quoteIdent("catalog_product_entity"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, bad := range []string{"users; DROP TABLE x", "a`b", "a-b", ""} {
		if _, err := quoteIdent(bad); err == nil {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
}

func TestExtractMediaPathsOrderIsStable(t *testing.T) {
	content := `<img src="/media/wysiwyg/b.png"><img src="/media/wysiwyg/a.png">`
	first := ExtractMediaPaths(content)
	second := ExtractMediaPaths(content)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("extraction is not deterministic:\n%v\n%v", first, second)
	}
}
