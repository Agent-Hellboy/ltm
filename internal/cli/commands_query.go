package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"ltm/internal/agent"
	"ltm/internal/diff"
	"ltm/internal/query"
	"ltm/internal/storage"
)

func runDiff(cfg Config, args []string) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	from := fs.String("from", "1h", "start time")
	to := fs.String("to", "now", "end time")
	jsonOut := fs.Bool("json", cfg.JSON, "json output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := storage.OpenReadOnly(cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()
	start, err := parseDurationOrTime(*from, time.Now())
	if err != nil {
		return err
	}
	end, err := parseDurationOrTime(*to, time.Now())
	if err != nil {
		return err
	}
	engine := diff.NewEngine(store)
	report, err := engine.Diff(context.Background(), start, end)
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeJSON(os.Stdout, report)
	}
	return printDiff(os.Stdout, report)
}

func runQuery(cfg Config, args []string) error {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	jsonOut := fs.Bool("json", cfg.JSON, "json output")
	agentSpec := fs.String("agent", os.Getenv("LTM_AGENT"),
		"agent CLI for plain-English questions: claude, codex, cursor, gemini, auto, or a custom command (env LTM_AGENT)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()

	// `ltm query sql "<SELECT ...>"` runs exact SQL; no query prints the schema.
	if len(rest) > 0 && rest[0] == "sql" {
		sqlText := strings.TrimSpace(strings.Join(rest[1:], " "))
		if sqlText == "" {
			_, err := io.WriteString(os.Stdout, sqlSchemaHelp)
			return err
		}
		return execReadOnlySQL(cfg, sqlText, *jsonOut)
	}

	question := strings.TrimSpace(strings.Join(rest, " "))
	if question == "" {
		return errors.New(`query requires a question or: query sql "<SELECT ...>"`)
	}

	if a, err := agent.Resolve(*agentSpec); err != nil {
		return err
	} else if a != nil {
		if err := runAgentQuery(cfg, a, question, *jsonOut); err == nil {
			return nil
		} else {
			fmt.Fprintf(os.Stderr, "warning: %v; falling back to built-in templates\n", err)
		}
	}

	store, err := storage.OpenReadOnly(cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()
	engine := query.NewEngine(store)
	result, err := engine.Execute(context.Background(), question)
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeJSON(os.Stdout, result)
	}
	return printQueryResult(os.Stdout, result)
}

func runAgentQuery(cfg Config, a *agent.Agent, question string, jsonOut bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sqlText, err := a.GenerateSQL(ctx, question)
	if err != nil {
		return err
	}
	store, err := storage.OpenReadOnly(cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()
	cols, rows, err := store.RawSQL(ctx, sqlText)
	if err != nil {
		return fmt.Errorf("agent %s produced invalid SQL (%v): %s", a.Name, err, sqlText)
	}
	if jsonOut {
		return writeJSON(os.Stdout, map[string]any{
			"question": question,
			"agent":    a.Name,
			"sql":      sqlText,
			"rows":     rowsToMaps(cols, rows),
		})
	}
	fmt.Fprintf(os.Stderr, "[%s] %s\n", a.Name, sqlText)
	return printSQLTable(os.Stdout, cols, rows)
}

// runSQL keeps `ltm sql` as a shorthand for `ltm query sql`.
func runSQL(cfg Config, args []string) error {
	fs := flag.NewFlagSet("sql", flag.ContinueOnError)
	jsonOut := fs.Bool("json", cfg.JSON, "json output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		_, err := io.WriteString(os.Stdout, sqlSchemaHelp)
		return err
	}
	return execReadOnlySQL(cfg, query, *jsonOut)
}

func execReadOnlySQL(cfg Config, query string, jsonOut bool) error {
	store, err := storage.OpenReadOnly(cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()
	cols, rows, err := store.RawSQL(context.Background(), query)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(os.Stdout, rowsToMaps(cols, rows))
	}
	return printSQLTable(os.Stdout, cols, rows)
}

func rowsToMaps(cols []string, rows [][]any) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			m[c] = row[i]
		}
		out = append(out, m)
	}
	return out
}

func parseDurationOrTime(input string, now time.Time) (time.Time, error) {
	if input == "" {
		return time.Time{}, errors.New("empty time")
	}
	if input == "now" {
		return now, nil
	}
	if d, err := time.ParseDuration(input); err == nil {
		return now.Add(-d), nil
	}
	layouts := []string{
		"2006-01-02 15:04",
		"2006-01-02 15:04:05",
		time.RFC3339,
		time.RFC3339Nano,
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, input, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unable to parse time %q", input)
}
