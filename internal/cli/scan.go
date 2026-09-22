package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/shuaiZend/mage-mediagc/internal/config"
	"github.com/shuaiZend/mage-mediagc/internal/magento"
	"github.com/shuaiZend/mage-mediagc/internal/report"
)

func newScanCmd(a *app) *cobra.Command {
	var (
		dbOrphans bool
		outputTo  string
		topDirs   int
	)
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Analyze the media tree: orphans, missing files, cache usage and database orphans",
		Long: `scan is read-only. It indexes pub/media/catalog/product, collects every
reference that can keep an image alive, and reports the difference.

Nothing is modified. Use the reported numbers to decide whether to proceed
with cache clean, quarantine or db-clean.`,
		Example: `  # Zero-config: run from the Magento root
  mage-mediagc scan

  # JSON output for automation
  mage-mediagc scan --format json --output report.json

  # Markdown report for a ticket, with per-table database detail
  mage-mediagc scan --format markdown -vv --output media-report.md`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			res, client, err := a.runAnalysis(ctx, analyzeOptions{showProgress: !a.cfg.Output.Quiet})
			if err != nil {
				return err
			}
			defer client.Close()

			var dbRep *magento.OrphanReport
			if dbOrphans {
				if !a.cfg.Output.Quiet {
					fmt.Fprintln(a.stderr, "counting database orphans...")
				}
				rep, err := client.OrphanStats(ctx, nil)
				if err != nil {
					return fmt.Errorf("database orphan statistics: %w", err)
				}
				dbRep = rep
			}

			_, ver, commit, targetDB, mysqlVersion := a.targetInfo(ctx, client)
			payload := report.NewPayload(
				report.ToolInfo{Name: "mage-mediagc", Version: ver, Commit: commit},
				report.TargetInfo{
					MediaRoot:   a.cfg.Magento.MediaPath,
					MagentoRoot: a.cfg.Magento.Root,
					Database:    targetDB,
					MySQL:       mysqlVersion,
				},
				res,
				dbRep,
			)

			out := a.stdout
			var file *os.File
			if outputTo != "" {
				f, err := os.Create(outputTo)
				if err != nil {
					return fmt.Errorf("create output file: %w", err)
				}
				defer f.Close()
				file = f
				out = f
			}

			opts := report.Options{
				Color:    a.isColor() && file == nil,
				TopDirs:  topDirs,
				Verbose:  a.cfg.Output.Verbose,
				Language: string(a.cfg.Output.Language),
			}
			switch a.outputFormat() {
			case config.FormatJSON:
				err = report.RenderJSON(out, payload)
			case config.FormatMarkdown:
				err = report.RenderMarkdown(out, payload, opts)
			default:
				err = report.RenderTable(out, payload, opts)
			}
			if err != nil {
				return err
			}
			if file != nil && !a.cfg.Output.Quiet {
				fmt.Fprintf(a.stderr, "report written to %s\n", outputTo)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dbOrphans, "db-orphans", true, "include database orphan statistics (use --db-orphans=false to skip)")
	cmd.Flags().StringVarP(&outputTo, "output", "o", "", "write the report to a file instead of stdout")
	cmd.Flags().IntVar(&topDirs, "top-dirs", 10, "how many directory buckets to list")
	return cmd
}
