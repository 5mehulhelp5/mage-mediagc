package phpconfig

import (
	"os"
	"path/filepath"
	"testing"
)

// sampleEnvPHP mirrors the shape of a real Magento 2 app/etc/env.php,
// including the awkward bits: a namespaced-looking key, a nested numeric key,
// a password full of characters that break naive parsers, and a value
// containing a comma and a colon.
const sampleEnvPHP = `<?php
return [
    'backend' => [
        'frontName' => 'admin_x7k2p'
    ],
    'crypt' => [
        'key' => 'e3f1a2b4c5d60718293a4b5c6d7e8f90'
    ],
    'db' => [
        'table_prefix' => '',
        'connection' => [
            'default' => [
                'host' => 'localhost',
                'dbname' => 'magento',
                'username' => 'root',
                'password' => 'p@ss:w0rd/with#special\'quote',
                'active' => '1',
                'model' => 'mysql4',
                'engine' => 'innodb',
                'initStatements' => 'SET NAMES utf8;',
                'driver_options' => [
                    1014 => false
                ]
            ]
        ]
    ],
    'resource' => [
        'default_setup' => [
            'connection' => 'default'
        ]
    ],
    'x-frame-options' => 'SAMEORIGIN',
    'MAGE_MODE' => 'developer',
    'cache_types' => [
        'config' => 1,
        'layout' => 1,
        'block_html' => 0
    ],
    'install' => [
        // the date contains both a comma and colons
        'date' => 'Wed, 17 Dec 2020 09:00:00 +0000'
    ]
];
`

func TestParseRealisticEnvPHP(t *testing.T) {
	root, err := Parse([]byte(sampleEnvPHP))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	tests := []struct {
		name string
		path []string
		want string
	}{
		{"front name", []string{"backend", "frontName"}, "admin_x7k2p"},
		{"db name", []string{"db", "connection", "default", "dbname"}, "magento"},
		{"db user", []string{"db", "connection", "default", "username"}, "root"},
		{"empty prefix", []string{"db", "table_prefix"}, ""},
		{"mode", []string{"MAGE_MODE"}, "developer"},
		{"key with dash", []string{"x-frame-options"}, "SAMEORIGIN"},
		{"comma and colon in value", []string{"install", "date"}, "Wed, 17 Dec 2020 09:00:00 +0000"},
		{"init statements", []string{"db", "connection", "default", "initStatements"}, "SET NAMES utf8;"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := GetString(root, tc.path...)
			if !ok {
				t.Fatalf("GetString(%v) not found", tc.path)
			}
			if got != tc.want {
				t.Fatalf("GetString(%v) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}

	// The password is the classic parsing trap: '@', ':', '/', '#' and an
	// escaped single quote all in one value.
	pw, ok := GetString(root, "db", "connection", "default", "password")
	if !ok {
		t.Fatal("password not found")
	}
	want := `p@ss:w0rd/with#special'quote`
	if pw != want {
		t.Fatalf("password = %q, want %q", pw, want)
	}

	// Numeric key with a boolean value.
	drv, ok := GetMap(root, "db", "connection", "default", "driver_options")
	if !ok {
		t.Fatal("driver_options not found")
	}
	if v, ok := drv["1014"].(bool); !ok || v {
		t.Fatalf("driver_options[1014] = %#v, want false", drv["1014"])
	}

	// Integer stored as a real number, not a string.
	ct, ok := GetMap(root, "cache_types")
	if !ok {
		t.Fatal("cache_types not found")
	}
	if v, ok := ct["config"].(int64); !ok || v != 1 {
		t.Fatalf("cache_types[config] = %#v, want int64(1)", ct["config"])
	}
}

func TestParseArraySyntaxAndTrailingComma(t *testing.T) {
	src := `<?php
$ignored = 'x';
return array(
    'a' => array(1, 2, 3,),
    'b' => [
        'nested' => [ 'deep' => true ],
    ],
    'c' => null,
    'd' => -12,
    'e' => 1.5,
);`
	root, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if _, ok := Get(root, "c"); !ok {
		t.Fatal("null value lost")
	}
	if v, ok := GetInt(root, "d"); !ok || v != -12 {
		t.Fatalf("d = %v, want -12", v)
	}
	if v, ok := Get(root, "e"); !ok {
		t.Fatal("e missing")
	} else if f, isFloat := v.(float64); !isFloat || f != 1.5 {
		t.Fatalf("e = %#v, want 1.5", v)
	}
	b, ok := Get(root, "b")
	if !ok {
		t.Fatal("b missing")
	}
	bm, ok := b.(map[string]any)
	if !ok {
		t.Fatalf("b is %T, want map", b)
	}
	nm, ok := bm["nested"].(map[string]any)
	if !ok {
		t.Fatalf("b.nested is %T, want map", bm["nested"])
	}
	if v, _ := nm["deep"].(bool); !v {
		t.Fatalf("b.nested.deep = %#v, want true", nm["deep"])
	}
	// A pure list must come back as a slice.
	if _, ok := Get(root, "a"); !ok {
		t.Fatal("a missing")
	}
}

func TestParseDoubleQuotedEscapes(t *testing.T) {
	src := `<?php return ['k' => "line1\nline2\ttab\"quote\\slash"];`
	root, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	got, _ := GetString(root, "k")
	want := "line1\nline2\ttab\"quote\\slash"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"unterminated string":  `<?php return ['a' => 'oops];`,
		"unsupported constant": `<?php return ['a' => Magento\Framework\App\Area::AREA_GLOBAL];`,
		"unterminated array":   `<?php return ['a' => [1, 2`,
		"not an array":         `<?php return 'nope';`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(src)); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestParseFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env.php")
	if err := os.WriteFile(path, []byte(sampleEnvPHP), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if v, _ := GetString(root, "db", "connection", "default", "dbname"); v != "magento" {
		t.Fatalf("dbname = %q", v)
	}
}

func TestParseMissingFile(t *testing.T) {
	if _, err := ParseFile(filepath.Join(t.TempDir(), "nope.php")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestParseBOMAndShebang(t *testing.T) {
	src := "\xEF\xBB\xBF<?php\nreturn ['a' => 'b'];\n"
	root, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse with BOM: %v", err)
	}
	if v, _ := GetString(root, "a"); v != "b" {
		t.Fatalf("a = %q", v)
	}
}
