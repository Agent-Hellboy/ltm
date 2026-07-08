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
	"strconv"
	"strings"
	"syscall"
	"time"

	"ltm/daemon"
	"ltm/diff"
	"ltm/query"
	"ltm/storage"
)

type Config struct {
	DBPath      string
	PIDFile     string
	Mode        string
	JSON        bool
	IgnorePaths []string
}

func defaultConfig() Config {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "."
	}
	return Config{
		DBPath:  filepath.Join(home, ".local", "share", "ltm", "ltm.log"),
		PIDFile: filepath.Join(home, ".local", "run", "ltm.pid"),
		Mode:    "demo",
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
	args, err := parseGlobalFlags(args, &cfg)
	if err != nil {
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
	case "diff":
		return runDiff(cfg, args[1:])
	case "query":
		return runQuery(cfg, args[1:])
	case "benchmark":
		return runBenchmark(cfg, args[1:])
	case "daemon":
		return runDaemon(cfg, args[1:])
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

func runStart(cfg Config, args []string) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	mode := fs.String("mode", "demo", "collector mode: demo or ebpf")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg.Mode = *mode
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
	cmd := exec.Command(exe, "--db", cfg.DBPath, "--pidfile", cfg.PIDFile, "daemon", "--foreground", "--mode", cfg.Mode)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := os.WriteFile(cfg.PIDFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	fmt.Fprintf(os.Stdout, "ltm started pid=%d mode=%s db=%s\n", cmd.Process.Pid, cfg.Mode, cfg.DBPath)
	return nil
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
	store, err := storage.Open(cfg.DBPath)
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
	limit := fs.Int("limit", 200, "maximum number of events")
	jsonOut := fs.Bool("json", cfg.JSON, "json output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := storage.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()
	sinceTime, err := parseDurationOrTime(*since, time.Now())
	if err != nil {
		return err
	}
	events, err := store.EventsSince(context.Background(), sinceTime, *limit)
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeJSON(os.Stdout, events)
	}
	return printEvents(os.Stdout, events)
}

func runDiff(cfg Config, args []string) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	from := fs.String("from", "1h", "start time")
	to := fs.String("to", "now", "end time")
	jsonOut := fs.Bool("json", cfg.JSON, "json output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := storage.Open(cfg.DBPath)
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	question := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if question == "" {
		return errors.New("query requires a question")
	}
	store, err := storage.Open(cfg.DBPath)
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

func runBenchmark(cfg Config, args []string) error {
	fs := flag.NewFlagSet("benchmark", flag.ContinueOnError)
	count := fs.Int("count", 1000, "number of demo events")
	if err := fs.Parse(args); err != nil {
		return err
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
	throughput := float64(len(events)) / elapsed.Seconds()
	fmt.Fprintf(os.Stdout, "events/sec=%.0f dropped=%d db_write_latency_ms=%d\n", throughput, stats.Dropped, stats.WriteLatency.Milliseconds())
	return nil
}

func runDaemon(cfg Config, args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	foreground := fs.Bool("foreground", false, "run in foreground")
	mode := fs.String("mode", cfg.Mode, "collector mode")
	dbPath := fs.String("db", cfg.DBPath, "storage path")
	pidFile := fs.String("pidfile", cfg.PIDFile, "pid file path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*foreground {
		return errors.New("daemon requires --foreground")
	}
	cfg.Mode = *mode
	cfg.DBPath = *dbPath
	cfg.PIDFile = *pidFile
	store, err := storage.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()
	svc := daemon.NewService(store, daemon.Config{
		Mode:        cfg.Mode,
		IgnorePaths: cfg.IgnorePaths,
	})
	ctx, cancel := signalContext()
	defer cancel()
	return svc.Run(ctx)
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signalNotify(ch)
	go func() {
		<-ch
		cancel()
	}()
	return ctx, func() {
		signalStop(ch)
		cancel()
	}
}

func printRootHelp(w io.Writer) {
	fmt.Fprintln(w, "ltm - Linux Time Machine")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  ltm [global flags] <command> [command flags]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  start")
	fmt.Fprintln(w, "  stop")
	fmt.Fprintln(w, "  status")
	fmt.Fprintln(w, "  timeline")
	fmt.Fprintln(w, "  diff")
	fmt.Fprintln(w, "  query")
	fmt.Fprintln(w, "  benchmark")
}

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

func printEvents(w io.Writer, events []storage.Event) error {
	for _, ev := range events {
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
		line += "\n"
		if _, err := io.WriteString(w, line); err != nil {
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
	if result.Summary != "" {
		fmt.Fprintln(w, result.Summary)
	}
	for _, row := range result.Rows {
		fmt.Fprintln(w, "- "+row)
	}
	return nil
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
	return proc.Signal(syscall.Signal(0)) == nil
}

// signal helpers are split so tests can stub them if needed.
var signalNotify = func(ch chan<- os.Signal) {
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
}

var signalStop = func(ch chan<- os.Signal) {
	signal.Stop(ch)
}
