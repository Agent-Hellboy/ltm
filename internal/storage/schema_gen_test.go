package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ltm/internal/abi"
)

// TestSchemaMatchesGenerated is the schema-side ground-truth check. For every
// persisted table it reads the live columns with PRAGMA table_info and asserts
// they are exactly the generated column-list constant and that abi.SchemaDoc
// documents the same ordered list. Because all of these derive from abi.yaml, a
// mismatch means the generator outputs disagree or the generated files are
// stale — run `go generate ./internal/abi/`.
func TestSchemaMatchesGenerated(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "ltm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	docByTable := schemaDocColumns(abi.SchemaDoc)
	tables := map[string]string{
		"events":          eventColumns,
		"system_samples":  systemSamplesColumns,
		"process_samples": processSamplesColumns,
	}
	for table, genColumns := range tables {
		live := liveColumns(t, store, table)
		if strings.Join(live, ", ") != genColumns {
			t.Fatalf("%s: live columns disagree with generated constant\n live: %v\n gen:  %v", table, live, strings.Split(genColumns, ", "))
		}
		if !slices.Equal(live, docByTable[table]) {
			t.Fatalf("%s: abi.SchemaDoc columns disagree with live table\n live: %v\n doc:  %v", table, live, docByTable[table])
		}
	}
}

func liveColumns(t *testing.T, store *Store, table string) []string {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), `PRAGMA table_info(`+table+`)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var live []string
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		live = append(live, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return live
}

// schemaDocColumns parses abi.SchemaDoc into per-table ordered column lists.
// Sections begin with a "Table: <name>" line; column lines are indented two
// spaces; the "Indexes:" line ends a section.
func schemaDocColumns(doc string) map[string][]string {
	out := make(map[string][]string)
	var table string
	for _, line := range strings.Split(doc, "\n") {
		if rest, ok := strings.CutPrefix(line, "Table: "); ok {
			table = strings.Fields(rest)[0] // name, before any " (doc)"
			continue
		}
		if !strings.HasPrefix(line, "  ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || table == "" {
			continue
		}
		out[table] = append(out[table], fields[0])
	}
	return out
}
