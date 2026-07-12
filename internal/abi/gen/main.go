// Command gen compiles abi.yaml — the single source of truth for the kernel
// capture contract and the persisted event schema — into Go. It emits:
//
//	tracepoints_gen.go          (package abi:     CollectorTracepoints)
//	schema_gen.go               (package abi:     SchemaVersion, SchemaDoc)
//	../storage/schema_gen.go    (package storage: DDL + column lists)
//
// Invoked by `go generate ./internal/abi/`; never edit the outputs by hand.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"go/format"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type tracepoint struct {
	Group    string `yaml:"group"`
	Event    string `yaml:"event"`
	Program  string `yaml:"program"`
	Optional bool   `yaml:"optional"`
}

type column struct {
	Name   string `yaml:"name"`
	Type   string `yaml:"type"`
	Doc    string `yaml:"doc"`
	Insert *bool  `yaml:"insert"` // pointer: absent means true
}

func (c column) inserted() bool { return c.Insert == nil || *c.Insert }

type index struct {
	Name    string   `yaml:"name"`
	Columns []string `yaml:"columns"`
}

type schema struct {
	Version  int      `yaml:"version"`
	Table    string   `yaml:"table"`
	TableDoc string   `yaml:"table_doc"`
	Columns  []column `yaml:"columns"`
	Indexes  []index  `yaml:"indexes"`
}

type kconst struct {
	Name  string `yaml:"name"`
	Value int    `yaml:"value"`
}

type kfield struct {
	Name  string `yaml:"name"`
	Type  string `yaml:"type"`
	Count string `yaml:"count"` // constant name or integer literal; empty means scalar
}

type kernelEvent struct {
	Struct    string   `yaml:"struct"`
	Constants []kconst `yaml:"constants"`
	Fields    []kfield `yaml:"fields"`
}

type abiDoc struct {
	Tracepoints []tracepoint `yaml:"tracepoints"`
	Schema      schema       `yaml:"schema"`
	KernelEvent kernelEvent  `yaml:"kernel_event"`
}

func main() {
	in := flag.String("in", "abi.yaml", "source YAML file")
	tpOut := flag.String("tp-out", "tracepoints_gen.go", "package abi: tracepoint table")
	schemaOut := flag.String("schema-out", "schema_gen.go", "package abi: schema version/doc")
	storeOut := flag.String("store-out", "../storage/schema_gen.go", "package storage: DDL + column lists")
	headerOut := flag.String("header-out", "kernel_event.gen.h", "C header: kernel event layout")
	verify := flag.Bool("verify", false,
		"check each generated file's embedded content hash instead of regenerating; "+
			"detects a hand edit that bypassed the generator, without needing abi.yaml or a Go/BPF toolchain")
	flag.Parse()

	if *verify {
		if err := verifyGenerated(*tpOut, *schemaOut, *storeOut, *headerOut); err != nil {
			fmt.Fprintln(os.Stderr, "abi/gen -verify:", err)
			os.Exit(1)
		}
		return
	}

	if err := run(*in, *tpOut, *schemaOut, *storeOut, *headerOut); err != nil {
		fmt.Fprintln(os.Stderr, "abi/gen:", err)
		os.Exit(1)
	}
}

func run(in, tpOut, schemaOut, storeOut, headerOut string) error {
	raw, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	var doc abiDoc
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true) // reject unknown keys so the YAML can't silently carry junk
	if err := dec.Decode(&doc); err != nil {
		return fmt.Errorf("parse %s: %w", in, err)
	}
	if err := validate(&doc); err != nil {
		return err
	}

	if err := writeGo(tpOut, tracepointsFile(in, doc.Tracepoints)); err != nil {
		return err
	}
	if err := writeGo(schemaOut, schemaABIFile(in, &doc.Schema)); err != nil {
		return err
	}
	if err := writeGo(storeOut, schemaStoreFile(in, &doc.Schema)); err != nil {
		return err
	}
	return os.WriteFile(headerOut, kernelEventHeader(in, &doc.KernelEvent), 0o644)
}

