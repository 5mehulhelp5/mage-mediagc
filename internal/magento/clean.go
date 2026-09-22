package magento

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// CleanOptions tunes database cleanup.
type CleanOptions struct {
	// DryRun counts what would be removed without changing anything.
	DryRun bool
	// BatchSize is the number of rows deleted per statement. Small batches keep
	// replication lag and row locks predictable on live systems.
	BatchSize int
	// MaxPasses bounds how many whole sweeps run. See CleanOrphans.
	MaxPasses int
	// Progress is called after each batch with the running total for a table.
	Progress func(table string, deleted int64)
}

// CleanTableResult reports what happened to one table.
type CleanTableResult struct {
	Table       string `json:"table"`
	Description string `json:"description"`
	Deleted     int64  `json:"deleted"`
	Skipped     string `json:"skipped,omitempty"`
}

// CleanReport aggregates a cleanup run.
type CleanReport struct {
	DryRun       bool               `json:"dryRun"`
	Results      []CleanTableResult `json:"results"`
	TotalDeleted int64              `json:"totalDeleted"`
}

// CleanOrphans removes rows that reference products (or gallery entries)
// which no longer exist.
//
// Because deleting a row can orphan rows in a table already visited — most
// visibly, dropping a product's last gallery link orphans the gallery entry,
// which in turn orphans its per-store values — a single pass is not enough.
// The whole ordered list is swept repeatedly until a sweep deletes nothing,
// bounded by MaxPasses so a pathological schema cannot loop forever.
//
// Safety properties:
//   - every table is validated to exist, and its reference table too, before
//     any DELETE runs, so a partial schema can never turn into mass deletion
//   - rows are removed in bounded batches inside individual transactions
//   - foreign key checks are disabled only for the duration, on a dedicated
//     connection, and restored afterwards
//   - DryRun performs the same counting work without writing. It reports the
//     first pass only, which is a lower bound: the cascade above is invisible
//     until rows actually disappear.
func (c *Client) CleanOrphans(ctx context.Context, opts CleanOptions) (*CleanReport, error) {
	if opts.BatchSize <= 0 {
		opts.BatchSize = 1000
	}
	maxPasses := opts.MaxPasses
	if maxPasses <= 0 {
		maxPasses = 5
	}

	// Pre-flight validation: refuse to start if the catalog itself is absent.
	n, err := c.CountRows(ctx, "catalog_product_entity")
	if err != nil {
		return nil, err
	}
	if n < 0 {
		return nil, fmt.Errorf("table catalog_product_entity is missing: this does not look like a Magento 2 catalog database")
	}

	targets := orphanTargets()
	rep := &CleanReport{DryRun: opts.DryRun}

	if opts.DryRun {
		for _, t := range targets {
			res, err := c.countForTable(ctx, t)
			if err != nil {
				return nil, err
			}
			rep.Results = append(rep.Results, res)
			rep.TotalDeleted += res.Deleted
		}
		return rep, nil
	}

	// Accumulate per table so the report stays one row per table no matter
	// how many sweeps ran.
	acc := make(map[string]*CleanTableResult, len(targets))
	for _, t := range targets {
		acc[t.Table] = &CleanTableResult{Table: t.Table, Description: t.Description}
	}

	// A dedicated connection keeps FOREIGN_KEY_CHECKS scoped to this work.
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=0"); err != nil {
		return nil, fmt.Errorf("disable foreign key checks: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SET FOREIGN_KEY_CHECKS=1")
	}()

	for pass := 0; pass < maxPasses; pass++ {
		var passDeleted int64
		for _, t := range targets {
			a := acc[t.Table]
			res, err := c.cleanTable(ctx, conn, t, opts, a.Deleted)
			if err != nil {
				// Return what we have so far so operators can see progress.
				return rep, fmt.Errorf("clean %s: %w", t.Table, err)
			}
			if res.Skipped != "" && a.Skipped == "" {
				a.Skipped = res.Skipped
			}
			a.Deleted += res.Deleted
			passDeleted += res.Deleted
		}
		if passDeleted == 0 {
			break
		}
	}

	for _, t := range targets {
		a := acc[t.Table]
		rep.Results = append(rep.Results, *a)
		rep.TotalDeleted += a.Deleted
	}
	return rep, nil
}

