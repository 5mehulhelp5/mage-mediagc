package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/shuaiZend/mage-mediagc/internal/magento"
)

func newDBCleanCmd(a *app) *cobra.Command {
	var (
		apply     bool
		batchSize int
	)
	cmd := &cobra.Command{
		Use:   "db-clean",
		Short: "Remove database rows that point at deleted products",
		Long: `db-clean removes the EAV and gallery rows that survive when products are
deleted outside the admin UI (bulk imports, direct SQL, failed migrations).

A mature shop accumulates these quickly: a catalog of a few thousand live
products routinely carries hundreds of thousands of orphaned rows. They slow
backups, bloat mysqldump output and inflate the flat tables.

Scope: only rows whose referenced product or gallery entry is missing is
removed. Rows are deleted in bounded batches, each inside its own
transaction, on a dedicated connection with foreign key checks disabled and
restored afterwards.

Removing a row can orphan rows elsewhere (a product's last gallery link
orphans the gallery entry, which orphans its per-store values), so the whole
ordered list is swept repeatedly until a sweep changes nothing.

Without --apply nothing is deleted. Always run a backup first:

  mysqldump --single-transaction <db> > pre-db-clean.sql`,
		Example: `  mage-mediagc db-clean               # count only
  mage-mediagc db-clean --apply       # delete, 1000 rows per statement
  mage-mediagc db-clean --apply --batch-size 5000`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if err := a.cfg.Validate(false); err != nil {
				return err
			}
			client, err := a.openDB(ctx)
			if err != nil {
				return err
			}
			defer client.Close()

			if batchSize > 0 {
				a.cfg.Cleanup.BatchSize = batchSize
			}

			progress := func(table string, deleted int64) {
				if !a.cfg.Output.Quiet {
					fmt.Fprintf(a.stderr, "\r  %s: %d rows removed", table, deleted)
				}
			}

			rep, err := client.CleanOrphans(ctx, magento.CleanOptions{
				DryRun:    !apply,
				BatchSize: a.cfg.Cleanup.BatchSize,
				Progress:  progress,
			})
			if err != nil {
				return err
			}
			if !a.cfg.Output.Quiet && apply {
				fmt.Fprintln(a.stderr)
			}

			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(tw, "table\trows\tstatus\n")
			for _, r := range rep.Results {
				status := "removed"
				if !apply {
					status = "would remove"
				}
				if r.Skipped != "" {
					fmt.Fprintf(tw, "%s\t-\t%s\n", r.Table, r.Skipped)
					continue
				}
				if r.Deleted == 0 {
					fmt.Fprintf(tw, "%s\t0\tclean\n", r.Table)
					continue
				}
				fmt.Fprintf(tw, "%s\t%d\t%s\n", r.Table, r.Deleted, status)
			}
			_ = tw.Flush()

			fmt.Fprintln(a.stdout)
			if apply {
				fmt.Fprintf(a.stdout, "total rows removed: %d\n", rep.TotalDeleted)
				fmt.Fprintf(a.stdout, "\nnow rebuild the indexes:\n")
				fmt.Fprintf(a.stdout, "  php bin/magento indexer:reindex\n")
				fmt.Fprintf(a.stdout, "  php bin/magento cache:flush\n")
			} else if rep.TotalDeleted > 0 {
				fmt.Fprintf(a.stdout, "total rows that would be removed: %d\n", rep.TotalDeleted)
				fmt.Fprintf(a.stdout, "\nthis counts the first pass only; cascading orphans add more\n")
				fmt.Fprintf(a.stdout, "run again with --apply to delete them\n")
			} else {
				fmt.Fprintf(a.stdout, "database is clean: no orphaned rows found\n")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "perform the deletion (default is a dry run)")
	cmd.Flags().IntVar(&batchSize, "batch-size", 0, "rows per DELETE statement (default from config, usually 1000)")
	return cmd
}
