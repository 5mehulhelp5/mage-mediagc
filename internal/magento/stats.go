package magento

import (
	"context"
	"fmt"
)

// orphanTarget describes a table that accumulates rows pointing at products
// (or gallery entries) which no longer exist.
//
// The cleanup is expressed as "rows whose reference is missing", which is
// exactly the shape left behind by bulk deletes, imports and direct SQL work
// on a Magento catalog.
type orphanTarget struct {
	Table       string
	Description string
	// PkCols identifies a row for batched deletion.
	PkCols []string
	// RefTable/RefCol form the LEFT JOIN used to detect dangling rows:
	//   LEFT JOIN RefTable r ON r.RefKey = t.RefCol WHERE r.RefKey IS NULL
	RefTable string
	RefKey   string
	RefCol   string
}

// orphanTargets is ordered so that a table is always cleaned before any table
// it points at (its RefTable). Deleting rows can orphan rows elsewhere, and
// cleaning children first means a later target still sees the references that
// point at rows already scheduled for removal — which is what makes repeated
// sweeps converge instead of oscillating.
//
// The gallery trio illustrates the chain: media_gallery_value and
// media_gallery_value_video point at media_gallery, and media_gallery points
// at media_gallery_value_to_entity (an entry survives only while some product
// still links to it). So the values go first, the gallery entries next, and
// the product links last.
func orphanTargets() []orphanTarget {
	const prod = "catalog_product_entity"
	const gallery = "catalog_product_entity_media_gallery"
	const galleryLink = "catalog_product_entity_media_gallery_value_to_entity"
	return []orphanTarget{
		{
			Table: "catalog_product_entity_media_gallery_value", Description: "gallery values of missing gallery entries",
			PkCols: []string{"value_id", "store_id"}, RefTable: gallery, RefKey: "value_id", RefCol: "value_id",
		},
		{
			Table: "catalog_product_entity_media_gallery_value_video", Description: "gallery videos of missing gallery entries",
			PkCols: []string{"value_id", "store_id"}, RefTable: gallery, RefKey: "value_id", RefCol: "value_id",
		},
		{
			Table: gallery, Description: "gallery entries with no product link",
			PkCols: []string{"value_id"}, RefTable: galleryLink, RefKey: "value_id", RefCol: "value_id",
		},
		{
			Table: galleryLink, Description: "gallery links to deleted products",
			PkCols: []string{"value_id", "entity_id"}, RefTable: prod, RefKey: "entity_id", RefCol: "entity_id",
		},
		{
			Table: "catalog_product_entity_int", Description: "integer attributes of deleted products",
			PkCols: []string{"value_id"}, RefTable: prod, RefKey: "entity_id", RefCol: "entity_id",
		},
		{
			Table: "catalog_product_entity_varchar", Description: "varchar attributes of deleted products",
			PkCols: []string{"value_id"}, RefTable: prod, RefKey: "entity_id", RefCol: "entity_id",
		},
		{
			Table: "catalog_product_entity_text", Description: "text attributes of deleted products",
			PkCols: []string{"value_id"}, RefTable: prod, RefKey: "entity_id", RefCol: "entity_id",
		},
		{
			Table: "catalog_product_entity_decimal", Description: "decimal attributes of deleted products",
			PkCols: []string{"value_id"}, RefTable: prod, RefKey: "entity_id", RefCol: "entity_id",
		},
		{
			Table: "catalog_product_entity_datetime", Description: "datetime attributes of deleted products",
			PkCols: []string{"value_id"}, RefTable: prod, RefKey: "entity_id", RefCol: "entity_id",
		},
		{
			Table: "catalog_product_entity_gallery", Description: "legacy product gallery rows of deleted products",
			PkCols: []string{"value_id"}, RefTable: prod, RefKey: "entity_id", RefCol: "entity_id",
		},
		{
			Table: "cataloginventory_stock_item", Description: "stock items of deleted products",
			PkCols: []string{"item_id"}, RefTable: prod, RefKey: "entity_id", RefCol: "product_id",
		},
		{
			Table: "catalog_category_product", Description: "category links to deleted products",
			PkCols: []string{"category_id", "product_id"}, RefTable: prod, RefKey: "entity_id", RefCol: "product_id",
		},
		{
			Table: "catalog_product_website", Description: "website links to deleted products",
			PkCols: []string{"product_id", "website_id"}, RefTable: prod, RefKey: "entity_id", RefCol: "product_id",
		},
		{
			Table: "catalog_product_link", Description: "related/upsell links of deleted products",
			PkCols: []string{"link_id"}, RefTable: prod, RefKey: "entity_id", RefCol: "product_id",
		},
		{
			Table: "catalog_product_super_link", Description: "configurable child links to deleted products",
			PkCols: []string{"link_id"}, RefTable: prod, RefKey: "entity_id", RefCol: "product_id",
		},
	}
}

