// Package cli wires the command surface together.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/shuaiZend/mage-mediagc/internal/config"
	"github.com/shuaiZend/mage-mediagc/internal/magento"
	"github.com/shuaiZend/mage-mediagc/internal/version"
)

// app holds flag values and the resolved configuration shared by all commands.
type app struct {
	// global flags
	cfgFile       string
	magentoRoot   string
	mediaPath     string
	dbHost        string
	dbPort        int
	dbName        string
	dbUser        string
	dbPassword    string
	dbSocket      string
	workers       int
	format        string
	language      string
	verbose       int
	quiet         bool
	noContentRefs bool

	// resolved
	cfg *config.Config

	stdout io.Writer
	stderr io.Writer
}

// NewRootCmd builds the command tree.
func NewRootCmd() *cobra.Command {
	a := &app{stdout: os.Stdout, stderr: os.Stderr}

	root := &cobra.Command{
		Use:   "mage-mediagc",
		Short: "Find and safely remove orphaned Magento 2 media files and stale EAV rows",
		Long: `mage-mediagc is a standalone garbage collector for Magento 2 catalog media.

It indexes the files under pub/media/catalog/product, collects every reference
that can keep an image alive, and reports the difference. Cleaning is split
into independently reversible stages:

  cache clean   drop the derived thumbnail cache        (zero risk, auto-rebuilt)
  quarantine    move orphaned originals to a holding dir (reversible)
  purge         delete the holding dir once verified     (frees space)
  db-clean      remove rows pointing at deleted products

Unlike the Magento modules it replaces, mage-mediagc is a single static
binary: nothing is installed into the shop, and it never deletes an original
image without an explicit second step.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			// `version` and `help` must work without any configuration at all.
			if cmd.Name() == "version" || cmd.Name() == "help" {
				return nil
			}
			return a.loadConfig(cmd)
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&a.cfgFile, "config", "", "path to YAML config file (default: ./mage-mediagc.yaml)")
	pf.StringVar(&a.magentoRoot, "magento-root", "", "Magento installation root (contains app/etc/env.php)")
	pf.StringVar(&a.mediaPath, "media-path", "", "path to pub/media/catalog/product")
	pf.StringVar(&a.dbHost, "db-host", "", "MySQL host (default from env.php)")
	pf.IntVar(&a.dbPort, "db-port", 0, "MySQL port (default from env.php)")
	pf.StringVar(&a.dbName, "db-name", "", "database name (default from env.php)")
	pf.StringVar(&a.dbUser, "db-user", "", "database user (default from env.php)")
	pf.StringVar(&a.dbPassword, "db-password", "", "database password (default from env.php)")
	pf.StringVar(&a.dbSocket, "db-socket", "", "MySQL unix socket path (overrides host/port)")
	pf.IntVar(&a.workers, "workers", 0, "parallel workers for scanning and file moves (0 = auto)")
	pf.StringVarP(&a.format, "format", "f", "", "output format: table, json, markdown")
	pf.StringVar(&a.language, "language", "", "report language: en, zh")
	pf.CountVarP(&a.verbose, "verbose", "v", "increase output detail (repeatable)")
	pf.BoolVarP(&a.quiet, "quiet", "q", false, "suppress progress output")
	pf.BoolVar(&a.noContentRefs, "no-content-refs", false,
		"skip parsing product descriptions and CMS content for image references (faster, riskier)")

	root.AddCommand(
		newScanCmd(a),
		newListCmd(a),
		newCacheCmd(a),
		newQuarantineCmd(a),
		newRestoreCmd(a),
		newPurgeCmd(a),
		newDBCleanCmd(a),
		newVerifyCmd(a),
		newConfigCmd(a),
		newVersionCmd(a),
	)
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := NewRootCmd().ExecuteContext(ctx); err != nil {
		msg := err.Error()
		if !strings.HasPrefix(msg, "exit status") {
			fmt.Fprintf(os.Stderr, "error: %s\n", msg)
		}
		return 1
	}
	return 0
}

// loadConfig merges configuration sources, respecting which flags the user
// actually typed (as opposed to flags that merely have default values).
func (a *app) loadConfig(cmd *cobra.Command) error {
	flags := cmd.Flags()
	ov := config.Overrides{ConfigFile: a.cfgFile}

	if flags.Changed("magento-root") {
		ov.MagentoRoot = &a.magentoRoot
	}
	if flags.Changed("media-path") {
		ov.MediaPath = &a.mediaPath
	}
	if flags.Changed("db-host") {
		ov.DBHost = &a.dbHost
	}
	if flags.Changed("db-port") {
		ov.DBPort = &a.dbPort
	}
	if flags.Changed("db-name") {
		ov.DBName = &a.dbName
	}
	if flags.Changed("db-user") {
		ov.DBUser = &a.dbUser
	}
	if flags.Changed("db-password") {
		ov.DBPassword = &a.dbPassword
	}
	if flags.Changed("db-socket") {
		ov.DBSocket = &a.dbSocket
	}
	if flags.Changed("workers") {
		ov.Workers = &a.workers
	}
	if flags.Changed("format") {
		ov.Format = &a.format
	}
	if flags.Changed("language") {
		ov.Language = &a.language
	}
	if flags.Changed("verbose") {
		ov.Verbose = &a.verbose
	}
	if flags.Changed("quiet") {
		ov.Quiet = &a.quiet
	}
	if flags.Changed("no-content-refs") {
		refs := !a.noContentRefs
		ov.ContentRefs = &refs
	}

	cfg, err := config.Load(ov)
	if err != nil {
		return err
	}
	// Keep quarantine/parallel settings aligned with whatever the user asked
	// for at the top level.
	if a.workers > 0 {
		cfg.Cleanup.Parallel = a.workers
	}
	a.cfg = cfg
	return nil
}

// openDB connects to MySQL and returns a client.
func (a *app) openDB(ctx context.Context) (*magento.Client, error) {
	if a.cfg == nil {
		return nil, fmt.Errorf("configuration not loaded")
	}
	prefix := ""
	if env, err := config.LoadEnvPHP(a.cfg.Magento.Root); err == nil {
		prefix = env.TablePrefix
	}
	return magento.Open(ctx, a.cfg.DB, prefix)
}

// isColor reports whether styled output should be emitted.
func (a *app) isColor() bool {
	f, ok := a.stdout.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// outputFormat resolves the effective output format, falling back to table.
func (a *app) outputFormat() config.OutputFormat {
	if a.cfg != nil && a.cfg.Output.Format != "" {
		return a.cfg.Output.Format
	}
	return config.FormatTable
}

// toolInfo describes the running binary for reports.
func (a *app) toolInfo() (name, ver, commit string) {
	return "mage-mediagc", version.Version, version.Commit
}

// progressPrinter renders an in-place progress line unless output is quiet or
// not attached to a terminal.
func (a *app) progressPrinter(label string) func(done, total int64, extra ...string) {
	if a.cfg != nil && (a.cfg.Output.Quiet || !a.isColor()) {
		return func(int64, int64, ...string) {}
	}
	return func(done, total int64, extra ...string) {
		line := fmt.Sprintf("\r%s %d", label, done)
		if total > 0 {
			line += fmt.Sprintf("/%d (%.0f%%)", total, float64(done)/float64(total)*100)
		}
		if len(extra) > 0 && extra[0] != "" {
			line += " " + extra[0]
		}
		fmt.Fprint(a.stderr, line+"\x1b[K")
		if total > 0 && done >= total {
			fmt.Fprintln(a.stderr)
		}
	}
}
