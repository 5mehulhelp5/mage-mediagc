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
	if err := good.Validate(false); err != nil {
		t.Fatalf("expected a valid config, got %v", err)
	}

	noDB := Default()
	noDB.Magento.MediaPath = t.TempDir()
	if err := noDB.Validate(false); err == nil {
		t.Fatal("expected an error when the database is unset")
	}

	badFormat := Default()
	badFormat.Magento.MediaPath = t.TempDir()
	badFormat.DB.Name, badFormat.DB.User = "d", "u"
	badFormat.Output.Format = "xml"
	if err := badFormat.Validate(false); err == nil {
		t.Fatal("expected an error for an unsupported format")
	}

	noMedia := Default()
	noMedia.DB.Name, noMedia.DB.User = "d", "u"
	if err := noMedia.Validate(false); err == nil {
		t.Fatal("expected an error when no media path or root is known")
	}
}

func TestValidateForWriteRequiresExistingMediaPath(t *testing.T) {
	cfg := Default()
	cfg.Magento.MediaPath = filepath.Join(t.TempDir(), "does-not-exist")
	cfg.DB.Name, cfg.DB.User = "d", "u"
	cfg.Cleanup.QuarantineDir = "/tmp/q"
	if err := cfg.Validate(true); err == nil {
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
	if err := cfg.Validate(false); err != nil {
		t.Fatalf("the built-in defaults must validate once a shop is known: %v", err)
	}
}

func TestValidateRejectsOutOfRangeDeleteFraction(t *testing.T) {
	for _, bad := range []float64{0, -0.5, 1.5} {
		cfg := Default()
		cfg.Magento.MediaPath = t.TempDir()
		cfg.DB.Name, cfg.DB.User = "d", "u"
		cfg.Cleanup.MaxDeleteFraction = bad
		if err := cfg.Validate(false); err == nil {
			t.Errorf("expected cleanup.maxDeleteFraction=%g to be rejected", bad)
		}
	}
}