func (c *Client) countForTable(ctx context.Context, t orphanTarget) (CleanTableResult, error) {
	res := CleanTableResult{Table: t.Table, Description: t.Description}
	ok, err := c.TableExists(ctx, t.Table)
	if err != nil {
		return res, err
	}
	if !ok {
		res.Skipped = "table not present"
		return res, nil
	}
	refOK, err := c.TableExists(ctx, t.RefTable)
	if err != nil {
		return res, err
	}
	if !refOK {
		res.Skipped = fmt.Sprintf("reference table %s not present", t.RefTable)
		return res, nil
	}
	n, err := c.countOrphans(ctx, t)
	if err != nil {
		return res, err
	}
	res.Deleted = n
	return res, nil
}

// cleanTable deletes every orphaned row of one table in bounded batches.
// base is the number of rows already deleted from this table by earlier
// sweeps, so progress reports a cumulative figure.
func (c *Client) cleanTable(ctx context.Context, conn *sql.Conn, t orphanTarget, opts CleanOptions, base int64) (CleanTableResult, error) {
	res := CleanTableResult{Table: t.Table, Description: t.Description}

	ok, err := c.TableExists(ctx, t.Table)
	if err != nil {
		return res, err
	}
	if !ok {
		res.Skipped = "table not present"
		return res, nil
	}
	refOK, err := c.TableExists(ctx, t.RefTable)
	if err != nil {
		return res, err
	}
	if !refOK {
		res.Skipped = fmt.Sprintf("reference table %s not present", t.RefTable)
		return res, nil
	}

	for {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		keys, err := c.selectOrphanKeys(ctx, conn, t, opts.BatchSize)
		if err != nil {
			return res, err
		}
		if len(keys) == 0 {
			return res, nil
		}
		affected, err := c.deleteKeys(ctx, conn, t, keys)
		if err != nil {
			return res, err
		}
		res.Deleted += affected
		if opts.Progress != nil {
			opts.Progress(t.Table, base+res.Deleted)
		}
		// Guard against an unexpected no-op delete turning into a busy loop.
		if affected == 0 {
			return res, nil
		}
	}
}

// selectOrphanKeys fetches the primary key columns of up to limit orphaned rows.
func (c *Client) selectOrphanKeys(ctx context.Context, conn *sql.Conn, t orphanTarget, limit int) ([][]any, error) {
	cols := make([]string, len(t.PkCols))
	for i, col := range t.PkCols {
		cols[i] = "t.`" + col + "`"
	}
	q := "SELECT " + strings.Join(cols, ", ") +
		" FROM `" + c.Table(t.Table) + "` t" +
		" LEFT JOIN `" + c.Table(t.RefTable) + "` r ON r.`" + t.RefKey + "` = t.`" + t.RefCol + "`" +
		" WHERE r.`" + t.RefKey + "` IS NULL LIMIT " + strconv.Itoa(limit)

	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out [][]any
	for rows.Next() {
		vals := make([]any, len(t.PkCols))
		ptrs := make([]any, len(t.PkCols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		out = append(out, vals)
	}
	return out, rows.Err()
}

// deleteKeys removes exactly the rows whose primary keys were selected.
func (c *Client) deleteKeys(ctx context.Context, conn *sql.Conn, t orphanTarget, keys [][]any) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	table, err := quoteIdent(c.Table(t.Table))
	if err != nil {
		return 0, err
	}
	colList := make([]string, len(t.PkCols))
	for i, col := range t.PkCols {
		colList[i] = "`" + col + "`"
	}

	var sb strings.Builder
	sb.WriteString("DELETE FROM ")
	sb.WriteString(table)
	sb.WriteString(" WHERE (")
	sb.WriteString(strings.Join(colList, ", "))
	sb.WriteString(") IN (")
	args := make([]any, 0, len(keys)*len(t.PkCols))
	for i, row := range keys {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(")
		for j := range row {
			if j > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("?")
			args = append(args, row[j])
		}
		sb.WriteString(")")
	}
	sb.WriteString(")")

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, sb.String(), args...)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return affected, nil
}
