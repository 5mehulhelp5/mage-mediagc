package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/shuaiZend/mage-mediagc/internal/action"
	"github.com/shuaiZend/mage-mediagc/internal/media"
	"github.com/shuaiZend/mage-mediagc/internal/version"
)

func newQuarantineCmd(a *app) *cobra.Command {
	var (
		apply            bool
		quarantineDir    string
		allowCrossDevice bool
		force            bool
		maxFraction      float64
	)
	cmd := &cobra.Command{
		Use:   "quarantine",
		Short: "Move orphaned originals into a holding directory (reversible)",
		Long: `quarantine moves every file that no reference keeps alive into a holding
directory, preserving the original directory structure.

Files are moved, not copied. On the same filesystem a move is an inode
operation, so isolating hundreds of thousands of files takes seconds and uses
no extra space. The run refuses to cross filesystems for that reason.

A manifest is written into the holding directory, so restore can put every
file back exactly where it came from.

Safety rails:
  - at least one file must look orphaned, otherwise nothing happens
  - the orphan ratio is compared against --max-fraction; exceeding it aborts
    the run unless --force is given, because a ratio that extreme usually
    means reference collection failed rather than that the shop really is
    that full of garbage
  - without --apply nothing is moved

--max-fraction and --allow-cross-device default to cleanup.maxDeleteFraction
and cleanup.allowCrossDevice from the config file.`,
		Example: `  # Measure first
  mage-mediagc quarantine

  # Review the ratio, then move
  mage-mediagc quarantine --apply

  # Keep the holding directory on a specific volume
  mage-mediagc quarantine --apply --quarantine-dir /data/media-quarantine`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			res, client, err := a.runAnalysis(ctx, analyzeOptions{showProgress: !a.cfg.Output.Quiet})
			if err != nil {
				return err
			}
			defer client.Close()

			if res.DiskFiles == 0 {
				return fmt.Errorf("no files found under %s: nothing to quarantine", a.cfg.Magento.MediaPath)
			}
			if res.OrphanFiles == 0 {
				fmt.Fprintf(a.stdout, "no orphaned files found (%d files checked)\n", res.DiskFiles)
				return nil
			}

			fraction := float64(res.OrphanFiles) / float64(res.DiskFiles)

			// The threshold lives in the config file so a fleet can set one
			// policy; the flag overrides it for ad-hoc runs.
			threshold := a.cfg.Cleanup.MaxDeleteFraction
			if cmd.Flags().Changed("max-fraction") {
				threshold = maxFraction
			}
			if fraction > threshold && !force {
				return fmt.Errorf(
					"refusing to quarantine: %.1f%% of files look orphaned, above the %.1f%% safety threshold.\n"+
						"This usually means a reference source failed to load. Re-run `mage-mediagc scan -v` to inspect\n"+
						"the per-source counts, then pass --force (or raise --max-fraction) once you are satisfied",
					fraction*100, threshold*100)
			}

			dir := quarantineDir
			if dir == "" {
				dir = a.cfg.Cleanup.QuarantineDir
			}
			if dir == "" {
				return fmt.Errorf("no quarantine directory: pass --quarantine-dir or set cleanup.quarantineDir")
			}

			progress := func(done, total, bytes int64) {
				if !a.cfg.Output.Quiet {
					fmt.Fprintf(a.stderr, "\r  moved %d/%d (%s)", done, total, media.HumanBytes(bytes))
				}
			}

			out, err := action.Quarantine(ctx, action.QuarantineOptions{
				MediaRoot:        a.cfg.Magento.MediaPath,
				QuarantineDir:    dir,
				Orphans:          res.Orphans,
				DryRun:           !apply,
				Parallel:         a.cfg.Cleanup.Parallel,
				AllowCrossDevice: allowCrossDevice || a.cfg.Cleanup.AllowCrossDevice,
				ToolVersion:      version.Version,
				Progress:         progress,
			})
			if err != nil {
				return err
			}
			if !a.cfg.Output.Quiet && apply {
				fmt.Fprintln(a.stderr)
			}

			if !apply {
				fmt.Fprintf(a.stdout, "dry run: would move %d files (%s) into %s\n",
					out.Requested, media.HumanBytes(out.Bytes), dir)
				fmt.Fprintf(a.stdout, "  orphan ratio : %.1f%% of %d files\n", fraction*100, res.DiskFiles)
				fmt.Fprintf(a.stdout, "\nrun again with --apply to move them\n")
				return nil
			}

			fmt.Fprintf(a.stdout, "quarantined %d files (%s) into %s\n",
				out.Moved, media.HumanBytes(out.Bytes), dir)
			fmt.Fprintf(a.stdout, "  manifest     : %s\n", out.ManifestPath)
			fmt.Fprintf(a.stdout, "  duration     : %s\n", out.Duration.Round(1e6))
			if out.Failed > 0 {
				fmt.Fprintf(a.stdout, "  failed       : %d (see --format json for details)\n", out.Failed)
				for _, f := range out.Failures {
					fmt.Fprintf(a.stdout, "    %s\n", f)
				}
			}
			fmt.Fprintf(a.stdout, "\nnext steps:\n")
			fmt.Fprintf(a.stdout, "  verify the shop, then run `mage-mediagc purge --apply` to free the space\n")
			fmt.Fprintf(a.stdout, "  or roll back with `mage-mediagc restore --apply`\n")
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "perform the move (default is a dry run)")
	cmd.Flags().StringVar(&quarantineDir, "quarantine-dir", "",
		"holding directory for isolated files (must be on the same filesystem)")
	cmd.Flags().BoolVar(&allowCrossDevice, "allow-cross-device", false,
		"permit moving across filesystems (copies data and needs free space)")
	cmd.Flags().BoolVar(&force, "force", false, "ignore the orphan-ratio safety threshold")
	cmd.Flags().Float64Var(&maxFraction, "max-fraction", 0,
		"abort when the orphan ratio exceeds this value, 0-1 (default cleanup.maxDeleteFraction, 0.98)")
	return cmd
}
