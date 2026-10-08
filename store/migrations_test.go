package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

// emptyMigrationProvider resets the test database to an empty public schema
// and returns a raw connection to it and a goose provider over the embedded
// migrations, none applied. Skips when no test database is set.
func emptyMigrationProvider(t *testing.T) (*sql.DB, *goose.Provider) {
	t.Helper()
	dsn := os.Getenv(testDSNVar)
	if dsn == "" {
		t.Skipf("%s not set", testDSNVar)
	}
	ctx := context.Background()
	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open raw connection: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	if _, err := raw.ExecContext(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatalf("embedded migrations: %v", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, raw, files)
	if err != nil {
		t.Fatalf("migration provider: %v", err)
	}
	return raw, provider
}

// schemaQueries each read one kind of object in the public schema from the
// Postgres catalog, one line per object, so two schemas compare as sets of
// lines. Definitions are Postgres's own rendering of what it stored, not the
// migration's text.
var schemaQueries = []string{
	// Table columns, with type and default. Not position: a Down that drops
	// and re-adds a column puts it last.
	`SELECT format('column %s.%s %s default %s', c.relname, a.attname,
		format_type(a.atttypid, a.atttypmod), coalesce(pg_get_expr(d.adbin, d.adrelid), 'none'))
	FROM pg_attribute a
	JOIN pg_class c ON c.oid = a.attrelid
	JOIN pg_namespace n ON n.oid = c.relnamespace
	LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
	WHERE n.nspname = 'public' AND c.relkind = 'r'
		AND a.attnum > 0 AND NOT a.attisdropped`,
	// Indexes, including those behind a primary key or unique constraint.
	`SELECT format('index %s', indexdef) FROM pg_indexes WHERE schemaname = 'public'`,
	// Enum types, with their labels in order.
	`SELECT format('enum %s %s', t.typname, string_agg(e.enumlabel, ', ' ORDER BY e.enumsortorder))
	FROM pg_type t
	JOIN pg_namespace n ON n.oid = t.typnamespace
	JOIN pg_enum e ON e.enumtypid = t.oid
	WHERE n.nspname = 'public'
	GROUP BY t.typname`,
	// Domains, with base type and default. Their checks are constraints.
	`SELECT format('domain %s %s default %s', t.typname,
		format_type(t.typbasetype, t.typtypmod), coalesce(t.typdefault, 'none'))
	FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
	WHERE n.nspname = 'public' AND t.typtype = 'd'`,
	// Constraints on tables and on domains. Postgres 18 keeps a column's or
	// a domain's NOT NULL as one of these, so nullability is here too.
	`SELECT format('constraint %s %s %s',
		CASE WHEN con.conrelid <> 0 THEN con.conrelid::regclass::text ELSE con.contypid::regtype::text END,
		con.conname, pg_get_constraintdef(con.oid))
	FROM pg_constraint con JOIN pg_namespace n ON n.oid = con.connamespace
	WHERE n.nspname = 'public'`,
}

// schemaOf reads the public schema as a sorted list of lines.
func schemaOf(t *testing.T, raw *sql.DB) []string {
	t.Helper()
	var lines []string
	for _, query := range schemaQueries {
		got, err := queryAll(context.Background(), raw, func(row scanner) (string, error) {
			var line string
			err := row.Scan(&line)
			return line, err
		}, query)
		if err != nil {
			t.Fatalf("read schema: %v", err)
		}
		lines = append(lines, got...)
	}
	slices.Sort(lines)
	return lines
}

// schemaDiff lists the lines only in before, marked -, then the lines only
// in after, marked +. Empty means the two schemas match.
func schemaDiff(before, after []string) string {
	var b strings.Builder
	for _, line := range before {
		if !slices.Contains(after, line) {
			fmt.Fprintf(&b, "- %s\n", line)
		}
	}
	for _, line := range after {
		if !slices.Contains(before, line) {
			fmt.Fprintf(&b, "+ %s\n", line)
		}
	}
	return b.String()
}

// #479: every migration's Down undoes its Up. A rollback to an older release
// leaves the newer schema in place, so someone runs the Downs by hand during
// the incident. Each migration runs Up, Down, and Up again, in version order,
// so its Down meets the schema the migrations before it built, and the
// schema after its Down must match the schema before its Up.
func TestEveryMigrationDownRestoresTheSchemaBeforeItsUp(t *testing.T) {
	raw, provider := emptyMigrationProvider(t)
	ctx := context.Background()
	// goose creates its version table on its first call. Making that call
	// before the first snapshot keeps goose's own table out of the diff.
	if _, err := provider.GetDBVersion(ctx); err != nil {
		t.Fatalf("goose version table: %v", err)
	}
	for _, src := range provider.ListSources() {
		name := path.Base(src.Path)
		before := schemaOf(t, raw)
		if _, err := provider.ApplyVersion(ctx, src.Version, true); err != nil {
			t.Fatalf("Up of %s: %v", name, err)
		}
		if _, err := provider.ApplyVersion(ctx, src.Version, false); err != nil {
			t.Fatalf("Down of %s: %v", name, err)
		}
		if diff := schemaDiff(before, schemaOf(t, raw)); diff != "" {
			t.Errorf("Down of %s leaves the schema different from before its Up (- before, + after):\n%s", name, diff)
		}
		if _, err := provider.ApplyVersion(ctx, src.Version, true); err != nil {
			t.Fatalf("Up of %s again after its Down: %v", name, err)
		}
	}
}