// OrphanStat reports the orphan situation of a single table.
type OrphanStat struct {
	Table       string  `json:"table"`
	Description string  `json:"description"`
	Total       int64   `json:"total"`
	Orphans     int64   `json:"orphans"`
	Percent     float64 `json:"percent"`
	Skipped     string  `json:"skipped,omitempty"`
}

// OrphanReport aggregates orphan statistics.
type OrphanReport struct {
	ProductCount int64        `json:"productCount"`
	Tables       []OrphanStat `json:"tables"`
	TotalRows    int64        `json:"totalRows"`
	TotalOrphans int64        `json:"totalOrphans"`
}

// OrphanStats measures how many rows in each tracked table point at products
// that no longer exist. It is read-only and safe to run on production.
func (c *Client) OrphanStats(ctx context.Context, progress func(string)) (*OrphanReport, error) {
	rep := &OrphanReport{}

	n, err := c.CountRows(ctx, "catalog_product_entity")
	if err != nil {
		return nil, fmt.Errorf("count products: %w", err)
	}
	if n < 0 {
		return nil, fmt.Errorf("table catalog_product_entity is missing: this does not look like a Magento 2 catalog database")
	}
	rep.ProductCount = n

	for _, t := range orphanTargets() {
		if progress != nil {
			progress(t.Table)
		}
		exists, err := c.TableExists(ctx, t.Table)
		if err != nil {
			return nil, err
		}
		if !exists {
			rep.Tables = append(rep.Tables, OrphanStat{
				Table: t.Table, Description: t.Description, Skipped: "table not present",
			})
			continue
		}
		// The reference table must exist too, otherwise every row would look
		// like an orphan and the numbers would be dangerously wrong.
		refExists, err := c.TableExists(ctx, t.RefTable)
		if err != nil {
			return nil, err
		}
		if !refExists {
			rep.Tables = append(rep.Tables, OrphanStat{
				Table: t.Table, Description: t.Description,
				Skipped: fmt.Sprintf("reference table %s not present", t.RefTable),
			})
			continue
		}

		total, err := c.CountRows(ctx, t.Table)
		if err != nil {
			return nil, err
		}
		orphans, err := c.countOrphans(ctx, t)
		if err != nil {
			return nil, fmt.Errorf("count orphans in %s: %w", t.Table, err)
		}

		st := OrphanStat{
			Table: t.Table, Description: t.Description,
			Total: total, Orphans: orphans,
		}
		if total > 0 {
			st.Percent = float64(orphans) / float64(total) * 100
		}
		rep.Tables = append(rep.Tables, st)
		rep.TotalRows += total
		rep.TotalOrphans += orphans
	}
	return rep, nil
}

func (c *Client) countOrphans(ctx context.Context, t orphanTarget) (int64, error) {
	q := "SELECT COUNT(*) FROM `" + c.Table(t.Table) + "` t" +
		" LEFT JOIN `" + c.Table(t.RefTable) + "` r ON r.`" + t.RefKey + "` = t.`" + t.RefCol + "`" +
		" WHERE r.`" + t.RefKey + "` IS NULL"
	var n int64
	if err := c.db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
