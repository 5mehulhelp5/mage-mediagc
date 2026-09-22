package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shuaiZend/mage-mediagc/internal/phpconfig"
)

// EnvPHP is the subset of Magento's app/etc/env.php that mage-mediagc needs.
type EnvPHP struct {
	Path        string
	DBHost      string
	DBPort      int
	DBName      string
	DBUser      string
	DBPassword  string
	DBSocket    string
	TablePrefix string
}

// EnvPHPRelPath is where Magento keeps its environment configuration.
var EnvPHPRelPath = filepath.Join("app", "etc", "env.php")

// LoadEnvPHP extracts database credentials from <root>/app/etc/env.php.
//
// It accepts both the `[...]` and `array(...)` literal styles. Anything it
// cannot interpret produces an error naming the file, so operators are never
// left wondering why credentials silently came back empty.
func LoadEnvPHP(magentoRoot string) (*EnvPHP, error) {
	path := filepath.Join(magentoRoot, EnvPHPRelPath)
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	root, err := phpconfig.ParseFile(path)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	out := &EnvPHP{Path: path}
	if v, ok := phpconfig.GetString(root, "db", "table_prefix"); ok {
		out.TablePrefix = v
	}

	conn, ok := phpconfig.GetMap(root, "db", "connection")
	if !ok {
		return out, fmt.Errorf("%s: no db.connection section found", path)
	}

	// Prefer the "default" connection; fall back to the first one present so
	// unusual setups still resolve.
	selected, ok := conn["default"].(map[string]any)
	if !ok {
		for _, v := range conn {
			if m, ok := v.(map[string]any); ok {
				selected = m
				break
			}
		}
	}
	if selected == nil {
		return out, fmt.Errorf("%s: db.connection has no usable entry", path)
	}

	host, _ := selected["host"].(string)
	host, port := splitHostPort(host)
	out.DBHost = host
	out.DBPort = port

	if v, ok := selected["port"].(int64); ok && v > 0 {
		out.DBPort = int(v)
	}
	out.DBName, _ = selected["dbname"].(string)
	out.DBUser, _ = selected["username"].(string)
	out.DBPassword, _ = selected["password"].(string)
	out.DBSocket, _ = selected["unix_socket"].(string)

	return out, nil
}

// splitHostPort handles the shapes Magento accepts for the host field:
// "localhost", "10.0.0.5", "10.0.0.5:3307", "mysql://user@host/db".
func splitHostPort(raw string) (string, int) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0
	}
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			host := u.Hostname()
			port := 0
			if p := u.Port(); p != "" {
				port, _ = strconv.Atoi(p)
			}
			return host, port
		}
	}
	if i := strings.LastIndex(raw, ":"); i > 0 && !strings.Contains(raw[i+1:], "]") {
		if p, err := strconv.Atoi(raw[i+1:]); err == nil {
			return raw[:i], p
		}
	}
	return raw, 0
}
