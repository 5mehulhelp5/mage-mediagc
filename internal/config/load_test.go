package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testEnvPHP = `<?php
return [
    'db' => [
        'table_prefix' => 'mg_',
        'connection' => [
            'default' => [
                'host' => '10.1.2.3:3307',
                'dbname' => 'shop_prod',
                'username' => 'shopuser',
                'password' => 's3cr3t',
                'active' => '1',
            ],
        ],
    ],
];
`

func writeEnvPHP(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "app", "etc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "env.php"), []byte(testEnvPHP), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadEnvPHP(t *testing.T) {
	root := t.TempDir()
	writeEnvPHP(t, root)

	env, err := LoadEnvPHP(root)
	if err != nil {
		t.Fatalf("LoadEnvPHP: %v", err)
	}
	if env.DBName != "shop_prod" {
		t.Errorf("DBName = %q", env.DBName)
	}
	if env.DBUser != "shopuser" {
		t.Errorf("DBUser = %q", env.DBUser)
	}
	if env.DBPassword != "s3cr3t" {
		t.Errorf("DBPassword = %q", env.DBPassword)
	}
	if env.DBHost != "10.1.2.3" {
		t.Errorf("DBHost = %q, want host part only", env.DBHost)
	}
	if env.DBPort != 3307 {
		t.Errorf("DBPort = %d, want 3307 parsed from host", env.DBPort)
	}
	if env.TablePrefix != "mg_" {
		t.Errorf("TablePrefix = %q", env.TablePrefix)
	}
}

func TestLoadEnvPHPMissingFile(t *testing.T) {
	if _, err := LoadEnvPHP(t.TempDir()); err == nil {
		t.Fatal("expected an error when env.php is absent")
	}
}

// writeEnvPHPBody writes an arbitrary env.php, for fixtures that only differ in
// one field.
func writeEnvPHPBody(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "app", "etc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "env.php"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// envPHPWithPort builds an env.php whose only interesting field is `port`.
func envPHPWithPort(portLiteral string) string {
	return `<?php
return [
    'db' => [
        'connection' => [
            'default' => [
                'host' => 'localhost',
                'port' => ` + portLiteral + `,
                'dbname' => 'shop',
            ],
        ],
    ],
];
`
}

// Magento writes the port as a quoted string. A bare type assertion used to drop
// it, leaving the operator silently on the default port.
func TestLoadEnvPHPReadsQuotedPort(t *testing.T) {
	for _, tc := range []struct {
		name      string
		portValue string
		want      int
	}{
		{"quoted", "'3309'", 3309},
		{"unquoted", "3309", 3309},
		{"float", "3309.0", 3309},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeEnvPHPBody(t, envPHPWithPort(tc.portValue))
			env, err := LoadEnvPHP(root)
			if err != nil {
				t.Fatalf("LoadEnvPHP: %v", err)
			}
			if env.DBPort != tc.want {
				t.Errorf("DBPort = %d, want %d", env.DBPort, tc.want)
			}
		})
	}
}

// An explicit port wins over one embedded in the host.
func TestLoadEnvPHPPortOverridesHostPort(t *testing.T) {
	root := writeEnvPHPBody(t, `<?php
return [
    'db' => [
        'connection' => [
            'default' => [
                'host' => 'db.internal:3307',
                'port' => '3310',
                'dbname' => 'shop',
            ],
        ],
    ],
];
`)
	env, err := LoadEnvPHP(root)
	if err != nil {
		t.Fatalf("LoadEnvPHP: %v", err)
	}
	if env.DBHost != "db.internal" || env.DBPort != 3310 {
		t.Errorf("got %s:%d, want db.internal:3310", env.DBHost, env.DBPort)
	}
}

// A port outside 1-65535 is a configuration error, not something to fall back
// from silently. 4294967296 is 2^32, which a narrowing conversion would turn
// into 0 -- the value this package reads as "no port configured".
func TestLoadEnvPHPRejectsOutOfRangePort(t *testing.T) {
	for _, tc := range []struct{ name, portValue string }{
		{"zero", "0"},
		{"negative", "-1"},
		{"too large", "99999"},
		{"wraps a 32-bit int", "4294967296"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeEnvPHPBody(t, envPHPWithPort(tc.portValue))
			if _, err := LoadEnvPHP(root); err == nil {
				t.Fatalf("expected an error for port %s", tc.portValue)
			}
		})
	}
}

