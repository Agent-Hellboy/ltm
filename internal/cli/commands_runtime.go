package cli

import (
	"context"
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
	"sync"
	"syscall"
	"time"

	"ltm/internal/daemon"
	"ltm/internal/storage"
)

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
