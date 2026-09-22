package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shuaiZend/mage-mediagc/internal/analyzer"
	"github.com/shuaiZend/mage-mediagc/internal/config"
	"github.com/shuaiZend/mage-mediagc/internal/magento"
	"github.com/shuaiZend/mage-mediagc/internal/media"
)

func samplePayload() *Payload {
	res := &analyzer.Result{
		MediaRoot:   "/data/wwwroot/shop/pub/media/catalog/product",
		DiskFiles:   1000,
		DiskBytes:   10 << 30,
		CacheFiles:  300,
		CacheBytes:  3 << 30,
		RefPaths:    120,
		LiveFiles:   700,
		LiveBytes:   7 << 30,
		OrphanFiles: 300,
		OrphanBytes: 3 << 30,
		Missing:     []string{"a/b/gone.jpg", "c/d/also-gone.jpg"},
		RefStats: []magento.SourceStat{
			{Source: "media_gallery", Description: "gallery", RowsScanned: 700, PathsAdded: 700},
			{Source: "cms_content_cms_block", Description: "blocks", Skipped: "table not present"},
		},
		Dirs: []analyzer.DirStat{
			{Dir: "h/-", Total: 400, Orphans: 200, Bytes: 4 << 30, OrphanBytes: 2 << 30},
			{Dir: "s/-", Total: 200, Orphans: 100, Bytes: 2 << 30, OrphanBytes: 1 << 30},
		},
		Warnings: []string{"46.9% of files look orphaned (threshold 98%)"},
	}
	res.Orphans = []media.File{
		{RelPath: "h/-/x.jpg", Size: 10},
		{RelPath: "s/-/y.jpg", Size: 20},
	}

	db := &magento.OrphanReport{
		ProductCount: 42,
		TotalRows:    900,
		TotalOrphans: 300,
		Tables: []magento.OrphanStat{
			{Table: "catalog_product_entity_varchar", Total: 500, Orphans: 200, Percent: 40},
			{Table: "cataloginventory_stock_item", Skipped: "table not present"},
		},
	}

	return NewPayload(
		ToolInfo{Name: "mage-mediagc", Version: "v0.1.0", Commit: "abc1234"},
		TargetInfo{MediaRoot: res.MediaRoot, MagentoRoot: "/data/wwwroot/shop", Database: "shop_prod", MySQL: "8.0.46"},
		res, db,
	)
}

func render(t *testing.T, fn func(*bytes.Buffer) error) string {
	t.Helper()
	var buf bytes.Buffer
	if err := fn(&buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
}

func TestRenderTableLanguageSelection(t *testing.T) {
	p := samplePayload()

	en := render(t, func(b *bytes.Buffer) error {
		return RenderTable(b, p, Options{TopDirs: 10, Language: LangEnglish})
	})
	for _, want := range []string{"media root", "files on disk", "orphaned", "orphan hotspots", "warnings"} {
		if !strings.Contains(en, want) {
			t.Errorf("English table is missing %q:\n%s", want, en)
		}
	}

	zh := render(t, func(b *bytes.Buffer) error {
		return RenderTable(b, p, Options{TopDirs: 10, Language: LangChinese})
	})
	for _, want := range []string{"媒体目录", "磁盘原图", "孤儿碎片", "孤儿集中目录", "警告"} {
		if !strings.Contains(zh, want) {
			t.Errorf("Chinese table is missing %q:\n%s", want, zh)
		}
	}
	if strings.Contains(zh, "files on disk") {
		t.Error("Chinese table leaked English labels")
	}
}

// The verbose reference-source block is the one place the console builds a
// tabwriter table from a message format that already carries its own newline.
// Appending a second one emitted blank lines and broke column alignment, so
// every row was laid out independently.
func TestRenderTableReferenceSourcesAreAligned(t *testing.T) {
	out := render(t, func(b *bytes.Buffer) error {
		return RenderTable(b, samplePayload(), Options{TopDirs: 10, Verbose: 1, Language: LangEnglish})
	})

	_, after, ok := strings.Cut(out, "reference sources")
	if !ok {
		t.Fatalf("verbose table has no reference sources block:\n%s", out)
	}
	block := after
	if i := strings.Index(block, "orphan hotspots"); i >= 0 {
		block = block[:i]
	}

	lines := strings.Split(strings.Trim(block, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("reference sources block is empty")
	}
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			t.Errorf("reference sources line %d is blank; the row format must not "+
				"add a newline on top of its own", i+1)
		}
	}

	// The description is the informative part; a raw table name is not.
	if !strings.Contains(block, "gallery") {
		t.Errorf("reference sources dropped the human-readable description:\n%s", block)
	}
}

