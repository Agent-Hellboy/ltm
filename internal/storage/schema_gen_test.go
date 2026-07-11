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

// TestSchemaMatchesGenerated is the schema-side ground-truth check. It creates a
// real store, reads the live table's columns with PRAGMA table_info, and asserts
// they are exactly the generated eventColumns and that abi.SchemaDoc documents
// the same ordered column list. Because all of these derive from abi.yaml, a
// mismatch means the generator outputs disagree or the generated files are
// stale — run `go generate ./internal/abi/`.
func TestSchemaMatchesGenerated(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "ltm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	rows, err := store.db.QueryContext(context.Background(), `PRAGMA table_info(events)`)
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

	want := strings.Split(eventColumns, ", ")
	if strings.Join(live, ", ") != eventColumns {
		t.Fatalf("live table columns disagree with generated eventColumns\n live: %v\n gen:  %v", live, want)
	}

	docCols := schemaDocColumns(abi.SchemaDoc)
	if !slices.Equal(live, docCols) {
		t.Fatalf("abi.SchemaDoc columns disagree with live table\n live: %v\n doc:  %v", live, docCols)
	}
}

func schemaDocColumns(doc string) []string {
	lines := strings.Split(doc, "\n")
	cols := make([]string, 0, len(lines))
	for _, line := range lines {
		if !strings.HasPrefix(line, "  ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		cols = append(cols, fields[0])
	}
	return cols
}
