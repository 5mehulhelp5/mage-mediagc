package magento

import (
	"context"
	"fmt"
	"strings"
)

// SourceStat records what one reference source contributed.
type SourceStat struct {
	Source      string `json:"source"`
	Description string `json:"description"`
	RowsScanned int64  `json:"rowsScanned"`
	PathsAdded  int    `json:"pathsAdded"`
	Skipped     string `json:"skipped,omitempty"`
}

// RefSet is the union of every path still referenced by the installation.
//
// The set deliberately stores *candidate* paths (see CandidatePaths) rather
// than a single canonical form: a file is only treated as garbage when no
// reference shape matches it.
type RefSet struct {
	Paths map[string]struct{}
	Stats []SourceStat
}

// NewRefSet returns an empty reference set.
func NewRefSet() *RefSet {
	return &RefSet{Paths: make(map[string]struct{}, 1<<16)}
}

// Add records every candidate shape of a raw reference and returns how many
// new entries were inserted.
func (r *RefSet) Add(raw string) int {
	added := 0
	for _, cand := range CandidatePaths(raw) {
		if _, exists := r.Paths[cand]; exists {
			continue
		}
		r.Paths[cand] = struct{}{}
		added++
	}
	return added
}

// Len returns the number of distinct candidate paths.
func (r *RefSet) Len() int { return len(r.Paths) }

// Contains reports whether a filesystem-relative path is referenced.
func (r *RefSet) Contains(p string) bool {
	_, ok := r.Paths[p]
	return ok
}

// RefOptions tunes reference collection.
type RefOptions struct {
	// IncludeContentRefs additionally parses product descriptions and CMS
	// content for embedded images. Without it, images that only appear inside
	// page copy would be misclassified as orphans.
	IncludeContentRefs bool
}

// CollectRefs walks every reference source and returns their union.
func (c *Client) CollectRefs(ctx context.Context, opts RefOptions) (*RefSet, error) {
	rs := NewRefSet()

	if err := c.collectGallery(ctx, rs); err != nil {
		return nil, err
	}
	if err := c.collectProductRoleAttributes(ctx, rs); err != nil {
		return nil, err
	}
	if err := c.collectCategoryImages(ctx, rs); err != nil {
		return nil, err
	}
	if opts.IncludeContentRefs {
		if err := c.collectProductContent(ctx, rs); err != nil {
			return nil, err
		}
		if err := c.collectCMSContent(ctx, rs); err != nil {
			return nil, err
		}
	}
	return rs, nil
}

// collectGallery reads catalog_product_entity_media_gallery restricted to
// rows still attached to an existing product. Rows belonging to deleted
// products are intentionally excluded: they are exactly the orphans this tool
// exists to find.
func (c *Client) collectGallery(ctx context.Context, rs *RefSet) error {
	required := []string{
		"catalog_product_entity",
		"catalog_product_entity_media_gallery",
		"catalog_product_entity_media_gallery_value_to_entity",
	}
	for _, t := range required {
		ok, err := c.TableExists(ctx, t)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("required table %s is missing: this does not look like a Magento 2 catalog database", t)
		}
	}

	q := "SELECT DISTINCT g.value" +
		" FROM `" + c.Table("catalog_product_entity_media_gallery") + "` g" +
		" JOIN `" + c.Table("catalog_product_entity_media_gallery_value_to_entity") + "` l ON l.value_id = g.value_id" +
		" JOIN `" + c.Table("catalog_product_entity") + "` p ON p.entity_id = l.entity_id"

	values, err := c.scanStrings(ctx, q)
	if err != nil {
		return fmt.Errorf("query media gallery: %w", err)
	}
	stat := SourceStat{
		Source:      "media_gallery",
		Description: "catalog_product_entity_media_gallery joined to existing products",
		RowsScanned: int64(len(values)),
	}
	for _, v := range values {
		stat.PathsAdded += rs.Add(v)
	}
	rs.Stats = append(rs.Stats, stat)
	return nil
}

// collectProductRoleAttributes covers the image / small_image / thumbnail /
// swatch_image attributes. These normally mirror gallery entries, but a
// non-trivial number of catalogs have attributes pointing at files that are
// no longer in the gallery, and deleting those would visibly break listings.
func (c *Client) collectProductRoleAttributes(ctx context.Context, rs *RefSet) error {
	table := "catalog_product_entity_varchar"
	ok, err := c.TableExists(ctx, table)
	if err != nil {
		return err
	}
	if !ok {
		rs.Stats = append(rs.Stats, SourceStat{
			Source: "product_image_attr", Description: "product image role attributes",
			Skipped: "table catalog_product_entity_varchar not present",
		})
		return nil
	}

	ids, err := c.AttributeIDs(ctx, "catalog_product", []string{"image", "small_image", "thumbnail", "swatch_image"})
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		rs.Stats = append(rs.Stats, SourceStat{
			Source: "product_image_attr", Description: "product image role attributes",
			Skipped: "no image role attributes found",
		})
		return nil
	}

	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	q := "SELECT DISTINCT v.value" +
		" FROM `" + c.Table(table) + "` v" +
		" JOIN `" + c.Table("catalog_product_entity") + "` p ON p.entity_id = v.entity_id" +
		" WHERE v.attribute_id IN (" + placeholders(len(args)) + ")" +
		" AND v.value IS NOT NULL AND v.value <> '' AND v.value <> 'no_selection'"

	values, err := c.scanStrings(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("query product image attributes: %w", err)
	}
	stat := SourceStat{
		Source:      "product_image_attr",
		Description: "image, small_image, thumbnail, swatch_image attributes",
		RowsScanned: int64(len(values)),
	}
	for _, v := range values {
		stat.PathsAdded += rs.Add(v)
	}
	rs.Stats = append(rs.Stats, stat)
	return nil
}

