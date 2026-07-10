package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExtractSQL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{
			name: "bare statement",
			in:   "SELECT * FROM events LIMIT 10",
			want: "SELECT * FROM events LIMIT 10",
		},
		{
			name: "trailing semicolon stripped",
			in:   "select comm from events;",
			want: "select comm from events",
		},
		{
			name: "sql fence",
			in:   "Here you go:\n```sql\nSELECT comm FROM events ORDER BY ts DESC\n```\nThat should work.",
			want: "SELECT comm FROM events ORDER BY ts DESC",
		},
		{
			name: "plain fence",
			in:   "```\nSELECT 1\n```",
			want: "SELECT 1",
		},
		{
			name: "prose before statement",
			in:   "The query is: SELECT path FROM events WHERE category='file'",
			want: "SELECT path FROM events WHERE category='file'",
		},
		{
			name: "with clause",
			in:   "WITH recent AS (SELECT * FROM events) SELECT comm FROM recent",
			want: "WITH recent AS (SELECT * FROM events) SELECT comm FROM recent",
		},
		{
			name: "with clause containing write word in string",
			in:   "WITH deleted AS (SELECT 'delete' AS action) SELECT action FROM deleted",
			want: "WITH deleted AS (SELECT 'delete' AS action) SELECT action FROM deleted",
		},
		{
			name:    "no sql at all",
			in:      "I cannot answer that question.",
			wantErr: true,
		},
		{
			name:    "not a select",
			in:      "DELETE FROM events",
			wantErr: true,
		},
		{
			name:    "multiple statements",
			in:      "SELECT 1; DROP TABLE events",
			wantErr: true,
		},
		{
			name:    "mutating verb hidden in WITH cte",
			in:      "WITH x AS (SELECT 1) DELETE FROM events",
			wantErr: true,
		},
		{
			name: "write word in string literal",
			in:   "SELECT path FROM events WHERE action = 'delete'",
			want: "SELECT path FROM events WHERE action = 'delete'",
		},
		{
			name: "read only function with write verb name",
			in:   "SELECT replace(comm, 'a', 'b') FROM events",
			want: "SELECT replace(comm, 'a', 'b') FROM events",
		},
		{
			name: "semicolon in string literal",
			in:   "SELECT ';' AS semicolon FROM events",
			want: "SELECT ';' AS semicolon FROM events",
		},
		{
			name: "semicolon in line comment",
			in:   "SELECT count(*) FROM events -- ; ignored in comment",
			want: "SELECT count(*) FROM events -- ; ignored in comment",
		},
		{
			name:    "with update main statement",
			in:      "WITH x AS (SELECT 1) UPDATE events SET comm = 'x'",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExtractSQL(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ExtractSQL(%q) = %q, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExtractSQL(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ExtractSQL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()
	if a, err := Resolve(""); err != nil || a != nil {
		t.Fatalf("Resolve(\"\") = %v, %v; want nil, nil", a, err)
	}
	if _, err := Resolve("definitely-not-a-real-binary-xyz"); err == nil {
		t.Fatal("expected error for unknown custom command")
	}
	// "echo" exists everywhere; custom specs pass through with args preserved.
	a, err := Resolve("echo --flag")
	if err != nil {
		t.Fatalf("Resolve custom: %v", err)
	}
	if a.Name != "echo" || len(a.argv) != 2 {
		t.Fatalf("Resolve custom = %+v, want echo with one arg", a)
	}
}

func TestGenerateSQLWithFakeAgent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-agent")
	// Ignores the prompt and answers with fenced SQL like a chatty agent would.
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'Sure! Here is the query:'\necho '```sql'\necho \"SELECT comm, count(*) FROM events GROUP BY comm\"\necho '```'\n"), 0o755); err != nil {
		t.Fatalf("write fake agent: %v", err)
	}

	a, err := Resolve(script)
	if err != nil {
		t.Fatalf("resolve fake agent: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sql, err := a.GenerateSQL(ctx, "which processes are busiest?")
	if err != nil {
		t.Fatalf("GenerateSQL: %v", err)
	}
	if sql != "SELECT comm, count(*) FROM events GROUP BY comm" {
		t.Fatalf("GenerateSQL = %q", sql)
	}
}

func TestGenerateSQLAgentFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	script := filepath.Join(dir, "broken-agent")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'boom' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write broken agent: %v", err)
	}
	a, err := Resolve(script)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := a.GenerateSQL(ctx, "anything"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("GenerateSQL error = %v, want failure mentioning stderr", err)
	}
}
