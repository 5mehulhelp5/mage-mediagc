package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/shuaiZend/mage-mediagc/internal/config"
)

func newConfigCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect the effective configuration",
		Long: `Configuration is resolved from, in increasing order of precedence:
built-in defaults, a YAML file, Magento's app/etc/env.php, MAGEGC_* environment
variables, then command-line flags.

config show reveals exactly what was resolved, which is the fastest way to
diagnose "why is it talking to the wrong database".`,
	}
	cmd.AddCommand(newConfigShowCmd(a), newConfigTemplateCmd(a))
	return cmd
}

func newConfigShowCmd(a *app) *cobra.Command {
	var asYAML bool
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the resolved configuration (passwords redacted)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.cfg.Validate(config.ModeLocal); err != nil {
				return err
			}
			if asYAML {
				out := *a.cfg
				out.DB.Password = redact(a.cfg.DB.Password)
				data, err := yaml.Marshal(&out)
				if err != nil {
					return err
				}
				fmt.Fprint(a.stdout, string(data))
				return nil
			}

			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(tw, "config file\t%s\n", orDash(a.cfg.ConfigFile))
			fmt.Fprintf(tw, "magento root\t%s\n", orDash(a.cfg.Magento.Root))
			fmt.Fprintf(tw, "media path\t%s\n", orDash(a.cfg.Magento.MediaPath))
			fmt.Fprintf(tw, "quarantine dir\t%s\n", orDash(a.cfg.Cleanup.QuarantineDir))
			fmt.Fprintf(tw, "db dsn\t%s\n", a.cfg.DB.RedactedDSN())
			fmt.Fprintf(tw, "scan workers\t%d\n", a.cfg.Scan.Workers)
			fmt.Fprintf(tw, "content refs\t%v\n", a.cfg.Scan.IncludeContentRefs)
			fmt.Fprintf(tw, "include cache\t%v\n", a.cfg.Scan.IncludeCache)
			fmt.Fprintf(tw, "exclude globs\t%s\n", orDash(strings.Join(a.cfg.Scan.ExcludeGlobs, ", ")))
			fmt.Fprintf(tw, "move parallel\t%d\n", a.cfg.Cleanup.Parallel)
			fmt.Fprintf(tw, "cross device\t%v\n", a.cfg.Cleanup.AllowCrossDevice)
			fmt.Fprintf(tw, "orphan threshold\t%.2f\n", a.cfg.Cleanup.MaxDeleteFraction)
			fmt.Fprintf(tw, "batch size\t%d\n", a.cfg.Cleanup.BatchSize)
			fmt.Fprintf(tw, "warm base url\t%s\n", orNone(a.cfg.Warm.BaseURL))
			fmt.Fprintf(tw, "warm size sets\t%d\n", len(a.cfg.Warm.CacheHashes))
			fmt.Fprintf(tw, "warm hash file\t%s\n", orDash(a.cfg.Warm.HashFile))
			fmt.Fprintf(tw, "warm concurrency\t%d\n", a.cfg.Warm.Concurrency)
			fmt.Fprintf(tw, "warm timeout\t%s\n", a.cfg.Warm.Timeout.Std())
			fmt.Fprintf(tw, "warm method\t%s\n", orNone(a.cfg.Warm.Method))
			fmt.Fprintf(tw, "warm user agent\t%s\n", userAgentForDisplay(a.cfg.Warm.UserAgent))
			fmt.Fprintf(tw, "warm max requests\t%d\n", a.cfg.Warm.MaxRequests)
			fmt.Fprintf(tw, "warm rate\t%d\n", a.cfg.Warm.Rate)
			fmt.Fprintf(tw, "warm error limit\t%.2f\n", a.cfg.Warm.MaxErrorFraction)
			fmt.Fprintf(tw, "output format\t%s\n", a.cfg.Output.Format)
			fmt.Fprintf(tw, "output language\t%s\n", a.cfg.Output.Language)
			fmt.Fprintf(tw, "verbose\t%d\n", a.cfg.Output.Verbose)
			_ = tw.Flush()
			return nil
		},
	}
	cmd.Flags().BoolVar(&asYAML, "yaml", false, "print as a YAML document instead of a table")
	return cmd
}

func newConfigTemplateCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "template",
		Short: "Print a commented YAML configuration template",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprint(a.stdout, configTemplate)
			return nil
		},
	}
}

func redact(s string) string {
	if s == "" {
		return ""
	}
	return "***"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return filepath.Clean(s)
}