func validate(doc *abiDoc) error {
	if len(doc.Tracepoints) == 0 {
		return fmt.Errorf("no tracepoints declared")
	}
	seenProgram := make(map[string]bool, len(doc.Tracepoints))
	seenGroupEvent := make(map[string]bool, len(doc.Tracepoints))
	for i, tp := range doc.Tracepoints {
		if tp.Group == "" || tp.Event == "" || tp.Program == "" {
			return fmt.Errorf("tracepoint %d has an empty group/event/program", i)
		}
		if seenProgram[tp.Program] {
			return fmt.Errorf("duplicate program %q", tp.Program)
		}
		seenProgram[tp.Program] = true
		groupEvent := tp.Group + "\x00" + tp.Event
		if seenGroupEvent[groupEvent] {
			return fmt.Errorf("duplicate tracepoint %s/%s", tp.Group, tp.Event)
		}
		seenGroupEvent[groupEvent] = true
	}

	s := doc.Schema
	if s.Version <= 0 {
		return fmt.Errorf("schema.version must be positive, got %d", s.Version)
	}
	if s.Table == "" {
		return fmt.Errorf("empty schema.table")
	}
	if len(s.Columns) == 0 {
		return fmt.Errorf("schema declares no columns")
	}
	cols := make(map[string]bool, len(s.Columns))
	for _, c := range s.Columns {
		if c.Name == "" || c.Type == "" {
			return fmt.Errorf("column with empty name or type")
		}
		if cols[c.Name] {
			return fmt.Errorf("duplicate column %q", c.Name)
		}
		cols[c.Name] = true
	}
	seenIndex := make(map[string]bool, len(s.Indexes))
	for _, idx := range s.Indexes {
		if idx.Name == "" || len(idx.Columns) == 0 {
			return fmt.Errorf("index %q has no name or columns", idx.Name)
		}
		if seenIndex[idx.Name] {
			return fmt.Errorf("duplicate index %q", idx.Name)
		}
		seenIndex[idx.Name] = true
		for _, col := range idx.Columns {
			if !cols[col] {
				return fmt.Errorf("index %q references unknown column %q", idx.Name, col)
			}
		}
	}

	ke := doc.KernelEvent
	if ke.Struct == "" {
		return fmt.Errorf("empty kernel_event.struct")
	}
	if len(ke.Fields) == 0 {
		return fmt.Errorf("kernel_event declares no fields")
	}
	consts := make(map[string]int, len(ke.Constants))
	for _, c := range ke.Constants {
		if c.Name == "" {
			return fmt.Errorf("kernel_event constant with empty name")
		}
		if _, exists := consts[c.Name]; exists {
			return fmt.Errorf("duplicate kernel_event constant %q", c.Name)
		}
		consts[c.Name] = c.Value
	}
	fields := make(map[string]bool, len(ke.Fields))
	for _, f := range ke.Fields {
		if f.Name == "" || f.Type == "" {
			return fmt.Errorf("kernel_event field with empty name or type")
		}
		if fields[f.Name] {
			return fmt.Errorf("duplicate kernel_event field %q", f.Name)
		}
		fields[f.Name] = true
		if f.Count == "" {
			continue
		}
		// A count must resolve to a positive size: either a declared constant
		// (whose own value must be positive) or a positive integer literal.
		if value, ok := consts[f.Count]; ok {
			if value <= 0 {
				return fmt.Errorf("field %q count %q resolves to non-positive constant value %d", f.Name, f.Count, value)
			}
			continue
		}
		n, err := strconv.Atoi(f.Count)
		if err != nil {
			return fmt.Errorf("field %q count %q is neither a declared constant nor an integer", f.Name, f.Count)
		}
		if n <= 0 {
			return fmt.Errorf("field %q count %q must be positive, got %d", f.Name, f.Count, n)
		}
	}
	return nil
}

func tracepointsFile(in string, tps []tracepoint) []byte {
	var buf bytes.Buffer
	buf.WriteString("package abi\n\n")
	buf.WriteString("// TracepointSpec defines one loader-visible capture hook in the embedded BPF\n")
	buf.WriteString("// object contract.\n")
	buf.WriteString("type TracepointSpec struct {\n")
	buf.WriteString("\tGroup    string\n\tEvent    string\n\tProgram  string\n\tOptional bool\n}\n\n")
	buf.WriteString("// CollectorTracepoints is the tracepoint/program contract expected by the eBPF\n")
	buf.WriteString("// loader. It is generated from abi.yaml, the source of truth.\n")
	buf.WriteString("var CollectorTracepoints = []TracepointSpec{\n")
	for _, tp := range tps {
		fmt.Fprintf(&buf, "\t{Group: %q, Event: %q, Program: %q", tp.Group, tp.Event, tp.Program)
		if tp.Optional {
			buf.WriteString(", Optional: true")
		}
		buf.WriteString("},\n")
	}
	buf.WriteString("}\n")
	return prependGoHeader(in, buf.Bytes())
}

