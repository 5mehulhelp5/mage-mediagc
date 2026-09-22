package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/shuaiZend/mage-mediagc/internal/action"
	"github.com/shuaiZend/mage-mediagc/internal/media"
)

func newVerifyCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Re-check the media tree and database after a cleanup",
		Long: `verify runs the analysis again and prints a checklist, which is the quickest
way to confirm that a cleanup did what you expected and did not break
anything.

Expected healthy state after a full cleanup:
  orphaned files     0
  missing files      unchanged (cleanup never touches referenced files)
  derived cache      small, growing as visitors browse
  database orphans   0`,
		Example: `  mage-mediagc verify`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			res, client, err := a.runAnalysis(ctx, analyzeOptions{showProgress: !a.cfg.Output.Quiet})
			if err != nil {
				return err
			}
			defer client.Close()

			dbRep, err := client.OrphanStats(ctx, nil)
			if err != nil {
				return fmt.Errorf("database orphan statistics: %w", err)
			}

			cacheFiles, cacheBytes := res.CacheFiles, res.CacheBytes
			if cacheFiles == 0 {
				if f, b, err := action.CacheStats(ctx, a.cfg.Magento.MediaPath); err == nil {
					cacheFiles, cacheBytes = f, b
				}
			}

			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(tw, "check\tvalue\tstatus\n")
			fmt.Fprintf(tw, "media root\t%s\t\n", res.MediaRoot)
			fmt.Fprintf(tw, "original files\t%d\t%s\n", res.DiskFiles, media.HumanBytes(res.DiskBytes))
			fmt.Fprintf(tw, "referenced files\t%d\t%s\n", res.LiveFiles, media.HumanBytes(res.LiveBytes))
			fmt.Fprintf(tw, "orphaned files\t%d\t%s\n", res.OrphanFiles, statusIcon(res.OrphanFiles == 0))
			fmt.Fprintf(tw, "missing files\t%d\t%s\n", len(res.Missing), statusIcon(len(res.Missing) == 0))
			fmt.Fprintf(tw, "derived cache\t%d\t%s\n", cacheFiles, media.HumanBytes(cacheBytes))
			fmt.Fprintf(tw, "products\t%d\t\n", dbRep.ProductCount)
			fmt.Fprintf(tw, "database orphan rows\t%d\t%s\n", dbRep.TotalOrphans, statusIcon(dbRep.TotalOrphans == 0))
			_ = tw.Flush()

			if len(res.Warnings) > 0 {
				fmt.Fprintln(a.stdout)
				for _, w := range res.Warnings {
					fmt.Fprintf(a.stdout, "! %s\n", w)
				}
			}

			fmt.Fprintln(a.stdout)
			reclaim := res.OrphanBytes + cacheBytes
			if res.OrphanFiles == 0 && dbRep.TotalOrphans == 0 {
				fmt.Fprintf(a.stdout, "clean: no orphaned files or database rows\n")
			} else {
				fmt.Fprintf(a.stdout, "outstanding: %d orphaned files (%s of recoverable media, plus cache)\n",
					res.OrphanFiles, media.HumanBytes(reclaim))
				fmt.Fprintf(a.stdout, "see `mage-mediagc scan` for the breakdown\n")
			}
			return nil
		},
	}
	return cmd
}

func statusIcon(ok bool) string {
	if ok {
		return "ok"
	}
	return "action needed"
}