// A port the parser cannot read at all is not an error: the host-derived port or
// the default still applies.
func TestLoadEnvPHPIgnoresUnreadablePort(t *testing.T) {
	root := writeEnvPHPBody(t, `<?php
return [
    'db' => [
        'connection' => [
            'default' => [
                'host' => 'db.internal:3307',
                'port' => 'not-a-port',
                'dbname' => 'shop',
            ],
        ],
    ],
];
`)
	env, err := LoadEnvPHP(root)
	if err != nil {
		t.Fatalf("LoadEnvPHP: %v", err)
	}
	if env.DBHost != "db.internal" || env.DBPort != 3307 {
		t.Errorf("got %s:%d, want db.internal:3307", env.DBHost, env.DBPort)
	}
}

func TestLoadEnvPHPRejectsOutOfRangePortInHost(t *testing.T) {
	for _, host := range []string{"db.internal:99999", "db.internal:-1"} {
		root := writeEnvPHPBody(t, `<?php
return [
    'db' => [
        'connection' => [
            'default' => ['host' => '`+host+`', 'dbname' => 'shop'],
        ],
    ],
];
`)
		if _, err := LoadEnvPHP(root); err == nil {
			t.Errorf("expected an error for host %q", host)
		}
	}
}

func TestDetectMagentoRootWalksUp(t *testing.T) {
	root := t.TempDir()
	writeEnvPHP(t, root)
	deep := filepath.Join(root, "pub", "media", "catalog", "product", "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	got := DetectMagentoRoot(deep)
	if got != root {
		t.Fatalf("DetectMagentoRoot = %q, want %q", got, root)
	}
	if DetectMagentoRoot(t.TempDir()) != "" {
		t.Fatal("expected no root to be detected in an empty tree")
	}
}

func TestLoadDerivesPathsAndFillsDBFromEnvPHP(t *testing.T) {
	root := t.TempDir()
	writeEnvPHP(t, root)

	cfg, err := Load(Overrides{MagentoRoot: &root})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	wantMedia := filepath.Join(root, "pub", "media", "catalog", "product")
	if cfg.Magento.MediaPath != wantMedia {
		t.Errorf("MediaPath = %q, want %q", cfg.Magento.MediaPath, wantMedia)
	}
	if cfg.Magento.MediaBase != filepath.Join(root, "pub", "media") {
		t.Errorf("MediaBase = %q", cfg.Magento.MediaBase)
	}
	if cfg.DB.Name != "shop_prod" {
		t.Errorf("DB.Name = %q, want shop_prod from env.php", cfg.DB.Name)
	}
	if cfg.DB.Host != "10.1.2.3" {
		t.Errorf("DB.Host = %q", cfg.DB.Host)
	}
	if cfg.DB.Port != 3307 {
		t.Errorf("DB.Port = %d", cfg.DB.Port)
	}
	if cfg.Cleanup.QuarantineDir == "" {
		t.Error("expected a default quarantine directory to be derived")
	}
	if !strings.HasPrefix(cfg.Cleanup.QuarantineDir, root) {
		t.Errorf("quarantine dir %q should live under the Magento root", cfg.Cleanup.QuarantineDir)
	}
}

func TestExplicitFlagsBeatEnvPHP(t *testing.T) {
	root := t.TempDir()
	writeEnvPHP(t, root)

	host := "127.0.0.1"
	port := 3308
	name := "override_db"
	user := "override_user"
	pw := "override_pw"

	cfg, err := Load(Overrides{
		MagentoRoot: &root,
		DBHost:      &host,
		DBPort:      &port,
		DBName:      &name,
		DBUser:      &user,
		DBPassword:  &pw,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DB.Host != host || cfg.DB.Port != port || cfg.DB.Name != name ||
		cfg.DB.User != user || cfg.DB.Password != pw {
		t.Fatalf("explicit values were not honored: %+v", cfg.DB)
	}
}

func TestMediaPathOverrideWins(t *testing.T) {
	root := t.TempDir()
	writeEnvPHP(t, root)
	media := "/custom/media/catalog/product"

	cfg, err := Load(Overrides{MagentoRoot: &root, MediaPath: &media})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Magento.MediaPath != "/custom/media/catalog/product" {
		t.Fatalf("MediaPath = %q", cfg.Magento.MediaPath)
	}
	if cfg.Magento.MediaBase != "/custom/media" {
		t.Fatalf("MediaBase = %q", cfg.Magento.MediaBase)
	}
}

func TestConfigFileIsApplied(t *testing.T) {
	root := t.TempDir()
	writeEnvPHP(t, root)
	cfgPath := filepath.Join(root, "mage-mediagc.yaml")
	yaml := `
magento:
  root: ` + root + `
scan:
  workers: 3
  includeContentRefs: false
cleanup:
  batchSize: 250
output:
  format: json
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(Overrides{ConfigFile: cfgPath})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Scan.Workers != 3 {
		t.Errorf("Workers = %d, want 3", cfg.Scan.Workers)
	}
	if cfg.Scan.IncludeContentRefs {
		t.Error("includeContentRefs should have been disabled by the config file")
	}
	if cfg.Cleanup.BatchSize != 250 {
		t.Errorf("BatchSize = %d, want 250", cfg.Cleanup.BatchSize)
	}
	if cfg.Output.Format != FormatJSON {
		t.Errorf("Format = %q, want json", cfg.Output.Format)
	}
	if cfg.ConfigFile != cfgPath {
		t.Errorf("ConfigFile = %q, want %q", cfg.ConfigFile, cfgPath)
	}
}

func TestDatabaseGapFillingDoesNotOverwriteYAML(t *testing.T) {
	root := t.TempDir()
	writeEnvPHP(t, root)
	cfgPath := filepath.Join(root, "mage-mediagc.yaml")
	yaml := `
magento:
  root: ` + root + `
database:
  name: from_yaml
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(Overrides{ConfigFile: cfgPath})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DB.Name != "from_yaml" {
		t.Fatalf("DB.Name = %q, want the value from the config file", cfg.DB.Name)
	}
	// User still came from env.php because YAML left it empty.
	if cfg.DB.User != "shopuser" {
		t.Fatalf("DB.User = %q, want shopuser filled from env.php", cfg.DB.User)
	}
}

func TestValidate(t *testing.T) {
	good := Default()
	good.Magento.MediaPath = t.TempDir()
	good.DB.Name = "db"
	good.DB.User = "user"
	if err := good.Validate(ModeAnalyze); err != nil {
		t.Fatalf("expected a valid config, got %v", err)
	}

	noDB := Default()
	noDB.Magento.MediaPath = t.TempDir()
	if err := noDB.Validate(ModeAnalyze); err == nil {
		t.Fatal("expected an error when the database is unset")
	}

	badFormat := Default()
	badFormat.Magento.MediaPath = t.TempDir()
	badFormat.DB.Name, badFormat.DB.User = "d", "u"
	badFormat.Output.Format = "xml"
	if err := badFormat.Validate(ModeAnalyze); err == nil {
		t.Fatal("expected an error for an unsupported format")
	}

	noMedia := Default()
	noMedia.DB.Name, noMedia.DB.User = "d", "u"
	if err := noMedia.Validate(ModeAnalyze); err == nil {
		t.Fatal("expected an error when no media path or root is known")
	}
}

// A command that only touches the filesystem has no business demanding
// database credentials. cache clean and config show are the tools an operator
// reaches for on a web host that cannot reach MySQL, and refusing to run
// without credentials would make the diagnosis impossible.
func TestValidateLocalDoesNotRequireADatabase(t *testing.T) {
	cfg := Default()
	cfg.Magento.MediaPath = t.TempDir()

	if err := cfg.Validate(ModeLocal); err != nil {
		t.Fatalf("filesystem-only validation must not require the database: %v", err)
	}
	// The same config is still refused when a command really will connect, so
	// the relaxation is scoped to the commands that need it.
	if err := cfg.Validate(ModeAnalyze); err == nil {
		t.Fatal("analyze mode must still require database settings")
	}
	if err := cfg.RequireDatabase(); err == nil {
		t.Fatal("RequireDatabase must report the missing name and user")
	} else if !strings.Contains(err.Error(), "database name is required") ||
		!strings.Contains(err.Error(), "database user is required") {
		t.Fatalf("RequireDatabase should name both missing settings, got: %v", err)
	}
}

func TestValidateRejectsBadWarmSettings(t *testing.T) {
	cases := map[string]func(*Config){
		"concurrency":      func(c *Config) { c.Warm.Concurrency = 0 },
		"timeout":          func(c *Config) { c.Warm.Timeout = 0 },
		"maxErrorFraction": func(c *Config) { c.Warm.MaxErrorFraction = 1.5 },
		"maxRequests":      func(c *Config) { c.Warm.MaxRequests = -1 },
		"rate":             func(c *Config) { c.Warm.Rate = -1 },
	}
	for name, corrupt := range cases {
		cfg := Default()
		cfg.Magento.MediaPath = t.TempDir()
		cfg.DB.Name, cfg.DB.User = "d", "u"
		corrupt(cfg)
		if err := cfg.Validate(ModeAnalyze); err == nil {
			t.Errorf("expected warm.%s to be rejected", name)
		}
	}

	// The built-in warm defaults have to survive their own validation, which
	// is what makes an unmodified config file usable.
	fresh := Default()
	fresh.Magento.MediaPath = t.TempDir()
	fresh.DB.Name, fresh.DB.User = "d", "u"
	if err := fresh.Validate(ModeAnalyze); err != nil {
		t.Fatalf("the built-in warm defaults must validate: %v", err)
	}
}

func TestValidateForWriteRequiresExistingMediaPath(t *testing.T) {
	cfg := Default()
	cfg.Magento.MediaPath = filepath.Join(t.TempDir(), "does-not-exist")
	cfg.DB.Name, cfg.DB.User = "d", "u"
	cfg.Cleanup.QuarantineDir = "/tmp/q"
	if err := cfg.Validate(ModeWrite); err == nil {
		t.Fatal("expected an error for a missing media path in write mode")
	}
}

func TestDSNRedaction(t *testing.T) {
	db := DB{Host: "localhost", Port: 3306, Name: "shop", User: "u", Password: "topsecret"}
	full := db.DSN()
	if !strings.Contains(full, "topsecret") {
		t.Fatalf("real DSN should contain the password: %s", full)
	}
	red := db.RedactedDSN()
	if strings.Contains(red, "topsecret") {
		t.Fatalf("redacted DSN leaked the password: %s", red)
	}
}

func TestDSNSpecialCharactersSurviveRoundTrip(t *testing.T) {
	db := DB{
		Host: "127.0.0.1", Port: 3306, Name: "shop",
		User: "user@corp", Password: "p@ss:w/rd#x",
	}
	dsn := db.DSN()
	if !strings.Contains(dsn, "p@ss:w/rd#x") {
		t.Fatalf("password was mangled in the DSN: %s", dsn)
	}
}

func TestDSNUsesUnixSocketWhenProvided(t *testing.T) {
	db := DB{Socket: "/var/run/mysqld/mysqld.sock", Name: "shop", User: "u"}
	dsn := db.DSN()
	if !strings.Contains(dsn, "unix(") {
		t.Fatalf("expected a unix socket DSN, got %s", dsn)
	}
}

func TestSplitHostPort(t *testing.T) {
	cases := []struct {
		in       string
		wantHost string
		wantPort int
	}{
		{"localhost", "localhost", 0},
		{"10.0.0.5", "10.0.0.5", 0},
		{"10.0.0.5:3307", "10.0.0.5", 3307},
		{"mysql://user@db.internal:3308/shop", "db.internal", 3308},
		{"", "", 0},
	}
	for _, tc := range cases {
		host, port := splitHostPort(tc.in)
		if host != tc.wantHost || port != tc.wantPort {
			t.Errorf("splitHostPort(%q) = (%q, %d), want (%q, %d)",
				tc.in, host, port, tc.wantHost, tc.wantPort)
		}
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("MAGEGC_DB_NAME", "from_env")
	t.Setenv("MAGEGC_WORKERS", "7")

	cfg, err := Load(Overrides{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DB.Name != "from_env" {
		t.Errorf("DB.Name = %q, want from_env", cfg.DB.Name)
	}
	if cfg.Scan.Workers != 7 {
		t.Errorf("Workers = %d, want 7", cfg.Scan.Workers)
	}
}

func TestFlagsBeatEnvironment(t *testing.T) {
	t.Setenv("MAGEGC_DB_NAME", "from_env")
	name := "from_flag"
	cfg, err := Load(Overrides{DBName: &name})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DB.Name != "from_flag" {
		t.Fatalf("DB.Name = %q, want the flag value to win", cfg.DB.Name)
	}
}

func TestDefaultWorkersIsSane(t *testing.T) {
	cfg := Default()
	if cfg.Scan.Workers < 1 || cfg.Scan.Workers > 16 {
		t.Fatalf("Workers = %d, want 1..16", cfg.Scan.Workers)
	}
	if cfg.Cleanup.BatchSize < 1 {
		t.Fatalf("BatchSize = %d", cfg.Cleanup.BatchSize)
	}
	if cfg.Cleanup.MaxDeleteFraction <= 0 || cfg.Cleanup.MaxDeleteFraction > 1 {
		t.Fatalf("MaxDeleteFraction = %g, want a value in (0, 1]", cfg.Cleanup.MaxDeleteFraction)
	}
	// The default must be a real guard, not "everything is allowed".
	if cfg.Cleanup.MaxDeleteFraction >= 1 {
		t.Errorf("MaxDeleteFraction = %g, want a threshold below 1 so a broken "+
			"reference collection cannot quarantine the whole catalog",
			cfg.Cleanup.MaxDeleteFraction)
	}
	// The defaults are only required to be valid once the shop is known.
	cfg.Magento.MediaPath = t.TempDir()
	cfg.DB.Name, cfg.DB.User = "shop", "shopuser"
	if err := cfg.Validate(ModeAnalyze); err != nil {
		t.Fatalf("the built-in defaults must validate once a shop is known: %v", err)
	}
}

// Zero means "auto" for the worker counts, wherever the zero came from. The
// shipped template and example config both write it deliberately, so a zero
// that survived loading was a config that no command would start from.
func TestZeroWorkerCountsResolveToAuto(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mage-mediagc.yaml")
	body := "scan:\n  workers: 0\ncleanup:\n  parallel: 0\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(Overrides{ConfigFile: path})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Scan.Workers < 1 {
		t.Errorf("scan.workers = %d, want the auto value", cfg.Scan.Workers)
	}
	if cfg.Cleanup.Parallel < 1 {
		t.Errorf("cleanup.parallel = %d, want the auto value", cfg.Cleanup.Parallel)
	}

	// A negative count is a mistake rather than a request for auto, and must
	// still be reported.
	cfg.Scan.Workers = -1
	cfg.Magento.MediaPath = t.TempDir()
	cfg.DB.Name, cfg.DB.User = "d", "u"
	if err := cfg.Validate(ModeAnalyze); err == nil {
		t.Fatal("expected a negative worker count to be rejected")
	}
}

func TestValidateRejectsOutOfRangeDeleteFraction(t *testing.T) {
	for _, bad := range []float64{0, -0.5, 1.5} {
		cfg := Default()
		cfg.Magento.MediaPath = t.TempDir()
		cfg.DB.Name, cfg.DB.User = "d", "u"
		cfg.Cleanup.MaxDeleteFraction = bad
		if err := cfg.Validate(ModeAnalyze); err == nil {
			t.Errorf("expected cleanup.maxDeleteFraction=%g to be rejected", bad)
		}
	}
}
