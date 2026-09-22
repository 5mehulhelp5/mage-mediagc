// Package magento talks to a Magento 2 database: it resolves the reference
// sources that decide whether a media file is still in use, and it exposes
// the orphan statistics and cleanup operations for the EAV tables that
// accumulate stale rows when products are deleted outside of the admin UI.
package magento

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql" // MySQL driver

	"github.com/shuaiZend/mage-mediagc/internal/config"
)

// Client wraps a MySQL connection pool with Magento-aware helpers.
type Client struct {
	db     *sql.DB
	dbName string
	prefix string
}

// Open connects to MySQL and verifies the connection is usable.
func Open(ctx context.Context, cfg config.DB, tablePrefix string) (*Client, error) {
	db, err := sql.Open("mysql", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(5 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to %s: %w", cfg.RedactedDSN(), err)
	}
	return &Client{db: db, dbName: cfg.Name, prefix: tablePrefix}, nil
}

// Close releases the connection pool.
func (c *Client) Close() error { return c.db.Close() }

// DB exposes the underlying pool for advanced queries.
func (c *Client) DB() *sql.DB { return c.db }

// Table applies the configured table prefix.
func (c *Client) Table(name string) string { return c.prefix + name }

// Version returns the MySQL server version string.
func (c *Client) Version(ctx context.Context) (string, error) {
	var v string
	if err := c.db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&v); err != nil {
		return "", err
	}
	return v, nil
}

// TableExists reports whether a table is present, which keeps the tool
// usable against trimmed-down schemas and third-party installs.
func (c *Client) TableExists(ctx context.Context, table string) (bool, error) {
	var n int
	q := `SELECT COUNT(*) FROM information_schema.tables
	      WHERE table_schema = DATABASE() AND table_name = ?`
	if err := c.db.QueryRowContext(ctx, q, c.Table(table)).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// CountRows returns the row count of a table, or -1 when it does not exist.
func (c *Client) CountRows(ctx context.Context, table string) (int64, error) {
	ok, err := c.TableExists(ctx, table)
	if err != nil {
		return 0, err
	}
	if !ok {
		return -1, nil
	}
	var n int64
	if err := c.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+c.Table(table)+"`").Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// EntityTypeID resolves an EAV entity type code such as "catalog_product".
//
// Attribute lookups go through the entity type rather than a hard-coded
// entity_type_id, because that numeric id differs between installations.
func (c *Client) EntityTypeID(ctx context.Context, code string) (int64, error) {
	var id int64
	q := "SELECT entity_type_id FROM `" + c.Table("eav_entity_type") + "` WHERE entity_type_code = ?"
	err := c.db.QueryRowContext(ctx, q, code).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("entity type %q not found: is this a Magento database?", code)
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

// AttributeIDs maps attribute codes to their numeric ids for an entity type.
// Missing codes are simply absent from the result, so callers can degrade
// gracefully on trimmed schemas.
func (c *Client) AttributeIDs(ctx context.Context, entityTypeCode string, codes []string) (map[string]int64, error) {
	out := make(map[string]int64, len(codes))
	if len(codes) == 0 {
		return out, nil
	}
	typeID, err := c.EntityTypeID(ctx, entityTypeCode)
	if err != nil {
		return nil, err
	}
	args := make([]any, 0, len(codes)+1)
	args = append(args, typeID)
	for _, code := range codes {
		args = append(args, code)
	}
	q := "SELECT attribute_id, attribute_code FROM `" + c.Table("eav_attribute") + "`" +
		" WHERE entity_type_id = ? AND attribute_code IN (" + placeholders(len(codes)) + ")"
	rows, err := c.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var code string
		if err := rows.Scan(&id, &code); err != nil {
			return nil, err
		}
		out[code] = id
	}
	return out, rows.Err()
}

// ColumnExists reports whether a column exists, used to adapt queries to
// schema variations across Magento versions.
func (c *Client) ColumnExists(ctx context.Context, table, column string) (bool, error) {
	var n int
	q := `SELECT COUNT(*) FROM information_schema.columns
	      WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`
	if err := c.db.QueryRowContext(ctx, q, c.Table(table), column).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// quoteIdent makes identifiers safe to interpolate into SQL. Identifiers
// cannot be parameterised in MySQL, and Magento table names are always
// [a-z0-9_], so anything else is rejected.
func quoteIdent(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("empty identifier")
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
		if !ok {
			return "", fmt.Errorf("unsafe identifier %q", name)
		}
	}
	return "`" + name + "`", nil
}
