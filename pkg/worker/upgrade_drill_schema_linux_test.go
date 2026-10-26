//go:build linux

package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var retiredByMigration = map[string]string{
	"card_cracks":                    "0044_remove_run_cards.sql",
	"card_rarity":                    "0044_remove_run_cards.sql",
	"card_comments":                  "0044_remove_run_cards.sql",
	"pull_requests.kpis":             "0044_remove_run_cards.sql",
	"pull_requests.kpis_computed_at": "0044_remove_run_cards.sql",
	"pull_requests.shape":            "0044_remove_run_cards.sql",
}

var rewrittenByMigration = map[string]string{
	"work_items.state": `CASE WHEN state = 'leased' THEN 'active' ELSE state END`,
}

type schemaColumns map[string][]string

func publicColumns(t *testing.T, pool *pgxpool.Pool) schemaColumns {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT c.table_name, c.column_name
		FROM information_schema.columns c
		JOIN information_schema.tables tb ON tb.table_schema = c.table_schema AND tb.table_name = c.table_name
		WHERE c.table_schema = 'public' AND tb.table_type = 'BASE TABLE' AND c.table_name <> 'schema_migrations'
		ORDER BY c.table_name, c.ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := schemaColumns{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatal(err)
		}
		columns[table] = append(columns[table], column)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}

func survivingColumns(t *testing.T, before, after schemaColumns) schemaColumns {
	t.Helper()
	surviving := schemaColumns{}
	for table, columns := range before {
		kept, exists := after[table]
		if !exists {
			if retiredByMigration[table] == "" {
				t.Errorf("the upgrade dropped table %s, which no migration retires on record", table)
			}
			continue
		}
		have := map[string]bool{}
		for _, column := range kept {
			have[column] = true
		}
		for _, column := range columns {
			if have[column] {
				surviving[table] = append(surviving[table], column)
			} else if retiredByMigration[table+"."+column] == "" {
				t.Errorf("the upgrade dropped column %s.%s, which no migration retires on record", table, column)
			}
		}
	}
	return surviving
}

type tableRows map[string]string

func selectList(table string, columns []string, rewrites map[string]string) string {
	parts := make([]string, len(columns))
	for i, column := range columns {
		ident := pgx.Identifier{column}.Sanitize()
		if rewrite, ok := rewrites[table+"."+column]; ok {
			parts[i] = rewrite + " AS " + ident
		} else {
			parts[i] = ident
		}
	}
	return strings.Join(parts, ", ")
}

func snapshotTable(t *testing.T, pool *pgxpool.Pool, table string, columns []string, rewrites map[string]string, where string) string {
	t.Helper()
	query := `SELECT COALESCE(jsonb_agg(r ORDER BY r::text), '[]'::jsonb)::text FROM (SELECT to_jsonb(x) AS r FROM (SELECT ` +
		selectList(table, columns, rewrites) + ` FROM ` + pgx.Identifier{table}.Sanitize() + ` ` + where + `) x) s`
	var rows string
	if err := pool.QueryRow(context.Background(), query).Scan(&rows); err != nil {
		t.Fatalf("snapshot %s: %v", table, err)
	}
	return rows
}

func snapshotRows(t *testing.T, pool *pgxpool.Pool, columns schemaColumns, rewrites map[string]string) tableRows {
	t.Helper()
	snapshot := tableRows{}
	for table, cols := range columns {
		snapshot[table] = snapshotTable(t, pool, table, cols, rewrites, "")
	}
	return snapshot
}

func projectRows(t *testing.T, rows string, keep []string) []string {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(rows))
	decoder.UseNumber()
	var decoded []map[string]any
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("rows %q: %v", rows, err)
	}
	projected := make([]string, len(decoded))
	for i, row := range decoded {
		kept := map[string]any{}
		for _, column := range keep {
			kept[column] = row[column]
		}
		var line bytes.Buffer
		encoder := json.NewEncoder(&line)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(kept); err != nil {
			t.Fatal(err)
		}
		projected[i] = strings.TrimSpace(line.String())
	}
	sort.Strings(projected)
	return projected
}

func equalProjection(t *testing.T, what string, want, got tableRows, columns schemaColumns) bool {
	t.Helper()
	same := true
	tables := make([]string, 0, len(columns))
	for table := range columns {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		wantRows := projectRows(t, want[table], columns[table])
		gotRows := projectRows(t, got[table], columns[table])
		if strings.Join(wantRows, "\n") != strings.Join(gotRows, "\n") {
			t.Errorf("%s: %s differs:\nwant %s\ngot  %s", what, table, strings.Join(wantRows, "\n     "), strings.Join(gotRows, "\n     "))
			same = false
		}
	}
	return same
}

func appliedMigrations(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT name FROM schema_migrations ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func schemaFingerprint(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var fingerprint string
	if err := pool.QueryRow(context.Background(), `SELECT string_agg(line, E'\n' ORDER BY line) FROM (
		SELECT 'column ' || table_name || '.' || column_name || ' ' || data_type || ' ' || is_nullable || ' ' || COALESCE(column_default, '') AS line
			FROM information_schema.columns WHERE table_schema = 'public'
		UNION ALL SELECT 'constraint ' || conrelid::regclass::text || ' ' || conname || ' ' || pg_get_constraintdef(oid)
			FROM pg_constraint WHERE connamespace = 'public'::regnamespace
		UNION ALL SELECT 'index ' || indexdef FROM pg_indexes WHERE schemaname = 'public'
		UNION ALL SELECT 'trigger ' || tgrelid::regclass::text || ' ' || pg_get_triggerdef(oid) FROM pg_trigger WHERE NOT tgisinternal
		UNION ALL SELECT 'function ' || proname || ' ' || md5(prosrc) FROM pg_proc WHERE pronamespace = 'public'::regnamespace
		UNION ALL SELECT 'view ' || viewname || ' ' || md5(definition) FROM pg_views WHERE schemaname = 'public'
	) catalog`).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

func sameNames(a, b []string) bool {
	return strings.Join(a, ",") == strings.Join(b, ",")
}