// collectCategoryImages covers category landing images.
func (c *Client) collectCategoryImages(ctx context.Context, rs *RefSet) error {
	table := "catalog_category_entity_varchar"
	ok, err := c.TableExists(ctx, table)
	if err != nil {
		return err
	}
	if !ok {
		rs.Stats = append(rs.Stats, SourceStat{
			Source: "category_image", Description: "category image attributes",
			Skipped: "table catalog_category_entity_varchar not present",
		})
		return nil
	}

	ids, err := c.AttributeIDs(ctx, "catalog_category", []string{"image", "thumbnail"})
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		rs.Stats = append(rs.Stats, SourceStat{
			Source: "category_image", Description: "category image attributes",
			Skipped: "no category image attributes found",
		})
		return nil
	}

	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	q := "SELECT DISTINCT v.value" +
		" FROM `" + c.Table(table) + "` v" +
		" JOIN `" + c.Table("catalog_category_entity") + "` e ON e.entity_id = v.entity_id" +
		" WHERE v.attribute_id IN (" + placeholders(len(args)) + ")" +
		" AND v.value IS NOT NULL AND v.value <> ''"

	values, err := c.scanStrings(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("query category images: %w", err)
	}
	stat := SourceStat{
		Source:      "category_image",
		Description: "category image and thumbnail attributes",
		RowsScanned: int64(len(values)),
	}
	for _, v := range values {
		stat.PathsAdded += rs.Add(v)
	}
	rs.Stats = append(rs.Stats, stat)
	return nil
}

// collectProductContent parses descriptions for embedded media references.
func (c *Client) collectProductContent(ctx context.Context, rs *RefSet) error {
	table := "catalog_product_entity_text"
	ok, err := c.TableExists(ctx, table)
	if err != nil {
		return err
	}
	if !ok {
		rs.Stats = append(rs.Stats, SourceStat{
			Source: "product_content", Description: "images embedded in product descriptions",
			Skipped: "table catalog_product_entity_text not present",
		})
		return nil
	}

	ids, err := c.AttributeIDs(ctx, "catalog_product", []string{"description", "short_description"})
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		rs.Stats = append(rs.Stats, SourceStat{
			Source: "product_content", Description: "images embedded in product descriptions",
			Skipped: "no description attributes found",
		})
		return nil
	}

	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	q := "SELECT v.value" +
		" FROM `" + c.Table(table) + "` v" +
		" JOIN `" + c.Table("catalog_product_entity") + "` p ON p.entity_id = v.entity_id" +
		" WHERE v.attribute_id IN (" + placeholders(len(args)) + ")" +
		" AND v.value LIKE '%media%'"

	values, err := c.scanStrings(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("query product descriptions: %w", err)
	}
	stat := SourceStat{
		Source:      "product_content",
		Description: "images embedded in product descriptions",
		RowsScanned: int64(len(values)),
	}
	for _, content := range values {
		for _, ref := range ExtractMediaPaths(content) {
			stat.PathsAdded += rs.Add(ref)
		}
	}
	rs.Stats = append(rs.Stats, stat)
	return nil
}

// collectCMSContent parses CMS pages and blocks for embedded media references.
func (c *Client) collectCMSContent(ctx context.Context, rs *RefSet) error {
	for _, tbl := range []string{"cms_page", "cms_block"} {
		ok, err := c.TableExists(ctx, tbl)
		if err != nil {
			return err
		}
		if !ok {
			rs.Stats = append(rs.Stats, SourceStat{
				Source: "cms_content_" + tbl, Description: "images embedded in " + tbl,
				Skipped: "table not present",
			})
			continue
		}
		q := "SELECT content FROM `" + c.Table(tbl) + "` WHERE content LIKE '%media%'"
		values, err := c.scanStrings(ctx, q)
		if err != nil {
			return fmt.Errorf("query %s: %w", tbl, err)
		}
		stat := SourceStat{
			Source:      "cms_content_" + tbl,
			Description: "images embedded in " + tbl,
			RowsScanned: int64(len(values)),
		}
		for _, content := range values {
			for _, ref := range ExtractMediaPaths(content) {
				stat.PathsAdded += rs.Add(ref)
			}
		}
		rs.Stats = append(rs.Stats, stat)
	}
	return nil
}

// scanStrings runs a single-column query and returns all values.
func (c *Client) scanStrings(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		if strings.TrimSpace(s) == "" {
			continue
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
