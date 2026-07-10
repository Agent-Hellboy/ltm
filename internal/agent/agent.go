// Package agent shells out to a locally installed coding agent CLI (Claude
// Code, Codex, Cursor, Gemini, or any custom command) to translate a plain
// English question into a single read-only SQL statement over the ltm schema.
package agent

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"unicode"

	"ltm/internal/storage"
)

// Agent is a resolved external command that can answer a translation prompt.
type Agent struct {
	Name string
	argv []string
}

// knownAgents maps friendly names to the non-interactive invocation of each
// CLI. The prompt is always appended as the final argument.
var knownAgents = map[string][]string{
	"claude": {"claude", "-p"},
	"codex":  {"codex", "exec"},
	"cursor": {"cursor-agent", "-p"},
	"gemini": {"gemini", "-p"},
}

// autoOrder is the detection order for Resolve("auto").
var autoOrder = []string{"claude", "codex", "cursor", "gemini"}

// Resolve turns an agent spec into a runnable Agent. Specs:
//   - "" returns (nil, nil): no agent configured.
//   - "claude" | "codex" | "cursor" | "gemini": a known CLI, which must be on PATH.
//   - "auto": the first known CLI found on PATH.
//   - anything else: a custom command line, split on whitespace; the prompt is
//     appended as the final argument.
func Resolve(spec string) (*Agent, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	if spec == "auto" {
		for _, name := range autoOrder {
			argv := knownAgents[name]
			if _, err := exec.LookPath(argv[0]); err == nil {
				return &Agent{Name: name, argv: argv}, nil
			}
		}
		return nil, fmt.Errorf("no known agent CLI found on PATH (looked for: %s)", strings.Join(autoOrder, ", "))
	}
	if argv, ok := knownAgents[spec]; ok {
		if _, err := exec.LookPath(argv[0]); err != nil {
			return nil, fmt.Errorf("agent %q: %q not found on PATH", spec, argv[0])
		}
		return &Agent{Name: spec, argv: argv}, nil
	}
	argv := strings.Fields(spec)
	if _, err := exec.LookPath(argv[0]); err != nil {
		return nil, fmt.Errorf("agent command %q not found on PATH", argv[0])
	}
	return &Agent{Name: argv[0], argv: argv}, nil
}

// GenerateSQL asks the agent to translate a question into one SELECT statement.
func (a *Agent) GenerateSQL(ctx context.Context, question string) (string, error) {
	prompt := buildPrompt(question)
	args := append(append([]string{}, a.argv[1:]...), prompt)
	cmd := exec.CommandContext(ctx, a.argv[0], args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
		return "", fmt.Errorf("agent %s failed: %w: %s", a.Name, err, msg)
	}
	sql, err := ExtractSQL(stdout.String())
	if err != nil {
		return "", fmt.Errorf("agent %s: %w", a.Name, err)
	}
	return sql, nil
}

func buildPrompt(question string) string {
	return `Translate the question below into exactly one SQLite SELECT statement over this schema:

` + storage.SchemaDoc + `

Rules:
- Output only the SQL statement. No markdown fences, no explanation, no commentary.
- It must be a single SELECT (or WITH ... SELECT). The database is read-only.
- ts is unix nanoseconds; the current time is (unixepoch()*1000000000).
- Prefer ORDER BY ts DESC and add LIMIT 200 unless the question implies otherwise.

Question: ` + question
}

var reSQLStart = regexp.MustCompile(`(?is)\b(select|with)\b`)

// ExtractSQL pulls a single SELECT statement out of agent output, tolerating
// markdown fences and surrounding prose, and rejects anything that is not a
// SELECT / WITH ... SELECT.
func ExtractSQL(out string) (string, error) {
	s := strings.TrimSpace(out)
	if _, rest, ok := strings.Cut(s, "```"); ok {
		if tagLine, body, hasNL := strings.Cut(rest, "\n"); hasNL {
			tag := strings.ToLower(strings.TrimSpace(tagLine))
			if tag == "" || tag == "sql" || tag == "sqlite" {
				rest = body
			}
		}
		if fenced, _, closed := strings.Cut(rest, "```"); closed {
			s = strings.TrimSpace(fenced)
		}
	}
	loc := reSQLStart.FindStringIndex(s)
	if loc == nil {
		return "", fmt.Errorf("no SELECT statement in agent output: %.200s", s)
	}
	sql := strings.TrimSpace(s[loc[0]:])
	sql = strings.TrimSuffix(sql, "```")
	sql = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sql), ";"))
	if err := validateReadOnlySQL(sql); err != nil {
		return "", err
	}
	return sql, nil
}

// validateReadOnlySQL rejects multi-statement output and non-SELECT top-level
// statements (e.g. WITH ... DELETE). OpenReadOnly is the real write guard.
func validateReadOnlySQL(sql string) error {
	upper := strings.ToUpper(strings.TrimLeftFunc(sql, unicode.IsSpace))
	starters, multi := scanTopLevel(sql)
	if multi {
		return fmt.Errorf("agent output contains multiple statements: %.200s", sql)
	}
	switch {
	case strings.HasPrefix(upper, "SELECT"):
		return nil
	case strings.HasPrefix(upper, "WITH"):
		// CTE bodies are parenthesized; the first depth-0 statement starter
		// is the main statement and must be SELECT.
		if len(starters) == 0 || starters[0] != "SELECT" {
			return fmt.Errorf("agent output contains a non-SELECT statement: %.200s", sql)
		}
		return nil
	default:
		return fmt.Errorf("agent output is not a SELECT statement: %.200s", sql)
	}
}

// scanTopLevel walks sql once, returning depth-0 statement starters
// (SELECT/INSERT/UPDATE/DELETE/REPLACE) and whether a statement separator
// appears outside quotes/comments.
func scanTopLevel(sql string) (starters []string, multi bool) {
	depth := 0
	for i := 0; i < len(sql); {
		switch c := sql[i]; {
		case c == '\'':
			i = skipQuoted(sql, i+1, '\'')
		case c == '"':
			i = skipQuoted(sql, i+1, '"')
		case c == '[':
			i = skipUntil(sql, i+1, ']')
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			i += 2
			for i+1 < len(sql) && !(sql[i] == '*' && sql[i+1] == '/') {
				i++
			}
			if i+1 < len(sql) {
				i += 2
			}
		case c == '(':
			depth++
			i++
		case c == ')':
			if depth > 0 {
				depth--
			}
			i++
		case c == ';':
			return starters, true
		case depth == 0 && isIdentStart(c):
			start := i
			for i < len(sql) && isIdentPart(sql[i]) {
				i++
			}
			switch strings.ToUpper(sql[start:i]) {
			case "SELECT", "INSERT", "UPDATE", "DELETE", "REPLACE":
				starters = append(starters, strings.ToUpper(sql[start:i]))
			}
		default:
			i++
		}
	}
	return starters, false
}

func skipQuoted(sql string, i int, quote byte) int {
	for i < len(sql) {
		if sql[i] == quote {
			if i+1 < len(sql) && sql[i+1] == quote {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return len(sql)
}

func skipUntil(sql string, i int, end byte) int {
	for i < len(sql) && sql[i] != end {
		i++
	}
	if i < len(sql) {
		return i + 1
	}
	return len(sql)
}

func isIdentStart(b byte) bool {
	return b == '_' || ('A' <= b && b <= 'Z') || ('a' <= b && b <= 'z')
}

func isIdentPart(b byte) bool {
	return isIdentStart(b) || ('0' <= b && b <= '9')
}
