package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"ltm/internal/abi"
	"ltm/internal/agent"
	"ltm/internal/daemon"
	"ltm/internal/diff"
	"ltm/internal/query"
	"ltm/internal/storage"
)

// Build information, overridden at link time via -ldflags -X (see .goreleaser.yaml).
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

type Config struct {
	DBPath      string
	PIDFile     string
	JSON        bool
	IgnorePaths []string
}

func defaultConfig() Config {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "."
	}
	return Config{
		DBPath:  filepath.Join(home, ".local", "share", "ltm", "ltm.db"),
		PIDFile: filepath.Join(home, ".local", "run", "ltm.pid"),
		IgnorePaths: []string{
			"/proc",
			"/sys",
			"/dev",
			filepath.Join(home, ".cache"),
			filepath.Join(home, "Library", "Caches"),
		},
	}
}

func Execute() error {
	cfg := defaultConfig()
	args := os.Args[1:]
	// Handle `-v`/`--version` up front: the global flag parser would otherwise
	// reject them as undefined flags before dispatch sees them.
	if len(args) > 0 && (args[0] == "-v" || args[0] == "--version") {
		return printVersion(os.Stdout, cfg.JSON)
	}
	args, err := parseGlobalFlags(args, &cfg)
	if err != nil {
		// `-h`/`--help` before a subcommand surfaces as flag.ErrHelp.
		if errors.Is(err, flag.ErrHelp) {
			printRootHelp(os.Stdout)
			return nil
		}
		return err
	}
	if len(args) == 0 {
		printRootHelp(os.Stdout)
		return nil
	}
	switch args[0] {
	case "start":
		return runStart(cfg, args[1:])
	case "stop":
		return runStop(cfg, args[1:])
	case "status":
		return runStatus(cfg, args[1:])
	case "timeline":
		return runTimeline(cfg, args[1:])
	case "watch":
		return runWatch(cfg, args[1:])
	case "diff":
		return runDiff(cfg, args[1:])
	case "query":
		return runQuery(cfg, args[1:])
	case "benchmark":
		return runBenchmark(cfg, args[1:])
	case "daemon":
		return runDaemon(cfg, args[1:])
	case "sql":
		return runSQL(cfg, args[1:])
	case "prune":
		return runPrune(cfg, args[1:])
	case "version", "-v", "--version":
		return printVersion(os.Stdout, cfg.JSON)
	case "-h", "--help", "help":
		printRootHelp(os.Stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func parseGlobalFlags(args []string, cfg *Config) ([]string, error) {
	fs := flag.NewFlagSet("ltm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.DBPath, "db", cfg.DBPath, "storage path")
	fs.StringVar(&cfg.PIDFile, "pidfile", cfg.PIDFile, "pid file path")
	fs.BoolVar(&cfg.JSON, "json", false, "json output")
	ignore := multiStringFlag{values: append([]string{}, cfg.IgnorePaths...)}
	fs.Var(&ignore, "ignore-path", "path prefix to ignore")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	cfg.IgnorePaths = ignore.values
	return fs.Args(), nil
}

type multiStringFlag struct {
	values []string
}

func (m *multiStringFlag) String() string {
	return strings.Join(m.values, ",")
}

func (m *multiStringFlag) Set(v string) error {
	m.values = append(m.values, v)
	return nil
}

type multiIntFlag struct {
	values []int
}

func (m *multiIntFlag) String() string {
	parts := make([]string, len(m.values))
	for i, v := range m.values {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ",")
}

func (m *multiIntFlag) Set(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	m.values = append(m.values, n)
	return nil
}

func runStart(cfg Config, args []string) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.PIDFile), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, daemonArgs(cfg)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	// Detach into its own session so the recorder keeps running after the
	// launching shell exits (otherwise it takes SIGHUP on terminal close).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := os.WriteFile(cfg.PIDFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	fmt.Fprintf(os.Stdout, "ltm started pid=%d db=%s\n", cmd.Process.Pid, cfg.DBPath)
	return nil
}

func daemonArgs(cfg Config) []string {
	args := []string{"--db", cfg.DBPath, "--pidfile", cfg.PIDFile}
	for _, path := range customIgnorePaths(cfg.IgnorePaths) {
		args = append(args, "--ignore-path", path)
	}
	return append(args, "daemon", "--foreground")
}

func customIgnorePaths(paths []string) []string {
	defaultSet := sliceToSet(defaultConfig().IgnorePaths)
	var out []string
	for _, path := range paths {
		if !defaultSet[path] {
			out = append(out, path)
		}
	}
	return out
}

func runStop(cfg Config, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("stop takes no arguments")
	}
	pid, err := readPIDFile(cfg.PIDFile)
	if err != nil {
		return err
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "ltm stopped pid=%d\n", pid)
	return nil
}

func runStatus(cfg Config, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	jsonOut := fs.Bool("json", cfg.JSON, "json output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := storage.OpenReadOnly(cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()
	status, err := store.Status(context.Background())
	if err != nil {
		return err
	}
	pid, _ := readPIDFile(cfg.PIDFile)
	alive := pid > 0 && processAlive(pid)
	if *jsonOut {
		return writeJSON(os.Stdout, map[string]any{
			"pid":    pid,
			"alive":  alive,
			"status": status,
		})
	}
	fmt.Fprintf(os.Stdout, "daemon: alive=%t pid=%d\n", alive, pid)
	fmt.Fprintf(os.Stdout, "events=%d dropped=%d last_event=%s\n", status.EventCount, status.DroppedEvents, status.LastEventTime.Format(time.RFC3339))
	return nil
}

func runTimeline(cfg Config, args []string) error {
	fs := flag.NewFlagSet("timeline", flag.ContinueOnError)
	since := fs.String("since", "1h", "show events since duration or absolute time")
	until := fs.String("until", "now", "show events until duration or absolute time")
	limit := fs.Int("limit", 200, "maximum number of events")
	jsonOut := fs.Bool("json", cfg.JSON, "json output")
	var pids, uids multiIntFlag
	var categories, actions, comms multiStringFlag
	pathLike := fs.String("path", "", "filter by path (SQL LIKE pattern, % wildcard)")
	exeLike := fs.String("exe", "", "filter by exe (SQL LIKE pattern, % wildcard)")
	fs.Var(&pids, "pid", "filter by pid (repeatable)")
	fs.Var(&uids, "uid", "filter by uid (repeatable)")
	fs.Var(&categories, "category", "filter by category (repeatable)")
	fs.Var(&actions, "action", "filter by action (repeatable)")
	fs.Var(&comms, "comm", "filter by process name (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := storage.OpenReadOnly(cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()
	now := time.Now()
	sinceTime, err := parseDurationOrTime(*since, now)
	if err != nil {
		return err
	}
	untilTime, err := parseDurationOrTime(*until, now)
	if err != nil {
		return err
	}
	events, err := store.Query(context.Background(), storage.Filter{
		From:       sinceTime,
		To:         untilTime,
		PIDs:       pids.values,
		UIDs:       uids.values,
		Categories: categories.values,
		Actions:    actions.values,
		Comms:      comms.values,
		PathLike:   *pathLike,
		ExeLike:    *exeLike,
		Limit:      *limit,
	})
	if err != nil {
		return err
	}
	// timeline reads newest-first from Query; display oldest-first.
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	if *jsonOut {
		return writeJSON(os.Stdout, events)
	}
	return printEvents(os.Stdout, events)
}

func runWatch(cfg Config, args []string) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	interval := fs.Duration("interval", time.Second, "poll interval")
	since := fs.String("since", "", "backfill events since this duration/time before tailing (default: only new events)")
	limit := fs.Int("limit", 500, "maximum events fetched per poll")
	var comms, categories multiStringFlag
	var pids multiIntFlag
	fs.Var(&comms, "comm", "only show this process name (repeatable)")
	fs.Var(&pids, "pid", "only show this pid (repeatable)")
	fs.Var(&categories, "category", "only show this category (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := storage.OpenReadOnly(cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()

	match := watchPredicate(categories.values, comms.values, pids.values)
	ctx, cancel := signalContext()
	defer cancel()

	var lastID int64
	if *since != "" {
		start, err := parseDurationOrTime(*since, time.Now())
		if err != nil {
			return err
		}
		events, err := store.EventsBetween(ctx, start, time.Now(), *limit)
		if err != nil {
			return err
		}
		for _, ev := range events {
			if match(ev) {
				fmt.Fprintln(os.Stdout, formatEvent(ev))
			}
			if ev.ID > lastID {
				lastID = ev.ID
			}
		}
	}
	if lastID == 0 {
		if lastID, err = store.LatestEventID(ctx); err != nil {
			return err
		}
	}

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			lastID, err = watchStep(ctx, store, lastID, *limit, match, os.Stdout)
			if err != nil {
				return err
			}
		}
	}
}

// watchStep fetches events newer than lastID, prints those matching the
// predicate, and returns the new high-water id. Split out from runWatch so it
// can be tested without tickers or signals.
func watchStep(ctx context.Context, store *storage.Store, lastID int64, limit int, match func(storage.Event) bool, w io.Writer) (int64, error) {
	events, err := store.EventsAfterID(ctx, lastID, limit)
	if err != nil {
		return lastID, err
	}
	for _, ev := range events {
		if match(ev) {
			if _, err := io.WriteString(w, formatEvent(ev)+"\n"); err != nil {
				return lastID, err
			}
		}
		if ev.ID > lastID {
			lastID = ev.ID
		}
	}
	return lastID, nil
}

func watchPredicate(categories, comms []string, pids []int) func(storage.Event) bool {
	catSet := sliceToSet(categories)
	commSet := sliceToSet(comms)
	pidSet := make(map[int]bool, len(pids))
	for _, p := range pids {
		pidSet[p] = true
	}
	return func(ev storage.Event) bool {
		if len(catSet) > 0 && !catSet[ev.Category] {
			return false
		}
		if len(commSet) > 0 && !commSet[ev.Comm] {
			return false
		}
		if len(pidSet) > 0 && !pidSet[ev.PID] {
			return false
		}
		return true
	}
}

func sliceToSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[item] = true
	}
	return set
}

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