// orNone is orDash for values that are not paths. filepath.Clean must not be
// applied to a URL: it collapses the "//" in "https://shop.example.com" and
// silently produces a hostless string.
func orNone(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// userAgentForDisplay resolves the empty User-Agent setting into the value the
// warm command would actually send, so that `config show` does not answer the
// question "what will my traffic look like?" with a dash.
func userAgentForDisplay(configured string) string {
	if configured == "" {
		return defaultUserAgent() + "  (built-in default)"
	}
	return configured
}

const configTemplate = `# mage-mediagc configuration
# All values are optional: anything omitted is taken from Magento's
# app/etc/env.php when it can be found, and from built-in defaults otherwise.

magento:
  # Installation root, i.e. the directory containing app/etc/env.php.
  # Auto-detected by walking up from the working directory.
  root: /var/www/magento
  # Directory holding the original catalog images.
  # Defaults to <root>/pub/media/catalog/product
  mediaPath: ""

database:
  # Leave host/port/name/user/password empty to read them from env.php.
  host: ""
  port: 0
  name: ""
  user: ""
  password: ""
  # Unix socket takes precedence over host/port when set.
  socket: ""
  charset: utf8mb4

scan:
  # Parallel workers. 0 means auto (number of CPUs, capped at 16).
  workers: 0
  # Parse product descriptions and CMS content for embedded images.
  # Disabling this is faster but will report images used only in page copy
  # as orphans. Keep it enabled unless you know your content is clean.
  includeContentRefs: true
  # Index the derived cache as regular files (almost never wanted).
  includeCache: false
  # Skip entries whose path relative to mediaPath, or whose base name, matches
  # one of these shell patterns. A matching directory is skipped whole.
  excludeGlobs: []

cleanup:
  # Holding directory for quarantined files. Must be on the same filesystem
  # as the media path, otherwise moves become copies.
  quarantineDir: ""
  # Rows deleted per statement during db-clean.
  batchSize: 1000
  # Parallel file moves during quarantine/restore. 0 means auto.
  parallel: 0
  # Allow moving across filesystems (needs free space for a full copy).
  allowCrossDevice: false
  # Abort quarantine when the orphan ratio exceeds this value (0-1].
  maxDeleteFraction: 0.98

warm:
  # Storefront URL the cache URLs are resolved against, e.g.
  # https://shop.example.com (no trailing slash needed, no path prefix unless
  # the store really is served from one). Leave empty to read
  # web/secure/base_url, then web/unsecure/base_url, from core_config_data —
  # which means the run needs database access. Setting it explicitly keeps
  # cache warm usable on a host with no database at all.
  #
  # Point this at the origin, not at a CDN: a CDN that already holds the image
  # will answer without the origin ever generating it.
  baseUrl: ""
  # Size sets to warm, as hex hashes. Leave empty: they are discovered from
  # media/catalog/product/cache/ (or read from hashFile). Only set this to
  # pin a specific set, for example to re-warm one after a theme change.
  cacheHashes: []
  # Snapshot of the size sets, written by "cache clean --save-hashes <file>"
  # before the cache is emptied. After a clean, nothing on the host remembers
  # which size sets the theme asks for, and discovery has nothing left to look
  # at.
  hashFile: ""
  # Requests in flight. Keep it modest: every request makes the storefront
  # resize an image on the same PHP workers that serve visitors.
  concurrency: 4
  # Timeout for one request.
  timeout: 30s
  # get downloads the variant, head asks for headers only. Prefer get: a proxy
  # or CDN may answer a head from the edge without generating anything, which
  # looks like success while writing no file.
  method: get
  # User-Agent header. Empty means a built-in default that names and versions
  # the tool, so your traffic is identifiable in the shop's access log.
  userAgent: ""
  # Stop after this many requests in a single run (0 = no limit). Useful to
  # warm a large catalog in slices, or to measure throughput before committing
  # to the rest.
  maxRequests: 0
  # Cap on request starts per second across all workers (0 = no limit). The
  # polite setting for a shop that is live and busy.
  rate: 0
  # Abort when the share of failed requests exceeds this (0-1].
  maxErrorFraction: 0.05

output:
  # table, json or markdown
  format: table
  # Report language: en or zh. Reports only; logs, errors and JSON are always
  # in English so automation reads the same whoever ran the tool.
  language: en
  # 0 = normal, 1 = per-source detail, 2 = everything
  verbose: 0
  quiet: false
`
