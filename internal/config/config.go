// Package config resolves runtime configuration from four sources, later
// sources overriding earlier ones:
//
//  1. built-in defaults
//  2. a YAML config file (--config)
//  3. Magento's app/etc/env.php, used only to fill gaps
//  4. command-line flags and MAGEGC_* environment variables
//
// The design goal is that a plain `mage-mediagc scan` executed from a Magento
// root works with zero configuration, while every derived value stays
// explicitly overridable for unusual deployments.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// OutputFormat enumerates supported report encodings.
type OutputFormat string

const (
	FormatTable    OutputFormat = "table"
	FormatJSON     OutputFormat = "json"
	FormatMarkdown OutputFormat = "markdown"
)

// Language selects the report language. Reports are the only localized
// surface; logs, errors and the JSON payload stay English so automation and
// issue reports read the same whoever ran the tool.
type Language string

const (
	LanguageEnglish Language = "en"
	LanguageChinese Language = "zh"
)

// SupportedLanguages lists the accepted values of Output.Language.
//
// The rendering side keys its message tables off these same tags; a test in
// internal/report asserts the two lists cannot drift apart.
var SupportedLanguages = []Language{LanguageEnglish, LanguageChinese}

// DB holds MySQL connection settings.
type DB struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Name     string `yaml:"name"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Socket   string `yaml:"socket"`
	Charset  string `yaml:"charset"`
}

// Magento holds paths of the target installation.
type Magento struct {
	// Root is the Magento installation root (the directory holding app/etc/env.php).
	Root string `yaml:"root"`
	// MediaPath is the directory holding original catalog images.
	MediaPath string `yaml:"mediaPath"`
	// MediaBase is the pub/media directory; derived from MediaPath.
	MediaBase string `yaml:"-"`
}

// Scan tunes the analysis stage.
type Scan struct {
	Workers            int  `yaml:"workers"`
	IncludeContentRefs bool `yaml:"includeContentRefs"`
	IncludeCache       bool `yaml:"includeCache"`
	// ExcludeGlobs skips entries whose path (relative to the scan root) or
	// base name matches one of these shell patterns. A matching directory is
	// skipped whole. Useful for staging folders left behind by importers.
	ExcludeGlobs []string `yaml:"excludeGlobs"`
}

// Cleanup tunes destructive operations.
type Cleanup struct {
	QuarantineDir    string `yaml:"quarantineDir"`
	BatchSize        int    `yaml:"batchSize"`
	Parallel         int    `yaml:"parallel"`
	AllowCrossDevice bool   `yaml:"allowCrossDevice"`
	// MaxDeleteFraction aborts a quarantine run when the orphan ratio exceeds
	// it (0-1]. A ratio that extreme usually means reference collection
	// failed rather than that the shop really is that full of garbage.
	MaxDeleteFraction float64 `yaml:"maxDeleteFraction"`
}

// Warm tunes the thumbnail-cache warm-up stage, which refills the derived
// cache by requesting the cached image URLs through the storefront.
//
// Nothing here is read by the analysis commands; a shop can warm its cache
// without knowing anything about orphans, and can find orphans without ever
// pointing the tool at the web server.
type Warm struct {
	// BaseURL is the storefront URL the cache URLs are resolved against.
	// Empty means "read web/secure/base_url (then web/unsecure/base_url) from
	// core_config_data", which needs database access.
	BaseURL string `yaml:"baseUrl"`
	// CacheHashes pins the size sets to warm. Normally they are discovered
	// from the cache directory, or read back from HashFile, so this only
	// exists for pinning a specific set by hand.
	CacheHashes []string `yaml:"cacheHashes"`
	// HashFile is a snapshot of the size sets, written by
	// `cache clean --save-hashes` before the tree is emptied. It is what
	// survives the gap between emptying the cache and refilling it.
	HashFile string `yaml:"hashFile"`
	// Concurrency is the number of requests in flight.
	Concurrency int `yaml:"concurrency"`
	// Timeout bounds a single request.
	Timeout Duration `yaml:"timeout"`
	// Method is "get" or "head"; empty means "get". Prefer get: a caching
	// proxy can answer a head from the edge without generating anything.
	Method string `yaml:"method"`
	// UserAgent is sent with every request. Empty means the built-in default,
	// which names and versions the tool so that whoever sees the traffic in
	// their logs can tell what it is.
	UserAgent string `yaml:"userAgent"`
	// MaxRequests stops a run after this many requests, so a large catalog can
	// be warmed in slices (0 = no limit).
	MaxRequests int `yaml:"maxRequests"`
	// Rate caps request starts per second across all workers (0 = no limit).
	Rate int `yaml:"rate"`
	// MaxErrorFraction aborts the run's verdict when the share of failed
	// requests exceeds it (0-1]. A run that mostly fails is evidence that the
	// tool is talking to the wrong host, not that the shop is broken.
	MaxErrorFraction float64 `yaml:"maxErrorFraction"`
}

// Output tunes reporting.
type Output struct {
	Format   OutputFormat `yaml:"format"`
	Language Language     `yaml:"language"`
	Verbose  int          `yaml:"verbose"`
	Quiet    bool         `yaml:"quiet"`
}

// Config is the fully resolved configuration.
type Config struct {
	Magento Magento `yaml:"magento"`
	DB      DB      `yaml:"database"`
	Scan    Scan    `yaml:"scan"`
	Cleanup Cleanup `yaml:"cleanup"`
	Warm    Warm    `yaml:"warm"`
	Output  Output  `yaml:"output"`

	// ConfigFile records where the YAML came from, for provenance in reports.
	ConfigFile string `yaml:"-"`
}

// Default returns the built-in defaults, which assume a typical Magento 2
// layout and a locally reachable MySQL server.
func Default() *Config {
	return &Config{
		DB: DB{
			Host:    "localhost",
			Port:    3306,
			Charset: "utf8mb4",
		},
		Scan: Scan{
			Workers:            defaultWorkers(),
			IncludeContentRefs: true,
			IncludeCache:       false,
		},
		Cleanup: Cleanup{
			BatchSize:         1000,
			Parallel:          defaultWorkers(),
			MaxDeleteFraction: 0.98,
		},
		Warm: Warm{
			// Four is deliberately well below the scan worker count. Warming
			// spends the storefront's PHP workers, which are the same ones
			// serving visitors, so the ceiling that matters is not this
			// machine's CPU. Callers raise it once they have measured.
			Concurrency: 4,
			Timeout:     Duration(30 * time.Second),
			// Spelled out rather than left empty even though warm.ParseMethod
			// treats "" as get. A literal here cannot drift silently: the
			// accepted values live in internal/warm, and anything it does not
			// recognize fails before the first request is issued.
			Method:           "get",
			MaxErrorFraction: 0.05,
		},
		Output: Output{
			Format:   FormatTable,
			Language: LanguageEnglish,
			Verbose:  0,
		},
	}
}

func defaultWorkers() int {
	n := runtime.NumCPU()
	if n < 2 {
		return 2
	}
	if n > 16 {
		return 16
	}
	return n
}

// DSN builds a go-sql-driver/mysql connection string using the driver's own
// formatter, which handles special characters in credentials correctly.
//
// The password is never logged by this package; callers should use
// RedactedDSN when they need something printable.
func (d DB) DSN() string {
	return d.dsn(false)
}

// RedactedDSN is a copy of DSN safe to print in logs and reports.
func (d DB) RedactedDSN() string {
	return d.dsn(true)
}

func (d DB) dsn(redact bool) string {
	charset := d.Charset
	if charset == "" {
		charset = "utf8mb4"
	}
	c := mysql.NewConfig()
	c.User = d.User
	c.Passwd = d.Password
	if redact && c.Passwd != "" {
		c.Passwd = "***"
	}
	c.DBName = d.Name
	c.Params = map[string]string{"charset": charset}
	c.Timeout = 30 * time.Second
	c.ReadTimeout = 10 * time.Minute
	c.WriteTimeout = 10 * time.Minute
	// Loc is left as UTC on purpose: mage-mediagc never interprets datetimes.
	if d.Socket != "" {
		c.Net = "unix"
		c.Addr = d.Socket
	} else {
		c.Net = "tcp"
		c.Addr = net.JoinHostPort(d.Host, strconv.Itoa(d.Port))
	}
	return c.FormatDSN()
}

// ValidateMode names the parts of the configuration that a command will
// actually use, so that a command which cannot touch one of them is not
// blocked by the gap.
//
// This matters because the natural time to discover that the database settings
// are wrong is before you have run anything, and the natural tool is
// `config show` — which does not need a database at all. Filesystem-only
// commands are in the same position: `cache clean` on a web host that cannot
// reach MySQL is a reasonable thing to want, and the credentials have nothing
// to do with whether it can delete a directory.
type ValidateMode struct {
	// Database requires the MySQL connection settings.
	Database bool
	// Write requires the paths that operations modifying the media tree need.
	Write bool
}

// The three kinds of command the CLI has, named so call sites read as intent.
var (
	// ModeAnalyze is for commands that read the database and the media tree.
	ModeAnalyze = ValidateMode{Database: true}
	// ModeWrite is for commands that also modify the media tree.
	ModeWrite = ValidateMode{Database: true, Write: true}
	// ModeLocal is for commands that only touch the filesystem.
	ModeLocal = ValidateMode{}
)

// databaseProblems lists the settings that a connection needs, so that the
// wording lives in one place whether it is Validate or a command that decides
// to connect partway through asking for it.
func (c *Config) databaseProblems() []string {
	var problems []string
	if c.DB.Name == "" {
		problems = append(problems, "database name is required (--db-name or database.name)")
	}
	if c.DB.User == "" {
		problems = append(problems, "database user is required (--db-user or database.user)")
	}
	return problems
}

// RequireDatabase reports the missing database settings as an error.
//
// It is exported for commands that connect conditionally: `cache warm` reads
// the storefront base URL from the database only when --base-url was not
// given, so it asks for these settings at the point it decides to connect
// rather than up front.
func (c *Config) RequireDatabase() error {
	if problems := c.databaseProblems(); len(problems) > 0 {
		return errors.New("invalid configuration:\n  - " + strings.Join(problems, "\n  - "))
	}
	return nil
}

// Validate reports configuration problems that would fail later, in a way
// that is easier to act on up front.
func (c *Config) Validate(mode ValidateMode) error {
	var problems []string

	if c.Magento.MediaPath == "" && c.Magento.Root == "" {
		problems = append(problems, "magento root or media path must be set "+
			"(run from the Magento root, pass --magento-root/--media-path, "+
			"or set magento.root in the config file)")
	}
	if mode.Database {
		problems = append(problems, c.databaseProblems()...)
	}
	switch c.Output.Format {
	case FormatTable, FormatJSON, FormatMarkdown:
	default:
		problems = append(problems, fmt.Sprintf("unsupported output format %q (want table, json or markdown)", c.Output.Format))
	}
	langOK := false
	for _, l := range SupportedLanguages {
		if c.Output.Language == l {
			langOK = true
			break
		}
	}
	if !langOK {
		problems = append(problems, fmt.Sprintf(
			"unsupported output language %q (want en or zh)", c.Output.Language))
	}
	if c.Scan.Workers < 1 {
		problems = append(problems, "scan.workers must be >= 1")
	}
	if c.Cleanup.BatchSize < 1 {
		problems = append(problems, "cleanup.batchSize must be >= 1")
	}
	if c.Cleanup.Parallel < 1 {
		problems = append(problems, "cleanup.parallel must be >= 1")
	}
	if c.Cleanup.MaxDeleteFraction <= 0 || c.Cleanup.MaxDeleteFraction > 1 {
		problems = append(problems, fmt.Sprintf(
			"cleanup.maxDeleteFraction must be in (0, 1], got %g", c.Cleanup.MaxDeleteFraction))
	}

	// The warm settings are checked here even though most commands never read
	// them: a typo in the config file should be reported by whichever command
	// runs first, not hours later by the one that finally warms the cache.
	//
	// warm.method is intentionally not validated here. Its accepted values
	// live in internal/warm, and duplicating the list would let the two drift
	// apart silently; the warm run rejects an unknown method before it issues
	// a single request.
	if c.Warm.Concurrency < 1 {
		problems = append(problems, "warm.concurrency must be >= 1")
	}
	if c.Warm.Timeout.Std() <= 0 {
		problems = append(problems, `warm.timeout must be positive (for example "30s")`)
	}
	if c.Warm.MaxErrorFraction <= 0 || c.Warm.MaxErrorFraction > 1 {
		problems = append(problems, fmt.Sprintf(
			"warm.maxErrorFraction must be in (0, 1], got %g", c.Warm.MaxErrorFraction))
	}
	if c.Warm.MaxRequests < 0 {
		problems = append(problems, "warm.maxRequests must be >= 0")
	}
	if c.Warm.Rate < 0 {
		problems = append(problems, "warm.rate must be >= 0")
	}

	if mode.Write {
		if c.Cleanup.QuarantineDir == "" {
			problems = append(problems, "quarantine directory could not be derived; pass --quarantine-dir")
		}
		if c.Magento.MediaPath == "" {
			problems = append(problems, "media path is required for write operations")
		} else if _, err := os.Stat(c.Magento.MediaPath); err != nil {
			problems = append(problems, fmt.Sprintf("media path %s is not accessible: %v", c.Magento.MediaPath, err))
		}
	}

	if len(problems) > 0 {
		return errors.New("invalid configuration:\n  - " + strings.Join(problems, "\n  - "))
	}
	return nil
}

// DerivePaths fills MediaPath/MediaBase from whichever of them was provided,
// and picks a default quarantine directory on the same filesystem as media.
func (c *Config) DerivePaths() {
	root := strings.TrimSpace(c.Magento.Root)
	media := strings.TrimSpace(c.Magento.MediaPath)

	if media == "" && root != "" {
		media = filepath.Join(root, "pub", "media", "catalog", "product")
	}
	if media != "" {
		media = filepath.Clean(media)
		c.Magento.MediaPath = media
		// <media base>/catalog/product -> <media base>
		c.Magento.MediaBase = filepath.Dir(filepath.Dir(media))
	}
	if root == "" && media != "" {
		// Walk up looking for app/etc/env.php so relative layouts still work.
		if found := DetectMagentoRoot(filepath.Dir(filepath.Dir(media))); found != "" {
			c.Magento.Root = found
		}
	}
	if c.Cleanup.QuarantineDir == "" && root != "" {
		c.Cleanup.QuarantineDir = filepath.Join(root, "var", "mage-mediagc", "quarantine")
	}
}

// DetectMagentoRoot returns the closest ancestor of start (inclusive) that
// contains app/etc/env.php, or "" when none is found.
func DetectMagentoRoot(start string) string {
	dir, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "app", "etc", "env.php")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
