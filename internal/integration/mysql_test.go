// Package integration exercises mage-mediagc end to end against a real MySQL
// server: a miniature but faithful Magento 2 catalog schema is created, filled
// with the exact debris that motivates the tool, and then scanned, analyzed,
// cleaned and verified.
//
// The tests are skipped unless MAGEGC_TEST_DB_HOST is set, so `go test ./...`
// stays hermetic on a developer machine. CI supplies a MySQL service
// container; see .github/workflows/ci.yml.
//
// Every table is created with a per-run prefix and dropped afterwards, and the
// database name must look like a scratch database — this suite must never be
// pointed at a production catalog by accident.
package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/shuaiZend/mage-mediagc/internal/action"
	"github.com/shuaiZend/mage-mediagc/internal/analyzer"
	"github.com/shuaiZend/mage-mediagc/internal/config"
	"github.com/shuaiZend/mage-mediagc/internal/magento"
	"github.com/shuaiZend/mage-mediagc/internal/media"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

func testDB(t *testing.T) config.DB {
	t.Helper()
	host := os.Getenv("MAGEGC_TEST_DB_HOST")
	if host == "" {
		t.Skip("skipping MySQL integration test: set MAGEGC_TEST_DB_HOST to run it")
	}
	port, err := strconv.Atoi(os.Getenv("MAGEGC_TEST_DB_PORT"))
	if err != nil || port == 0 {
		port = 3306
	}
	name := os.Getenv("MAGEGC_TEST_DB_NAME")
	if !strings.Contains(strings.ToLower(name), "test") {
		t.Fatalf("refusing to run against database %q: the name must contain \"test\" "+
			"so a production catalog can never be touched by accident", name)
	}
	return config.DB{
		Host:     host,
		Port:     port,
		Name:     name,
		User:     os.Getenv("MAGEGC_TEST_DB_USER"),
		Password: os.Getenv("MAGEGC_TEST_DB_PASSWORD"),
		Charset:  "utf8mb4",
	}
}

// fixture owns the schema, the client and the on-disk media tree.
type fixture struct {
	t         *testing.T
	client    *magento.Client
	prefix    string
	mediaRoot string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()

	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	prefix := "t" + hex.EncodeToString(raw[:]) + "_"

	client, err := magento.Open(ctx, testDB(t), prefix)
	if err != nil {
		t.Fatalf("connect to MySQL: %v", err)
	}
	f := &fixture{t: t, client: client, prefix: prefix, mediaRoot: t.TempDir()}
	t.Cleanup(func() {
		f.dropAll()
		_ = client.Close()
	})
	f.createSchema()
	return f
}

// exec runs a statement with the fixture's table prefix substituted.
func (f *fixture) exec(query string, args ...any) {
	f.t.Helper()
	stmt := fmt.Sprintf(query, f.prefix)
	if _, err := f.client.DB().ExecContext(context.Background(), stmt, args...); err != nil {
		f.t.Fatalf("exec failed:\n%s\nerror: %v", stmt, err)
	}
}

func (f *fixture) table(name string) string { return f.prefix + name }