func runBenchmark(cfg Config, args []string) error {
	fs := flag.NewFlagSet("benchmark", flag.ContinueOnError)
	count := fs.Int("count", 1000, "number of synthetic events")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *count < 0 {
		return errors.New("benchmark count must be >= 0")
	}
	store, err := storage.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()
	events := storage.GenerateDemoEvents(time.Now(), *count)
	start := time.Now()
	stats, err := store.InsertEvents(context.Background(), events)
	if err != nil {
		return err
	}
	elapsed := time.Since(start)
	var throughput float64
	if elapsed > 0 {
		throughput = float64(len(events)) / elapsed.Seconds()
	}
	fmt.Fprintf(os.Stdout, "events/sec=%.0f dropped=%d db_write_latency_ms=%d\n", throughput, stats.Dropped, stats.WriteLatency.Milliseconds())
	return nil
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

func runPrune(cfg Config, args []string) error {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	olderThan := fs.String("older-than", "720h", "delete events older than this duration or absolute time")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := storage.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()
	cutoff, err := parseDurationOrTime(*olderThan, time.Now())
	if err != nil {
		return err
	}
	n, err := store.Prune(context.Background(), cutoff)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "pruned %d events older than %s\n", n, cutoff.Format(time.RFC3339))
	return nil
}

func runDaemon(cfg Config, args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	foreground := fs.Bool("foreground", false, "run in foreground")
	dbPath := fs.String("db", cfg.DBPath, "storage path")
	pidFile := fs.String("pidfile", cfg.PIDFile, "pid file path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*foreground {
		return errors.New("daemon requires --foreground")
	}
	cfg.DBPath = *dbPath
	cfg.PIDFile = *pidFile
	store, err := storage.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()
	svc := daemon.NewService(store, daemon.Config{
		IgnorePaths: cfg.IgnorePaths,
	})
	ctx, cancel := signalContext()
	defer cancel()
	return svc.Run(ctx)
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	done := make(chan struct{})
	var once sync.Once
	signalNotify(ch)
	go func() {
		select {
		case <-ch:
			cancel()
		case <-done:
		}
	}()
	return ctx, func() {
		once.Do(func() {
			signalStop(ch)
			close(done)
			cancel()
		})
	}
}

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

func readPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

// signal helpers are split so tests can stub them if needed.
var signalNotify = func(ch chan<- os.Signal) {
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
}

var signalStop = func(ch chan<- os.Signal) {
	signal.Stop(ch)
}
