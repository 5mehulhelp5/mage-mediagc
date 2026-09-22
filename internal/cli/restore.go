package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shuaiZend/mage-mediagc/internal/action"
	"github.com/shuaiZend/mage-mediagc/internal/media"
)

func newRestoreCmd(a *app) *cobra.Command {
	var (
		apply         bool
		quarantineDir string
		only          []string
	)
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Move quarantined files back to their original locations",
		Long: `restore reads the manifest written by quarantine and moves every recorded file
back into the media tree.

Files that already exist at the destination are skipped rather than
overwritten, because a newer upload at the same path should win. If some files
cannot be restored, the manifest is rewritten to keep only those, so the
command can simply be run again.`,
		Example: `  mage-mediagc restore                 # what would come back
  mage-mediagc restore --apply         # put everything back
  mage-mediagc restore --apply --only h/-/a.jpg,s/-/b.jpg`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := quarantineDir
			if dir == "" {
				dir = a.cfg.Cleanup.QuarantineDir
			}
			if dir == "" {
				return fmt.Errorf("no quarantine directory: pass --quarantine-dir or set cleanup.quarantineDir")
			}

			var onlyPaths []string
			for _, item := range only {
				for _, p := range strings.Split(item, ",") {
					p = strings.TrimSpace(p)
					if p != "" {
						onlyPaths = append(onlyPaths, p)
					}
				}
			}

			res, err := action.Restore(cmd.Context(), action.RestoreOptions{
				QuarantineDir: dir,
				MediaRoot:     a.cfg.Magento.MediaPath,
				DryRun:        !apply,
				Parallel:      a.cfg.Cleanup.Parallel,
				OnlyPaths:     onlyPaths,
			})
			if err != nil {
				return err
			}

			if !apply {
				fmt.Fprintf(a.stdout, "dry run: %d entries in %s, %s would be restored\n",
					res.Entries, dir, media.HumanBytes(res.Bytes))
				return nil
			}

			fmt.Fprintf(a.stdout, "restore from %s\n", dir)
			fmt.Fprintf(a.stdout, "  candidates : %d\n", res.Entries)
			fmt.Fprintf(a.stdout, "  restored   : %d (%s)\n", res.Restored, media.HumanBytes(res.Bytes))
			if res.Skipped > 0 {
				fmt.Fprintf(a.stdout, "  skipped    : %d (destination already exists)\n", res.Skipped)
			}
			if res.Failed > 0 {
				fmt.Fprintf(a.stdout, "  failed     : %d\n", res.Failed)
				for _, f := range res.Failures {
					fmt.Fprintf(a.stdout, "    %s\n", f)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "perform the restore (default is a dry run)")
	cmd.Flags().StringVar(&quarantineDir, "quarantine-dir", "", "holding directory to restore from")
	cmd.Flags().StringSliceVar(&only, "only", nil, "restore only these relative paths (comma separated, repeatable)")
	return cmd
}

func newPurgeCmd(a *app) *cobra.Command {
	var (
		apply         bool
		quarantineDir string
		force         bool
	)
	cmd := &cobra.Command{
		Use:   "purge",
		Short: "Permanently delete a quarantine directory to free disk space",
		Long: `purge removes the contents of the holding directory. This is the point of no
return: quarantining only reserves space by moving files out of the media
tree, and the bytes are not actually freed until the holding directory is
deleted.

As a guard, purge refuses to delete a directory that holds files but no
mage-mediagc manifest, which is the signature of pointing it at the wrong
path. Use --force to override.

Deleting the directory does not remove the orphaned rows from the database;
run db-clean for that.`,
		Example: `  mage-mediagc purge                  # report what would be freed
  mage-mediagc purge --apply          # free the space`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := quarantineDir
			if dir == "" {
				dir = a.cfg.Cleanup.QuarantineDir
			}
			if dir == "" {
				return fmt.Errorf("no quarantine directory: pass --quarantine-dir or set cleanup.quarantineDir")
			}

			res, err := action.Purge(cmd.Context(), action.PurgeOptions{
				QuarantineDir: dir,
				DryRun:        !apply,
				Force:         force,
			})
			if err != nil {
				return err
			}

			if !apply {
				fmt.Fprintf(a.stdout, "dry run: %s holds %d files (%s)\n",
					dir, res.Removed, media.HumanBytes(res.Bytes))
				fmt.Fprintf(a.stdout, "\nrun again with --apply to free the space\n")
				return nil
			}
			if res.Removed == 0 {
				fmt.Fprintf(a.stdout, "nothing to purge in %s\n", dir)
				return nil
			}
			fmt.Fprintf(a.stdout, "purged %s: %d files, %s freed\n",
				dir, res.Removed, media.HumanBytes(res.Bytes))
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "perform the deletion (default is a dry run)")
	cmd.Flags().StringVar(&quarantineDir, "quarantine-dir", "", "holding directory to delete")
	cmd.Flags().BoolVar(&force, "force", false, "delete even without a mage-mediagc manifest")
	return cmd
}