func TestRenderTableDefaultsToEnglish(t *testing.T) {
	en := render(t, func(b *bytes.Buffer) error {
		return RenderTable(b, samplePayload(), Options{TopDirs: 10})
	})
	if !strings.Contains(en, "files on disk") {
		t.Fatalf("the zero-value language should render English:\n%s", en)
	}
}

func TestRenderUnknownLanguageFallsBackToEnglish(t *testing.T) {
	out := render(t, func(b *bytes.Buffer) error {
		return RenderTable(b, samplePayload(), Options{TopDirs: 10, Language: "klingon"})
	})
	if !strings.Contains(out, "files on disk") {
		t.Fatalf("an unknown language should fall back to English:\n%s", out)
	}
}

// The Markdown report is what gets attached to a ticket, so the commands it
// recommends have to be commands that exist.
func TestRenderMarkdownRecommendsRealCommands(t *testing.T) {
	md := render(t, func(b *bytes.Buffer) error {
		return RenderMarkdown(b, samplePayload(), Options{TopDirs: 10, Language: LangEnglish})
	})

	if strings.Contains(md, "--dry-run") {
		t.Error("the report recommends --dry-run, but no command has that flag; " +
			"dry run is the default and --apply performs the change")
	}
	for _, cmd := range []string{
		"mage-mediagc cache clean --apply",
		"mage-mediagc quarantine --apply",
		"mage-mediagc restore --apply",
		"mage-mediagc purge --apply",
		"mage-mediagc db-clean",
		"mage-mediagc db-clean --apply",
		"mage-mediagc verify",
	} {
		if !strings.Contains(md, cmd) {
			t.Errorf("Markdown report does not mention %q", cmd)
		}
	}

	// The headline numbers and the per-source breakdown must be present.
	for _, want := range []string{
		"# mage-mediagc catalog media report",
		"## Summary",
		"| **orphaned** |",
		"## Reference sources",
		"## Orphan hotspots",
		"## Database orphans",
		"## Warnings",
		"## Missing files (first 50)",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown report is missing %q", want)
		}
	}
	if !strings.Contains(md, "cataloginventory_stock_item") {
		t.Error("a skipped table should still be listed, with its reason")
	}
}

// The console summary is fine with lowercase labels, but the Markdown document
// is attached to tickets. It must not inherit them: "## warnings" and
// "## orphan hotspots (2)" shipped once and looked like debug output.
func TestRenderMarkdownHeadingsAreTitleCased(t *testing.T) {
	md := render(t, func(b *bytes.Buffer) error {
		return RenderMarkdown(b, samplePayload(), Options{TopDirs: 10, Language: LangEnglish})
	})

	var headings []string
	insideCodeBlock := false
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "```") {
			insideCodeBlock = !insideCodeBlock
			continue
		}
		if insideCodeBlock || !strings.HasPrefix(line, "## ") {
			continue
		}
		headings = append(headings, strings.TrimPrefix(line, "## "))
	}
	if len(headings) == 0 {
		t.Fatal("no level-2 headings found in the Markdown report")
	}

	for _, h := range headings {
		// "Orphan hotspots (2)" -> "Orphan"
		word := strings.Fields(h)[0]
		if r := []rune(word)[0]; r < 'A' || r > 'Z' {
			t.Errorf("heading %q starts lowercase; Markdown headings should be title-cased", h)
		}
	}

	// The section headings reuse console labels elsewhere, so pin the three
	// that were wrong by name as well.
	for _, want := range []string{"## Orphan hotspots", "## Database orphans", "## Warnings"} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown report is missing headline %q", want)
		}
	}
}

func TestRenderMarkdownChinese(t *testing.T) {
	md := render(t, func(b *bytes.Buffer) error {
		return RenderMarkdown(b, samplePayload(), Options{TopDirs: 10, Language: LangChinese})
	})
	for _, want := range []string{"媒体碎片分析报告", "## 摘要", "## 引用来源明细", "## 警告", "mage-mediagc quarantine --apply"} {
		if !strings.Contains(md, want) {
			t.Errorf("Chinese Markdown report is missing %q", want)
		}
	}
}

func TestRenderMarkdownStepsAreNumbered(t *testing.T) {
	md := render(t, func(b *bytes.Buffer) error {
		return RenderMarkdown(b, samplePayload(), Options{TopDirs: 10, Language: LangEnglish})
	})
	if !strings.Contains(md, "1. ") || !strings.Contains(md, "6. ") {
		t.Errorf("next steps are not numbered:\n%s", md)
	}
}