// schemaABIFile renders package abi: SchemaVersion and the human-readable SchemaDoc.
func schemaABIFile(in string, s *schema) []byte {
	width := 0
	for _, c := range s.Columns {
		if len(c.Name) > width {
			width = len(c.Name)
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Table: %s", s.Table)
	if s.TableDoc != "" {
		fmt.Fprintf(&sb, " (%s)", s.TableDoc)
	}
	sb.WriteString("\n")
	for _, c := range s.Columns {
		baseType := strings.Fields(c.Type)[0] // INTEGER / TEXT, not the full constraint
		fmt.Fprintf(&sb, "  %-*s  %-7s  %s\n", width, c.Name, baseType, c.Doc)
	}
	parts := make([]string, len(s.Indexes))
	for i, idx := range s.Indexes {
		if len(idx.Columns) == 1 {
			parts[i] = idx.Columns[0]
		} else {
			parts[i] = "(" + strings.Join(idx.Columns, ", ") + ")"
		}
	}
	fmt.Fprintf(&sb, "\nIndexes: %s", strings.Join(parts, ", "))

	var buf bytes.Buffer
	buf.WriteString("package abi\n\n")
	fmt.Fprintf(&buf, "const SchemaVersion = %d\n\n", s.Version)
	buf.WriteString("// SchemaDoc describes the queryable data model. It is shown by `ltm query sql`\n")
	buf.WriteString("// with no arguments and embedded in the prompt sent to a configured agent.\n")
	fmt.Fprintf(&buf, "const SchemaDoc = %s\n", backquote(sb.String()))
	return prependGoHeader(in, buf.Bytes())
}

// schemaStoreFile renders package storage: the DDL statements and column lists.
func schemaStoreFile(in string, s *schema) []byte {
	var ddl strings.Builder
	fmt.Fprintf(&ddl, "CREATE TABLE IF NOT EXISTS %s (\n", s.Table)
	for i, c := range s.Columns {
		comma := ","
		if i == len(s.Columns)-1 {
			comma = ""
		}
		fmt.Fprintf(&ddl, "\t%s %s%s\n", c.Name, c.Type, comma)
	}
	ddl.WriteString(")")

	var all, insert []string
	for _, c := range s.Columns {
		all = append(all, c.Name)
		if c.inserted() {
			insert = append(insert, c.Name)
		}
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(insert)), ", ")

	var buf bytes.Buffer
	buf.WriteString("package storage\n\n")
	buf.WriteString("var schemaStatements = []string{\n")
	fmt.Fprintf(&buf, "\t%s,\n", backquote(ddl.String()))
	for _, idx := range s.Indexes {
		stmt := fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s(%s)", idx.Name, s.Table, strings.Join(idx.Columns, ", "))
		fmt.Fprintf(&buf, "\t%s,\n", backquote(stmt))
	}
	buf.WriteString("}\n\n")
	fmt.Fprintf(&buf, "const eventColumns = %s\n\n", backquote(strings.Join(all, ", ")))
	fmt.Fprintf(&buf, "const insertColumns = %s\n\n", backquote(strings.Join(insert, ", ")))
	fmt.Fprintf(&buf, "const insertPlaceholders = %s\n", backquote(placeholders))
	return prependGoHeader(in, buf.Bytes())
}

// kernelEventHeader renders the C header describing the kernel->userspace wire
// layout. collector.bpf.c includes it; bpf2go then derives the Go struct from
// the compiled object's BTF, so this header is the single source for the layout.
func kernelEventHeader(in string, ke *kernelEvent) []byte {
	var buf bytes.Buffer
	buf.WriteString("#pragma once\n\n")
	buf.WriteString("/*\n")
	buf.WriteString(" * Kernel->userspace transport ABI for ltm capture events. Field order is the\n")
	buf.WriteString(" * on-wire byte order. The integer types (__u64 etc.) are provided by the\n")
	buf.WriteString(" * includer (headers/common.h) before this header.\n")
	buf.WriteString(" */\n\n")

	for _, c := range ke.Constants {
		fmt.Fprintf(&buf, "#define %s %d\n", c.Name, c.Value)
	}
	if len(ke.Constants) > 0 {
		buf.WriteString("\n")
	}

	fmt.Fprintf(&buf, "struct %s {\n", ke.Struct)
	for _, f := range ke.Fields {
		if f.Count == "" {
			fmt.Fprintf(&buf, "\t%s %s;\n", f.Type, f.Name)
		} else {
			fmt.Fprintf(&buf, "\t%s %s[%s];\n", f.Type, f.Name, f.Count)
		}
	}
	buf.WriteString("};\n")
	return prependCHeader(in, buf.Bytes())
}

// backquote renders s as a Go raw string literal. The schema/tracepoint text
// never contains a backtick, so raw literals are always safe here.
func backquote(s string) string {
	if strings.Contains(s, "`") {
		panic("generated text unexpectedly contains a backtick")
	}
	return "`" + s + "`"
}

// writeGo writes src as-is: prependGoHeader already ran the body through
// go/format before hashing it, and reformatting the header+body combination
// again here would risk producing on-disk bytes that no longer match the
// embedded hash.
func writeGo(path string, src []byte) error {
	return os.WriteFile(path, src, 0o644)
}

// contentHash returns the hex sha256 digest of body, embedded in each
// generated file's header so a hand edit — which would not update this line —
// is detectable without needing abi.yaml or a Go/BPF toolchain (see -verify).
func contentHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// prependGoHeader formats body, hashes the formatted result, and prepends a
// "Code generated" header carrying that hash.
func prependGoHeader(in string, body []byte) []byte {
	formatted, err := format.Source(body)
	if err != nil {
		panic(fmt.Sprintf("gofmt generated body for %s: %v\n%s", in, err, body))
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, "// Code generated by internal/abi/gen from %s; DO NOT EDIT.\n", in)
	fmt.Fprintf(&out, "// sha256: %s\n\n", contentHash(formatted))
	out.Write(formatted)
	return out.Bytes()
}

// prependCHeader hashes body and prepends a "Code generated" header carrying
// that hash, C-comment style.
func prependCHeader(in string, body []byte) []byte {
	var out bytes.Buffer
	fmt.Fprintf(&out, "/* Code generated by internal/abi/gen from %s; DO NOT EDIT. */\n", in)
	fmt.Fprintf(&out, "// sha256: %s\n\n", contentHash(body))
	out.Write(body)
	return out.Bytes()
}

// splitGeneratedHeader extracts the "// sha256: <hex>" line embedded by
// prependGoHeader/prependCHeader and the body hashed to produce it. It
// expects their fixed 3-line-then-body layout: a DO-NOT-EDIT line, the hash
// line, a blank line, then the body.
func splitGeneratedHeader(content []byte) (hash string, body []byte, ok bool) {
	lines := bytes.SplitN(content, []byte("\n"), 4)
	if len(lines) < 4 {
		return "", nil, false
	}
	const prefix = "// sha256: "
	line2 := string(lines[1])
	if !strings.HasPrefix(line2, prefix) {
		return "", nil, false
	}
	if strings.TrimSpace(string(lines[2])) != "" {
		return "", nil, false
	}
	return strings.TrimSpace(strings.TrimPrefix(line2, prefix)), lines[3], true
}

// verifyGenerated checks that each path's current content still matches its
// own embedded content hash, catching a hand edit that bypassed the generator
// even if abi.yaml was never touched (which a regenerate-and-diff check would
// otherwise miss only if someone also forgot to run make generate/ebpf).
func verifyGenerated(paths ...string) error {
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		want, body, ok := splitGeneratedHeader(content)
		if !ok {
			return fmt.Errorf("%s: missing or malformed embedded content hash", path)
		}
		if got := contentHash(body); got != want {
			return fmt.Errorf("%s: content hash %s does not match embedded %s; "+
				"file was hand-edited after generation — run `make generate`/`make ebpf`", path, got, want)
		}
	}
	fmt.Println("abi/gen -verify: all generated files match their embedded content hash")
	return nil
}
