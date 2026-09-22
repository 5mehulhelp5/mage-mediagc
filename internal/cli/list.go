package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newListCmd(a *app) *cobra.Command {
	var (
		kind   string
		output string
		limit  int
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Print plain file lists, one path per line",
		Long: `list emits bare paths, which makes the output trivial to feed to other tools:

  mage-mediagc list --kind orphan --output orphans.txt
  rsync -a --files-from=orphans.txt --relative \
    ./pub/media/catalog/product/ /backup/orphans/`,
		Example: `  mage-mediagc list --kind orphan --output orphans.txt
  mage-mediagc list --kind missing
  mage-mediagc list --kind live --output keep.txt`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			res, client, err := a.runAnalysis(ctx, analyzeOptions{showProgress: false})
			if err != nil {
				return err
			}
			defer client.Close()

			out := a.stdout
			var file *os.File
			if output != "" {
				if dir := filepath.Dir(output); dir != "" && dir != "." {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						return fmt.Errorf("create output directory: %w", err)
					}
				}
				f, err := os.Create(output)
				if err != nil {
					return fmt.Errorf("create %s: %w", output, err)
				}
				defer f.Close()
				file = f
				out = f
			}
			w := bufio.NewWriterSize(out, 1<<20)

			var written int64
			emit := func(p string) error {
				if limit > 0 && written >= int64(limit) {
					return nil
				}
				if _, err := w.WriteString(p + "\n"); err != nil {
					return err
				}
				written++
				return nil
			}

			switch kind {
			case "orphan", "":
				for _, f := range res.Orphans {
					if err := emit(f.RelPath); err != nil {
						return err
					}
				}
			case "live":
				orphanSet := res.OrphanSet()
				for _, f := range res.Scan().Files {
					if _, isOrphan := orphanSet[f.RelPath]; isOrphan {
						continue
					}
					if err := emit(f.RelPath); err != nil {
						return err
					}
				}
			case "missing":
				for _, p := range res.Missing {
					if err := emit(p); err != nil {
						return err
					}
				}
			default:
				return fmt.Errorf("unknown --kind %q (want orphan, live or missing)", kind)
			}

			if err := w.Flush(); err != nil {
				return err
			}
			if file != nil {
				fmt.Fprintf(a.stderr, "wrote %d paths to %s\n", written, output)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&kind, "kind", "k", "orphan", "what to list: orphan, live or missing")
	cmd.Flags().StringVarP(&output, "output", "o", "", "write to a file instead of stdout")
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum number of paths to print (0 = all)")
	return cmd
}