func TestRenderJSONShape(t *testing.T) {
	out := render(t, func(b *bytes.Buffer) error {
		return RenderJSON(b, samplePayload())
	})
	var got struct {
		Tool struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"tool"`
		GeneratedAt string                `json:"generatedAt"`
		Target      TargetInfo            `json:"target"`
		Analysis    map[string]any        `json:"analysis"`
		Database    *magento.OrphanReport `json:"database"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if got.Tool.Name != "mage-mediagc" || got.Tool.Version != "v0.1.0" {
		t.Errorf("tool info wrong: %+v", got.Tool)
	}
	if got.GeneratedAt == "" {
		t.Error("generatedAt is empty")
	}
	if got.Database == nil || got.Database.TotalOrphans != 300 {
		t.Errorf("database section wrong: %+v", got.Database)
	}
	for _, key := range []string{"diskFiles", "orphanFiles", "liveFiles", "missing", "warnings"} {
		if _, ok := got.Analysis[key]; !ok {
			t.Errorf("analysis JSON is missing key %q", key)
		}
	}
	// The orphan file list is deliberately not serialized: a JSON report of a
	// 448k-file catalog should not be a 448k-line document.
	if _, ok := got.Analysis["Orphans"]; ok {
		t.Error("the orphan file list must not be part of the JSON payload")
	}
}

func TestRenderRejectsEmptyAnalysis(t *testing.T) {
	p := &Payload{}
	if err := RenderTable(&bytes.Buffer{}, p, Options{}); err == nil {
		t.Error("expected an error rendering a payload with no analysis")
	}
	if err := RenderMarkdown(&bytes.Buffer{}, p, Options{}); err == nil {
		t.Error("expected an error rendering a payload with no analysis")
	}
}

// The language tags are declared in two packages: config owns validation,
// report owns the message tables. They must not drift apart, or a config that
// validates would render in the wrong language.
func TestLanguageTagsMatchConfig(t *testing.T) {
	if len(Languages) != len(config.SupportedLanguages) {
		t.Fatalf("report.Languages = %v, config.SupportedLanguages = %v",
			Languages, config.SupportedLanguages)
	}
	for _, l := range config.SupportedLanguages {
		found := false
		for _, r := range Languages {
			if string(l) == r {
				found = true
			}
		}
		if !found {
			t.Errorf("config accepts language %q but report has no message table for it", l)
		}
	}
	// And every advertised language must actually have a table.
	for _, l := range Languages {
		if _, ok := messageTable[l]; !ok {
			t.Errorf("language %q is advertised but has no message table", l)
		}
	}
}

// Every message table must define the strings the renderers dereference;
// an empty field would silently produce a blank label.
func TestMessageTablesArePopulated(t *testing.T) {
	required := map[string]func(messages) string{
		"mediaRoot":            func(m messages) string { return m.mediaRoot },
		"database":             func(m messages) string { return m.database },
		"reclaimDetail":        func(m messages) string { return m.reclaimDetail },
		"rowCountBytes":        func(m messages) string { return m.rowCountBytes },
		"rowCountBytesPercent": func(m messages) string { return m.rowCountBytesPercent },
		"rowsUnit":             func(m messages) string { return m.rowsUnit },
		"mdTitle":              func(m messages) string { return m.mdTitle },
		"mdDBSummary":          func(m messages) string { return m.mdDBSummary },
		"mdMoreMissing":        func(m messages) string { return m.mdMoreMissing },
		"mdOrphanHotspots":     func(m messages) string { return m.mdOrphanHotspots },
		"mdDBOrphans":          func(m messages) string { return m.mdDBOrphans },
		"mdWarnings":           func(m messages) string { return m.mdWarnings },
		"mdOrphanRateHdr":      func(m messages) string { return m.mdOrphanRateHdr },
	}
	for _, lang := range Languages {
		m := messageTable[lang]
		for name, get := range required {
			if strings.TrimSpace(get(m)) == "" {
				t.Errorf("language %q: message %s is empty", lang, name)
			}
		}
		if len(m.mdSteps) == 0 {
			t.Errorf("language %q has no next steps", lang)
		}
		for i, s := range m.mdSteps {
			if strings.TrimSpace(s) == "" {
				t.Errorf("language %q: next step %d is empty", lang, i+1)
			}
		}
	}
}
