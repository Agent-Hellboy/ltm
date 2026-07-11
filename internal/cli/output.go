package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"ltm/internal/abi"
	"ltm/internal/diff"
	"ltm/internal/query"
	"ltm/internal/storage"
)

func printRootHelp(w io.Writer) {
	fmt.Fprintln(w, "ltm - Linux Time Machine")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  ltm [global flags] <command> [command flags]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  start      begin recording (eBPF; requires root)")
	fmt.Fprintln(w, "  stop")
	fmt.Fprintln(w, "  status")
	fmt.Fprintln(w, "  timeline   [--since] [--until] [--pid] [--uid] [--comm] [--category] [--action] [--path] [--exe] [--limit]")
	fmt.Fprintln(w, "  diff")
	fmt.Fprintln(w, "  watch      [--interval] [--since] [--category] [--comm] [--pid]  (live tail)")
	fmt.Fprintln(w, "  query      \"<plain English question>\" [--agent claude|codex|cursor|gemini|auto]  (env LTM_AGENT)")
	fmt.Fprintln(w, "  query sql  [\"<SELECT ...>\"]  (run with no query to print the schema; ltm sql also works)")
	fmt.Fprintln(w, "  prune      [--older-than]")
	fmt.Fprintln(w, "  benchmark")
	fmt.Fprintln(w, "  version")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Global flags (before the command):")
	fmt.Fprintln(w, "  --db <path>           storage database (default ~/.local/share/ltm/ltm.db)")
	fmt.Fprintln(w, "  --pidfile <path>      pid file (default ~/.local/run/ltm.pid)")
	fmt.Fprintln(w, "  --json                machine-readable output for read commands")
	fmt.Fprintln(w, "  --ignore-path <p>     extra path prefix to skip while recording (repeatable)")
}

func printVersion(w io.Writer, jsonOut bool) error {
	info := map[string]string{
		"version": Version,
		"commit":  Commit,
		"date":    Date,
		"go":      runtime.Version(),
		"os":      runtime.GOOS,
		"arch":    runtime.GOARCH,
	}
	if jsonOut {
		return writeJSON(w, info)
	}
	fmt.Fprintf(w, "ltm %s\n", Version)
	fmt.Fprintf(w, "  commit:  %s\n", Commit)
	fmt.Fprintf(w, "  built:   %s\n", Date)
	fmt.Fprintf(w, "  go:      %s\n", runtime.Version())
	fmt.Fprintf(w, "  platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	return nil
}

const sqlSchemaHelp = `ltm query sql - run read-only SQL against the events table.

Usage:
  ltm query sql "<SELECT ...>"
  ltm query --json sql "<SELECT ...>"

` + abi.SchemaDoc + `

Examples:
  ltm query sql "SELECT comm, count(*) n FROM events GROUP BY comm ORDER BY n DESC LIMIT 10"
  ltm query sql "SELECT datetime(ts/1e9,'unixepoch') ts, path, comm FROM events WHERE category='file' AND action='write' AND path LIKE '/etc/%'"
  ltm query sql "SELECT * FROM events WHERE pid = 1234 ORDER BY ts"
  ltm query sql "SELECT comm, remote_addr, remote_port FROM events WHERE category='network' AND action='connect' AND ts > (unixepoch()-3600)*1000000000"

The connection runs with PRAGMA query_only=ON: INSERT/UPDATE/DELETE/DDL will fail.
`

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func formatEvent(ev storage.Event) string {
	line := fmt.Sprintf("%s %-8s %-8s pid=%d ppid=%d uid=%d comm=%s",
		ev.Timestamp.Format(time.RFC3339),
		ev.Category,
		ev.Action,
		ev.PID,
		ev.PPID,
		ev.UID,
		ev.Comm,
	)
	if ev.Path != "" {
		line += " path=" + ev.Path
	}
	if ev.OldPath != "" {
		line += " old_path=" + ev.OldPath
	}
	if ev.LocalPort != 0 {
		line += fmt.Sprintf(" local=%s:%d", ev.LocalAddr, ev.LocalPort)
	}
	if ev.RemotePort != 0 {
		line += fmt.Sprintf(" remote=%s:%d", ev.RemoteAddr, ev.RemotePort)
	}
	return line
}

func printEvents(w io.Writer, events []storage.Event) error {
	for _, ev := range events {
		if _, err := io.WriteString(w, formatEvent(ev)+"\n"); err != nil {
			return err
		}
	}
	return nil
}

func printDiff(w io.Writer, report diff.DiffReport) error {
	fmt.Fprintf(w, "Diff %s -> %s\n", report.From.Format(time.RFC3339), report.To.Format(time.RFC3339))
	fmt.Fprintf(w, "new processes: %d exited: %d modified files: %d deleted files: %d\n",
		len(report.NewProcesses), len(report.ExitedProcesses), len(report.ModifiedFiles), len(report.DeletedFiles))
	fmt.Fprintf(w, "new listeners: %d outbound connections: %d hot writers: %d restarts: %d\n",
		len(report.NewListeners), len(report.OutboundConnections), len(report.HotWriters), len(report.Restarts))
	if len(report.ModifiedFiles) > 0 {
		fmt.Fprintln(w, "modified files:")
		for _, f := range report.ModifiedFiles {
			fmt.Fprintf(w, "  %s path=%s count=%d action=%s\n", f.Timestamp.Format(time.RFC3339), f.Path, f.Count, f.Action)
		}
	}
	if len(report.NewProcesses) > 0 {
		fmt.Fprintln(w, "new processes:")
		for _, p := range report.NewProcesses {
			fmt.Fprintf(w, "  %s pid=%d comm=%s path=%s action=%s\n", p.Timestamp.Format(time.RFC3339), p.PID, p.Comm, p.Path, p.Action)
		}
	}
	if len(report.NewListeners) > 0 {
		fmt.Fprintln(w, "new listeners:")
		for _, s := range report.NewListeners {
			fmt.Fprintf(w, "  %s pid=%d comm=%s socket=%s action=%s\n", s.Timestamp.Format(time.RFC3339), s.PID, s.Comm, s.Socket, s.Action)
		}
	}
	if len(report.OutboundConnections) > 0 {
		fmt.Fprintln(w, "outbound connections:")
		for _, s := range report.OutboundConnections {
			fmt.Fprintf(w, "  %s pid=%d comm=%s socket=%s action=%s\n", s.Timestamp.Format(time.RFC3339), s.PID, s.Comm, s.Socket, s.Action)
		}
	}
	if len(report.HotWriters) > 0 {
		fmt.Fprintln(w, "hot writers:")
		for _, hw := range report.HotWriters {
			fmt.Fprintf(w, "  %s pid=%d comm=%s path=%s count=%d\n", hw.Timestamp.Format(time.RFC3339), hw.PID, hw.Comm, hw.Path, hw.Count)
		}
	}
	if len(report.Restarts) > 0 {
		fmt.Fprintln(w, "restarts:")
		for _, r := range report.Restarts {
			fmt.Fprintf(w, "  %s pid=%d comm=%s\n", r.Timestamp.Format(time.RFC3339), r.PID, r.Comm)
		}
	}
	return nil
}

func printQueryResult(w io.Writer, result query.Result) error {
	fmt.Fprintln(w, result.Title)
	for _, row := range result.Rows {
		fmt.Fprintln(w, "- "+row)
	}
	return nil
}

func printSQLTable(w io.Writer, cols []string, rows [][]any) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(cols, "\t"))
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, v := range row {
			cells[i] = formatSQLValue(v)
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	return tw.Flush()
}

func formatSQLValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}
