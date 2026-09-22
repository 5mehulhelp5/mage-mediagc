package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultConfigNames are searched in the working directory when --config is
// not given.
var DefaultConfigNames = []string{"mage-mediagc.yaml", "mage-mediagc.yml", ".mage-mediagc.yaml"}

// Overrides carries values explicitly supplied on the command line.
// A nil pointer means "not supplied", which lets the loader distinguish
// "unset" from "deliberately set to the zero value".
type Overrides struct {
	ConfigFile    string
	MagentoRoot   *string
	MediaPath     *string
	DBHost        *string
	DBPort        *int
	DBName        *string
	DBUser        *string
	DBPassword    *string
	DBSocket      *string
	Workers       *int
	BatchSize     *int
	Parallel      *int
	QuarantineDir *string
	Format        *string
	Language      *string
	Verbose       *int
	Quiet         *bool
	ContentRefs   *bool
}

// Load merges defaults, the YAML file, Magento's env.php, environment
// variables and command-line overrides into a single Config.
func Load(ov Overrides) (*Config, error) {
	cfg := Default()

	if err := applyConfigFile(cfg, ov.ConfigFile); err != nil {
		return nil, err
	}

	// 1) Establish the Magento root as early as possible; env.php discovery
	//    depends on it and users expect `cd /var/www/magento && mage-mediagc scan`
	//    to just work.
	root := firstNonEmpty(derefString(ov.MagentoRoot), cfg.Magento.Root)
	if root == "" {
		if wd, err := os.Getwd(); err == nil {
			root = DetectMagentoRoot(wd)
		}
	}
	if root != "" {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("resolve magento root %q: %w", root, err)
		}
		cfg.Magento.Root = abs
	}

	// 2) Fill database gaps from env.php. Explicit values always win, so a
	//    user pointing at a replica or a Docker sidecar is never surprised.
	if cfg.Magento.Root != "" {
		if env, err := LoadEnvPHP(cfg.Magento.Root); err == nil {
			if cfg.DB.Name == "" {
				cfg.DB.Name = env.DBName
			}
			if cfg.DB.User == "" {
				cfg.DB.User = env.DBUser
			}
			if cfg.DB.Password == "" {
				cfg.DB.Password = env.DBPassword
			}
			if cfg.DB.Socket == "" {
				cfg.DB.Socket = env.DBSocket
			}
			if env.DBHost != "" && cfg.DB.Host == Default().DB.Host {
				cfg.DB.Host = env.DBHost
			}
			if env.DBPort != 0 && cfg.DB.Port == Default().DB.Port {
				cfg.DB.Port = env.DBPort
			}
		} else if !os.IsNotExist(err) && ov.ConfigFile == "" && cfg.DB.Name == "" {
			// A malformed env.php is worth surfacing when it was our only hope
			// of finding credentials.
			return nil, fmt.Errorf("cannot read database credentials: %w", err)
		}
	}

	// 3) Environment variables.
	applyEnv(cfg)

	// 4) Command-line overrides have the final word.
	applyOverrides(cfg, ov)

	cfg.DerivePaths()
	return cfg, nil
}

func applyConfigFile(cfg *Config, explicit string) error {
	path := explicit
	if path == "" {
		wd, err := os.Getwd()
		if err != nil {
			// Auto-discovery of a config file in the working directory is a
			// convenience, not a requirement: failing to read the cwd just
			// means we fall through to env.php and the flags.
			return nil //nolint:nilerr // best-effort discovery
		}
		for _, name := range DefaultConfigNames {
			candidate := filepath.Join(wd, name)
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
				break
			}
		}
		if path == "" {
			return nil
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config file %s: %w", path, err)
	}
	abs, absErr := filepath.Abs(path)
	if absErr == nil {
		cfg.ConfigFile = abs
	} else {
		cfg.ConfigFile = path
	}
	return nil
}

func applyEnv(cfg *Config) {
	if v := envString("MAGEGC_MAGENTO_ROOT"); v != "" {
		cfg.Magento.Root = v
	}
	if v := envString("MAGEGC_MEDIA_PATH"); v != "" {
		cfg.Magento.MediaPath = v
	}
	if v := envString("MAGEGC_DB_HOST"); v != "" {
		cfg.DB.Host = v
	}
	if v, ok := envInt("MAGEGC_DB_PORT"); ok {
		cfg.DB.Port = v
	}
	if v := envString("MAGEGC_DB_NAME"); v != "" {
		cfg.DB.Name = v
	}
	if v := envString("MAGEGC_DB_USER"); v != "" {
		cfg.DB.User = v
	}
	if v, ok := os.LookupEnv("MAGEGC_DB_PASSWORD"); ok {
		cfg.DB.Password = v
	}
	if v := envString("MAGEGC_DB_SOCKET"); v != "" {
		cfg.DB.Socket = v
	}
	if v, ok := envInt("MAGEGC_WORKERS"); ok {
		cfg.Scan.Workers = v
	}
	if v := envString("MAGEGC_QUARANTINE_DIR"); v != "" {
		cfg.Cleanup.QuarantineDir = v
	}
	if v := envString("MAGEGC_FORMAT"); v != "" {
		cfg.Output.Format = OutputFormat(v)
	}
	if v := envString("MAGEGC_LANGUAGE"); v != "" {
		cfg.Output.Language = Language(v)
	}
}

func applyOverrides(cfg *Config, ov Overrides) {
	if ov.MagentoRoot != nil {
		cfg.Magento.Root = *ov.MagentoRoot
	}
	if ov.MediaPath != nil {
		cfg.Magento.MediaPath = *ov.MediaPath
	}
	if ov.DBHost != nil {
		cfg.DB.Host = *ov.DBHost
	}
	if ov.DBPort != nil {
		cfg.DB.Port = *ov.DBPort
	}
	if ov.DBName != nil {
		cfg.DB.Name = *ov.DBName
	}
	if ov.DBUser != nil {
		cfg.DB.User = *ov.DBUser
	}
	if ov.DBPassword != nil {
		cfg.DB.Password = *ov.DBPassword
	}
	if ov.DBSocket != nil {
		cfg.DB.Socket = *ov.DBSocket
	}
	if ov.Workers != nil {
		cfg.Scan.Workers = *ov.Workers
	}
	if ov.BatchSize != nil {
		cfg.Cleanup.BatchSize = *ov.BatchSize
	}
	if ov.Parallel != nil {
		cfg.Cleanup.Parallel = *ov.Parallel
	}
	if ov.QuarantineDir != nil {
		cfg.Cleanup.QuarantineDir = *ov.QuarantineDir
	}
	if ov.Format != nil {
		cfg.Output.Format = OutputFormat(*ov.Format)
	}
	if ov.Language != nil {
		cfg.Output.Language = Language(*ov.Language)
	}
	if ov.Verbose != nil {
		cfg.Output.Verbose = *ov.Verbose
	}
	if ov.Quiet != nil {
		cfg.Output.Quiet = *ov.Quiet
	}
	if ov.ContentRefs != nil {
		cfg.Scan.IncludeContentRefs = *ov.ContentRefs
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func envString(key string) string { return strings.TrimSpace(os.Getenv(key)) }

func envInt(key string) (int, bool) {
	raw := envString(key)
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return n, true
}