func (f *fixture) createSchema() {
	f.t.Helper()
	f.dropAll()

	// Product entity plus the five EAV/gallery tables the cleaner touches.
	// Other Magento tables are deliberately absent: the tool must degrade
	// gracefully on partial schemas, and this proves it does.
	schema := []string{
		`CREATE TABLE %scatalog_product_entity (
			entity_id INT UNSIGNED NOT NULL AUTO_INCREMENT,
			type_id VARCHAR(32) NOT NULL DEFAULT 'simple',
			sku VARCHAR(64) NOT NULL,
			PRIMARY KEY (entity_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE %scatalog_product_entity_media_gallery (
			value_id INT UNSIGNED NOT NULL AUTO_INCREMENT,
			attribute_id SMALLINT UNSIGNED NOT NULL DEFAULT 0,
			value VARCHAR(255) DEFAULT NULL,
			media_type VARCHAR(32) NOT NULL DEFAULT 'image',
			disabled SMALLINT UNSIGNED NOT NULL DEFAULT 0,
			PRIMARY KEY (value_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE %scatalog_product_entity_media_gallery_value_to_entity (
			value_id INT UNSIGNED NOT NULL,
			entity_id INT UNSIGNED NOT NULL,
			PRIMARY KEY (value_id, entity_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE %scatalog_product_entity_media_gallery_value (
			value_id INT UNSIGNED NOT NULL,
			store_id SMALLINT UNSIGNED NOT NULL DEFAULT 0,
			label VARCHAR(255) DEFAULT NULL,
			position INT UNSIGNED DEFAULT NULL,
			disabled SMALLINT UNSIGNED NOT NULL DEFAULT 0,
			PRIMARY KEY (value_id, store_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE %scatalog_product_entity_varchar (
			value_id INT UNSIGNED NOT NULL AUTO_INCREMENT,
			attribute_id SMALLINT UNSIGNED NOT NULL DEFAULT 0,
			store_id SMALLINT UNSIGNED NOT NULL DEFAULT 0,
			entity_id INT UNSIGNED NOT NULL DEFAULT 0,
			value VARCHAR(255) DEFAULT NULL,
			PRIMARY KEY (value_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE %scatalog_product_entity_text (
			value_id INT UNSIGNED NOT NULL AUTO_INCREMENT,
			attribute_id SMALLINT UNSIGNED NOT NULL DEFAULT 0,
			store_id SMALLINT UNSIGNED NOT NULL DEFAULT 0,
			entity_id INT UNSIGNED NOT NULL DEFAULT 0,
			value MEDIUMTEXT,
			PRIMARY KEY (value_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE %seav_entity_type (
			entity_type_id SMALLINT UNSIGNED NOT NULL AUTO_INCREMENT,
			entity_type_code VARCHAR(50) NOT NULL,
			PRIMARY KEY (entity_type_id),
			UNIQUE KEY ENTITY_TYPE_CODE (entity_type_code)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE %seav_attribute (
			attribute_id SMALLINT UNSIGNED NOT NULL AUTO_INCREMENT,
			entity_type_id SMALLINT UNSIGNED NOT NULL DEFAULT 0,
			attribute_code VARCHAR(255) NOT NULL,
			backend_type VARCHAR(8) DEFAULT NULL,
			PRIMARY KEY (attribute_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE %scatalog_category_entity (
			entity_id INT UNSIGNED NOT NULL AUTO_INCREMENT,
			path VARCHAR(255) NOT NULL DEFAULT '1',
			PRIMARY KEY (entity_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE %scatalog_category_entity_varchar (
			value_id INT UNSIGNED NOT NULL AUTO_INCREMENT,
			attribute_id SMALLINT UNSIGNED NOT NULL DEFAULT 0,
			store_id SMALLINT UNSIGNED NOT NULL DEFAULT 0,
			entity_id INT UNSIGNED NOT NULL DEFAULT 0,
			value VARCHAR(255) DEFAULT NULL,
			PRIMARY KEY (value_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE %scms_page (
			page_id SMALLINT UNSIGNED NOT NULL AUTO_INCREMENT,
			identifier VARCHAR(100) NOT NULL DEFAULT '',
			content MEDIUMTEXT,
			PRIMARY KEY (page_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE TABLE %scms_block (
			block_id SMALLINT UNSIGNED NOT NULL AUTO_INCREMENT,
			identifier VARCHAR(100) NOT NULL DEFAULT '',
			content MEDIUMTEXT,
			PRIMARY KEY (block_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	}
	for _, s := range schema {
		f.exec(s)
	}

	// Live products 1 and 2. Product 200 is referenced all over the place but
	// has no row: this is the bulk-delete residue the tool targets.
	f.exec(`INSERT INTO %scatalog_product_entity (entity_id, type_id, sku) VALUES
		(1, 'simple', 'live-1'), (2, 'simple', 'live-2')`)

	// Gallery: one live entry, one belonging to the deleted product, one with
	// no product link at all.
	f.exec(`INSERT INTO %scatalog_product_entity_media_gallery (value_id, value, media_type) VALUES
		(10, '/l/i/live-gallery.jpg', 'image'),
		(11, '/d/e/dead-product.jpg', 'image'),
		(12, '/n/o/no-link.jpg', 'image')`)

	f.exec(`INSERT INTO %scatalog_product_entity_media_gallery_value_to_entity (value_id, entity_id) VALUES
		(10, 1), (11, 200)`)

	f.exec(`INSERT INTO %scatalog_product_entity_media_gallery_value (value_id, store_id, position) VALUES
		(10, 0, 1), (11, 0, 2), (12, 0, 3)`)

	// EAV scaffolding.
	f.exec(`INSERT INTO %seav_entity_type (entity_type_id, entity_type_code) VALUES
		(1, 'catalog_product'), (2, 'catalog_category')`)
	f.exec(`INSERT INTO %seav_attribute (attribute_id, entity_type_id, attribute_code, backend_type) VALUES
		(100, 1, 'image', 'varchar'),
		(101, 1, 'small_image', 'varchar'),
		(102, 1, 'thumbnail', 'varchar'),
		(103, 1, 'swatch_image', 'varchar'),
		(104, 1, 'description', 'text'),
		(200, 2, 'image', 'varchar')`)

	// Image-role attributes: a live one, one for the deleted product, and a
	// 'no_selection' placeholder that must never be treated as a reference.
	f.exec(`INSERT INTO %scatalog_product_entity_varchar
		(value_id, attribute_id, store_id, entity_id, value) VALUES
		(100, 100, 0, 1, 'a/b/live-role.jpg'),
		(101, 101, 0, 2, 's/m/small-only.jpg'),
		(102, 102, 0, 2, 'no_selection'),
		(103, 100, 0, 200, 'd/e/dead-role.jpg')`)

	// Descriptions: one live (both the HTML and the directive form), one
	// belonging to the deleted product.
	f.exec(`INSERT INTO %scatalog_product_entity_text
		(value_id, attribute_id, store_id, entity_id, value) VALUES
		(200, 104, 0, 1, '<p><img src="/media/catalog/product/c/m/desc-only.jpg"/>{{media url="catalog/product/c/m/desc-directive.jpg"}}</p>'),
		(201, 104, 0, 200, '<p><img src="/media/catalog/product/o/l/old-desc.jpg"/></p>')`)

	// A category image: referenced by nothing in the gallery, which is exactly
	// the case gallery-only tools get wrong.
	f.exec(`INSERT INTO %scatalog_category_entity (entity_id, path) VALUES (1, '1')`)
	f.exec(`INSERT INTO %scatalog_category_entity_varchar
		(value_id, attribute_id, store_id, entity_id, value) VALUES
		(500, 200, 0, 1, 'c/m/category-only.jpg')`)

	// CMS content, likewise invisible to a gallery-only comparison.
	f.exec(`INSERT INTO %scms_page (page_id, identifier, content) VALUES
		(1, 'home', '<p><img src="/media/catalog/product/c/m/cms-page-only.png"/></p>')`)
	f.exec(`INSERT INTO %scms_block (block_id, identifier, content) VALUES
		(1, 'footer', '<p>{{media url="catalog/product/c/m/cms-block-only.jpg"}}</p>')`)
}

func (f *fixture) dropAll() {
	tables := []string{
		"catalog_product_entity", "catalog_product_entity_media_gallery",
		"catalog_product_entity_media_gallery_value_to_entity",
		"catalog_product_entity_media_gallery_value",
		"catalog_product_entity_varchar", "catalog_product_entity_text",
		"eav_entity_type", "eav_attribute",
		"catalog_category_entity", "catalog_category_entity_varchar",
		"cms_page", "cms_block",
	}
	for _, t2 := range tables {
		_, _ = f.client.DB().ExecContext(context.Background(), "DROP TABLE IF EXISTS `"+f.table(t2)+"`")
	}
}

// writeMedia lays out the on-disk side of the fixture.
func (f *fixture) writeMedia(t *testing.T) {
	t.Helper()
	files := map[string]int{
		// live
		"l/i/live-gallery.jpg":   1024,
		"a/b/live-role.jpg":      2048,
		"s/m/small-only.jpg":     3072,
		"c/m/category-only.jpg":  4096, // category-only reference
		"c/m/desc-only.jpg":      5120, // product description
		"c/m/desc-directive.jpg": 6144, // product description, directive form
		"c/m/cms-page-only.png":  7168, // CMS page
		"c/m/cms-block-only.jpg": 8192, // CMS block
		// orphans
		"d/e/dead-product.jpg": 16384,
		"n/o/no-link.jpg":      32768,
		"d/e/dead-role.jpg":    65536,
		"o/l/old-desc.jpg":     131072,
		"z/z/never.jpg":        262144,
		// derived cache: measured, never orphaned
		"cache/abc123/1/2/derived.jpg": 4096,
	}
	for rel, size := range files {
		p := filepath.Join(f.mediaRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

var wantOrphans = []string{
	"d/e/dead-product.jpg",
	"d/e/dead-role.jpg",
	"n/o/no-link.jpg",
	"o/l/old-desc.jpg",
	"z/z/never.jpg",
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestEndToEnd walks the full pipeline against a real database, in the order
// an operator would run it.
func TestEndToEnd(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.writeMedia(t)

	// --- 1. reference collection ------------------------------------------
	refs, err := f.client.CollectRefs(ctx, magento.RefOptions{IncludeContentRefs: true})
	if err != nil {
		t.Fatalf("CollectRefs: %v", err)
	}

	livePaths := []string{
		"l/i/live-gallery.jpg",   // media_gallery
		"a/b/live-role.jpg",      // image attribute
		"s/m/small-only.jpg",     // small_image attribute
		"c/m/category-only.jpg",  // category image
		"c/m/desc-only.jpg",      // product description, HTML form
		"c/m/desc-directive.jpg", // product description, directive form
		"c/m/cms-page-only.png",  // CMS page
		"c/m/cms-block-only.jpg", // CMS block
	}
	for _, p := range livePaths {
		if !refs.Contains(p) {
			t.Errorf("reference %q was not collected", p)
		}
	}
	// Refs belonging to the deleted product must NOT be collected: they are
	// precisely what makes their files orphans.
	for _, p := range []string{"d/e/dead-product.jpg", "d/e/dead-role.jpg", "o/l/old-desc.jpg"} {
		if refs.Contains(p) {
			t.Errorf("reference %q belongs to a deleted product but was collected", p)
		}
	}
	if refs.Contains("no_selection") {
		t.Error("the 'no_selection' placeholder must never count as a reference")
	}
	// Every configured source should have contributed.
	for _, want := range []string{
		"media_gallery", "product_image_attr", "category_image",
		"product_content", "cms_content_cms_page", "cms_content_cms_block",
	} {
		found := false
		for _, s := range refs.Stats {
			if s.Source == want && s.Skipped == "" {
				found = true
			}
		}
		if !found {
			t.Errorf("reference source %q contributed nothing: %+v", want, refs.Stats)
		}
	}

	// --- 2. filesystem scan and analysis ----------------------------------
	scan, err := media.Scan(ctx, f.mediaRoot, media.ScanOptions{Workers: 2})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(scan.Files) != 13 {
		t.Fatalf("scanned %d original files, want 13 (cache excluded)", len(scan.Files))
	}
	if scan.CacheFiles != 1 {
		t.Fatalf("CacheFiles = %d, want 1 measured separately", scan.CacheFiles)
	}

	res, err := analyzer.Analyze(ctx, scan, refs, analyzer.DefaultOptions())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.LiveFiles != 8 || res.OrphanFiles != 5 {
		t.Fatalf("analysis: live=%d orphan=%d, want 8/5 (%+v)",
			res.LiveFiles, res.OrphanFiles, res.Warnings)
	}
	var got []string
	for _, o := range res.Orphans {
		got = append(got, o.RelPath)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(wantOrphans, ",") {
		t.Fatalf("orphan set mismatch:\n got %v\nwant %v", got, wantOrphans)
	}
	if len(res.Missing) != 0 {
		t.Errorf("expected no missing references, got %v", res.Missing)
	}

	// --- 3. orphan statistics (read-only) ---------------------------------
	before, err := f.client.OrphanStats(ctx, nil)
	if err != nil {
		t.Fatalf("OrphanStats: %v", err)
	}
	if before.ProductCount != 2 {
		t.Errorf("ProductCount = %d, want 2", before.ProductCount)
	}
	wantStats := map[string]int64{
		"catalog_product_entity_media_gallery":                 1, // value 12 has no link
		"catalog_product_entity_media_gallery_value_to_entity": 1, // link to product 200
		"catalog_product_entity_media_gallery_value":           0, // cascades only after a delete
		"catalog_product_entity_varchar":                       1, // entity 200
		"catalog_product_entity_text":                          1, // entity 200
	}
	for table, want := range wantStats {
		st := statFor(t, before, table)
		if st.Orphans != want {
			t.Errorf("%s: orphans = %d, want %d", table, st.Orphans, want)
		}
	}
	// Tables that are not part of this schema must be reported as skipped
	// rather than counted as clean or as fully orphaned.
	if st := statFor(t, before, "cataloginventory_stock_item"); st.Skipped == "" {
		t.Errorf("a missing table should be reported as skipped, got %+v", st)
	}

	// --- 4. dry run changes nothing ---------------------------------------
	dry, err := f.client.CleanOrphans(ctx, magento.CleanOptions{DryRun: true})
	if err != nil {
		t.Fatalf("CleanOrphans dry run: %v", err)
	}
	afterDry, err := f.client.OrphanStats(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if afterDry.TotalOrphans != before.TotalOrphans {
		t.Fatalf("dry run modified the database: %d -> %d", before.TotalOrphans, afterDry.TotalOrphans)
	}
	// A single pass can only see what is already dangling: one gallery entry
	// with no link, one link to the deleted product, one varchar and one text
	// row. The per-store gallery values are not orphans *yet* — they only
	// become so once their gallery entries disappear, which the dry run does
	// not do. So 4 here, and 7 after the cascade (see below).
	if dry.TotalDeleted != 4 {
		t.Errorf("dry run predicted %d rows, want 4", dry.TotalDeleted)
	}

	// --- 5. real cleanup, including the cascade ---------------------------
	rep, err := f.client.CleanOrphans(ctx, magento.CleanOptions{BatchSize: 2, MaxPasses: 5})
	if err != nil {
		t.Fatalf("CleanOrphans: %v", err)
	}
	if rep.DryRun {
		t.Fatal("report claims to be a dry run")
	}
	// 1 link + 1 stale gallery entry (no link) + 1 gallery entry orphaned by
	// the link removal + 2 of its per-store values + 1 varchar + 1 text row.
	if rep.TotalDeleted != 7 {
		t.Errorf("deleted %d rows, want 7", rep.TotalDeleted)
	}
	after, err := f.client.OrphanStats(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if after.TotalOrphans != 0 {
		t.Errorf("cleanup left %d orphaned rows behind: %+v", after.TotalOrphans, after.Tables)
	}
	// Live products must survive untouched.
	if n, err := f.client.CountRows(ctx, "catalog_product_entity"); err != nil || n != 2 {
		t.Errorf("product rows = %d (err %v), want 2", n, err)
	}
	// The live gallery entry must still be there.
	if n, err := f.client.CountRows(ctx, "catalog_product_entity_media_gallery"); err != nil || n != 1 {
		t.Errorf("gallery rows = %d (err %v), want 1", n, err)
	}

	// --- 6. quarantine, restore, purge ------------------------------------
	quarantineDir := filepath.Join(t.TempDir(), "quarantine")
	orphanSet := res.Orphans
	qres, err := action.Quarantine(ctx, action.QuarantineOptions{
		MediaRoot:     f.mediaRoot,
		QuarantineDir: quarantineDir,
		Orphans:       orphanSet,
		Parallel:      4,
		ToolVersion:   "test",
	})
	if err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	if qres.Moved != 5 || qres.Failed != 0 {
		t.Fatalf("quarantine moved=%d failed=%d (%v)", qres.Moved, qres.Failed, qres.Failures)
	}
	for _, p := range wantOrphans {
		if fileExists(filepath.Join(f.mediaRoot, filepath.FromSlash(p))) {
			t.Errorf("orphan %s is still in the media tree", p)
		}
		if !fileExists(filepath.Join(quarantineDir, filepath.FromSlash(p))) {
			t.Errorf("orphan %s was not quarantined", p)
		}
	}
	for _, p := range livePaths {
		if !fileExists(filepath.Join(f.mediaRoot, filepath.FromSlash(p))) {
			t.Errorf("live file %s was moved away", p)
		}
	}
	entries, meta, err := action.LoadManifest(quarantineDir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(entries) != 5 {
		t.Errorf("manifest has %d entries, want 5", len(entries))
	}
	if meta == nil || meta.MediaRoot != f.mediaRoot {
		t.Errorf("manifest metadata wrong: %+v", meta)
	}

	// Roll everything back and confirm byte-for-byte restoration.
	rres, err := action.Restore(ctx, action.RestoreOptions{
		QuarantineDir: quarantineDir,
		MediaRoot:     f.mediaRoot,
		Parallel:      4,
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if rres.Restored != 5 || rres.Failed != 0 {
		t.Fatalf("restore: restored=%d failed=%d", rres.Restored, rres.Failed)
	}
	for _, p := range wantOrphans {
		if !fileExists(filepath.Join(f.mediaRoot, filepath.FromSlash(p))) {
			t.Errorf("orphan %s was not restored", p)
		}
	}

	// Isolate again, then purge for real.
	if _, err := action.Quarantine(ctx, action.QuarantineOptions{
		MediaRoot: f.mediaRoot, QuarantineDir: quarantineDir, Orphans: orphanSet, Parallel: 4,
	}); err != nil {
		t.Fatalf("Quarantine (second run): %v", err)
	}
	pres, err := action.Purge(ctx, action.PurgeOptions{QuarantineDir: quarantineDir})
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if pres.Removed == 0 {
		t.Error("purge reported nothing removed")
	}
	if fileExists(quarantineDir) {
		t.Error("quarantine directory survived the purge")
	}
}

// TestCacheOnlyDeletionStaysInsideTheCache is a guard rail for the one
// operation that has no rollback: it must not touch originals.
func TestCacheOnlyDeletionStaysInsideTheCache(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.writeMedia(t)

	before := countFiles(t, f.mediaRoot)

	res, err := action.CleanCache(ctx, f.mediaRoot, false, nil)
	if err != nil {
		t.Fatalf("CleanCache: %v", err)
	}
	if res.RemovedFiles != 1 {
		t.Fatalf("removed %d cache files, want 1", res.RemovedFiles)
	}
	if !res.Recreated {
		t.Error("cache directory was not recreated")
	}
	if n := countFiles(t, f.mediaRoot); n != before-1 {
		t.Errorf("file count went from %d to %d; exactly one file should be gone", before, n)
	}
	if !fileExists(filepath.Join(f.mediaRoot, "cache")) {
		t.Error("cache directory must be recreated so Magento can repopulate it")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func statFor(t *testing.T, rep *magento.OrphanReport, table string) magento.OrphanStat {
	t.Helper()
	for _, s := range rep.Tables {
		if s.Table == table {
			return s
		}
	}
	t.Fatalf("table %s missing from the report", table)
	return magento.OrphanStat{}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func countFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
