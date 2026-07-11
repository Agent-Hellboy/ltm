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
	flag.Parse()

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
	seen := make(map[string]bool, len(doc.Tracepoints))
	for i, tp := range doc.Tracepoints {
		if tp.Group == "" || tp.Event == "" || tp.Program == "" {
			return fmt.Errorf("tracepoint %d has an empty group/event/program", i)
		}
		if seen[tp.Program] {
			return fmt.Errorf("duplicate program %q", tp.Program)
		}
		seen[tp.Program] = true
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
	for _, idx := range s.Indexes {
		if idx.Name == "" || len(idx.Columns) == 0 {
			return fmt.Errorf("index %q has no name or columns", idx.Name)
		}
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
	consts := make(map[string]bool, len(ke.Constants))
	for _, c := range ke.Constants {
		if c.Name == "" {
			return fmt.Errorf("kernel_event constant with empty name")
		}
		consts[c.Name] = true
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
		// A count must resolve: either a declared constant or an integer literal.
		if f.Count != "" && !consts[f.Count] {
			if _, err := strconv.Atoi(f.Count); err != nil {
				return fmt.Errorf("field %q count %q is neither a declared constant nor an integer", f.Name, f.Count)
			}
		}
	}
	return nil
}

func tracepointsFile(in string, tps []tracepoint) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "// Code generated by internal/abi/gen from %s; DO NOT EDIT.\n\n", in)
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
	return buf.Bytes()
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
	fmt.Fprintf(&buf, "// Code generated by internal/abi/gen from %s; DO NOT EDIT.\n\n", in)
	buf.WriteString("package abi\n\n")
	fmt.Fprintf(&buf, "const SchemaVersion = %d\n\n", s.Version)
	buf.WriteString("// SchemaDoc describes the queryable data model. It is shown by `ltm query sql`\n")
	buf.WriteString("// with no arguments and embedded in the prompt sent to a configured agent.\n")
	fmt.Fprintf(&buf, "const SchemaDoc = %s\n", backquote(sb.String()))
	return buf.Bytes()
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
	fmt.Fprintf(&buf, "// Code generated by internal/abi/gen from %s; DO NOT EDIT.\n\n", in)
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
	return buf.Bytes()
}

// kernelEventHeader renders the C header describing the kernel->userspace wire
// layout. collector.bpf.c includes it; bpf2go then derives the Go struct from
// the compiled object's BTF, so this header is the single source for the layout.
func kernelEventHeader(in string, ke *kernelEvent) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "/* Code generated by internal/abi/gen from %s; DO NOT EDIT. */\n\n", in)
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
	return buf.Bytes()
}

// backquote renders s as a Go raw string literal. The schema/tracepoint text
// never contains a backtick, so raw literals are always safe here.
func backquote(s string) string {
	if strings.Contains(s, "`") {
		panic("generated text unexpectedly contains a backtick")
	}
	return "`" + s + "`"
}

func writeGo(path string, src []byte) error {
	formatted, err := format.Source(src)
	if err != nil {
		return fmt.Errorf("gofmt %s: %w\n%s", path, err, src)
	}
	return os.WriteFile(path, formatted, 0o644)
}
