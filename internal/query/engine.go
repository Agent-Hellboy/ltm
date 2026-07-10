package query

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ltm/internal/storage"
)

type Engine struct {
	store *storage.Store
}

func NewEngine(store *storage.Store) *Engine {
	return &Engine{store: store}
}

type Result struct {
	Title   string   `json:"title"`
	Summary string   `json:"summary,omitempty"`
	Rows    []string `json:"rows"`
}

var (
	reWhoModified = regexp.MustCompile(`(?i)^who modified (.+?)\??$`)
	rePid         = regexp.MustCompile(`(?i)\bpid\s+(\d+)\b`)
	rePort        = regexp.MustCompile(`(?i)\bport\s+(\d+)\b`)
	reIP          = regexp.MustCompile(`(?i)\b(\d+\.\d+\.\d+\.\d+)\b`)
)

func (e *Engine) Execute(ctx context.Context, question string) (Result, error) {
	q := strings.TrimSpace(question)
	lq := strings.ToLower(q)
	switch {
	case reWhoModified.MatchString(q):
		m := reWhoModified.FindStringSubmatch(q)
		path := strings.TrimSpace(m[1])
		events, err := e.store.EventsByPath(ctx, path, 200)
		if err != nil {
			return Result{}, err
		}
		rows := make([]string, 0, len(events))
		for _, ev := range events {
			if ev.Category == "file" && (ev.Action == "write" || ev.Action == "rename" || ev.Action == "unlink") {
				rows = append(rows, fmt.Sprintf("%s pid=%d comm=%s action=%s", ev.Timestamp.Format(time.RFC3339), ev.PID, ev.Comm, ev.Action))
			}
		}
		return Result{Title: fmt.Sprintf("who modified %s", path), Rows: rows}, nil
	case strings.Contains(lq, "restarted") && strings.Contains(lq, "before"):
		return e.queryRestart(ctx, q)
	case strings.Contains(lq, "connected to") || strings.Contains(lq, "connected"):
		return e.queryConnection(ctx, q)
	case strings.Contains(lq, "opened port"):
		return e.queryPort(ctx, q)
	case strings.Contains(lq, "show activity for pid"):
		return e.queryPID(ctx, q)
	case strings.Contains(lq, "show activity for file"):
		return e.queryFile(ctx, q)
	default:
		terms := strings.Fields(strings.TrimSpace(q))
		events, err := e.store.QueryText(ctx, terms, 50)
		if err != nil {
			return Result{}, err
		}
		rows := make([]string, 0, len(events))
		for _, ev := range events {
			rows = append(rows, fmt.Sprintf("%s %s pid=%d %s %s", ev.Timestamp.Format(time.RFC3339), ev.Category, ev.PID, ev.Action, ev.Path))
		}
		return Result{Title: "matched timeline", Rows: rows}, nil
	}
}

func (e *Engine) queryRestart(ctx context.Context, q string) (Result, error) {
	// Show the process start/stop events that make up a restart. If the
	// question names a process, narrow to it; otherwise show all.
	comm := restartComm(q)
	f := storage.Filter{
		Categories: []string{"process"},
		Actions:    []string{"exec", "exit", "fork", "clone"},
		Limit:      200,
	}
	if comm != "" {
		f.Comms = []string{comm}
	}
	events, err := e.store.Query(ctx, f)
	if err != nil {
		return Result{}, err
	}
	rows := make([]string, 0, len(events))
	for _, ev := range events {
		if ev.Action == "exit" {
			rows = append(rows, fmt.Sprintf("%s process exited pid=%d comm=%s", ev.Timestamp.Format(time.RFC3339), ev.PID, ev.Comm))
		} else {
			rows = append(rows, fmt.Sprintf("%s process started pid=%d comm=%s exe=%s", ev.Timestamp.Format(time.RFC3339), ev.PID, ev.Comm, ev.Exe))
		}
	}
	title := "process restart window"
	if comm != "" {
		title = fmt.Sprintf("restart window for %s", comm)
	}
	return Result{Title: title, Rows: rows}, nil
}

// restartComm pulls a likely process name out of "what changed before X
// restarted?"-style questions, ignoring common filler words.
func restartComm(q string) string {
	stop := map[string]bool{
		"what": true, "changed": true, "before": true, "after": true, "the": true,
		"restarted": true, "restart": true, "was": true, "did": true, "when": true,
		"why": true, "process": true, "service": true, "a": true, "is": true,
	}
	for w := range strings.FieldsSeq(strings.ToLower(q)) {
		w = strings.Trim(w, "?.,'\"")
		if w != "" && !stop[w] {
			return w
		}
	}
	return ""
}

func (e *Engine) queryConnection(ctx context.Context, q string) (Result, error) {
	if ip := reIP.FindStringSubmatch(q); len(ip) == 2 {
		events, err := e.store.Query(ctx, storage.Filter{
			Categories: []string{"network"},
			Actions:    []string{"connect"},
			Limit:      200,
		})
		if err != nil {
			return Result{}, err
		}
		rows := make([]string, 0)
		for _, ev := range events {
			if ev.RemoteAddr != ip[1] && !strings.Contains(strings.ToLower(ev.RemoteHost), strings.ToLower(ip[1])) {
				continue
			}
			rows = append(rows, fmt.Sprintf("%s pid=%d comm=%s -> %s:%d", ev.Timestamp.Format(time.RFC3339), ev.PID, ev.Comm, ev.RemoteAddr, ev.RemotePort))
		}
		return Result{Title: "matching connections", Rows: rows}, nil
	}
	// Hostname (or no target): list connects, optionally filtered by a host
	// token appearing in remote_host/remote_addr.
	host := connectionHost(q)
	events, err := e.store.Query(ctx, storage.Filter{
		Categories: []string{"network"},
		Actions:    []string{"connect"},
		Limit:      200,
	})
	if err != nil {
		return Result{}, err
	}
	rows := make([]string, 0)
	for _, ev := range events {
		if host != "" &&
			!strings.Contains(strings.ToLower(ev.RemoteHost), host) &&
			!strings.Contains(ev.RemoteAddr, host) {
			continue
		}
		rows = append(rows, fmt.Sprintf("%s pid=%d comm=%s -> %s:%d", ev.Timestamp.Format(time.RFC3339), ev.PID, ev.Comm, ev.RemoteAddr, ev.RemotePort))
	}
	return Result{Title: "matching connections", Rows: rows}, nil
}

// connectionHost extracts the target after "connected to" (e.g. a hostname).
func connectionHost(q string) string {
	_, after, ok := strings.Cut(strings.ToLower(q), "connected to ")
	if !ok {
		return ""
	}
	rest := strings.Trim(strings.TrimSpace(after), "?.,'\"")
	if i := strings.IndexByte(rest, ' '); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

func (e *Engine) queryPort(ctx context.Context, q string) (Result, error) {
	m := rePort.FindStringSubmatch(q)
	if len(m) != 2 {
		return Result{Title: "opened port", Rows: nil}, nil
	}
	port, _ := strconv.Atoi(m[1])
	events, err := e.store.Sockets(ctx, 500)
	if err != nil {
		return Result{}, err
	}
	rows := make([]string, 0)
	for _, ev := range events {
		if ev.LocalPort == port && (ev.State == "listen" || ev.State == "bind") {
			rows = append(rows, fmt.Sprintf("%s pid=%d comm=%s port=%d", ev.SeenAt.Format(time.RFC3339), ev.PID, ev.Comm, port))
		}
	}
	return Result{Title: fmt.Sprintf("processes that opened port %d", port), Rows: rows}, nil
}

func (e *Engine) queryPID(ctx context.Context, q string) (Result, error) {
	m := rePid.FindStringSubmatch(q)
	if len(m) != 2 {
		return Result{}, nil
	}
	pid, _ := strconv.Atoi(m[1])
	events, err := e.store.EventsByPID(ctx, pid, 200)
	if err != nil {
		return Result{}, err
	}
	rows := make([]string, 0, len(events))
	for _, ev := range events {
		rows = append(rows, fmt.Sprintf("%s %s %s", ev.Timestamp.Format(time.RFC3339), ev.Category, ev.Action))
	}
	return Result{Title: fmt.Sprintf("activity for pid %d", pid), Rows: rows}, nil
}

func (e *Engine) queryFile(ctx context.Context, q string) (Result, error) {
	parts := strings.SplitN(q, "file", 2)
	if len(parts) != 2 {
		return Result{}, nil
	}
	path := strings.TrimSpace(parts[1])
	path = strings.Trim(path, "\"")
	events, err := e.store.EventsByPath(ctx, path, 200)
	if err != nil {
		return Result{}, err
	}
	rows := make([]string, 0, len(events))
	for _, ev := range events {
		rows = append(rows, fmt.Sprintf("%s pid=%d comm=%s action=%s path=%s", ev.Timestamp.Format(time.RFC3339), ev.PID, ev.Comm, ev.Action, ev.Path))
	}
	return Result{Title: fmt.Sprintf("activity for file %s", path), Rows: rows}, nil
}
