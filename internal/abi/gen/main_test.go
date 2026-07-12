package main

import (
	"strings"
	"testing"
)

// validDoc returns a minimal abiDoc that passes validate(), for tests to
// mutate into an invalid one.
func validDoc() abiDoc {
	return abiDoc{
		Tracepoints: []tracepoint{
			{Group: "syscalls", Event: "sys_enter_open", Program: "trace_open"},
		},
		Schema: schema{
			Version: 1,
			Table:   "events",
			Columns: []column{
				{Name: "id", Type: "INTEGER"},
				{Name: "path", Type: "TEXT"},
			},
			Indexes: []index{
				{Name: "idx_path", Columns: []string{"path"}},
			},
		},
		KernelEvent: kernelEvent{
			Struct:    "ltm_kernel_event",
			Constants: []kconst{{Name: "PATH_MAX", Value: 128}},
			Fields: []kfield{
				{Name: "path", Type: "char", Count: "PATH_MAX"},
				{Name: "pid", Type: "u32"},
			},
		},
	}
}

func TestValidateAcceptsMinimalValidDoc(t *testing.T) {
	doc := validDoc()
	if err := validate(&doc); err != nil {
		t.Fatalf("validate() on a well-formed doc = %v, want nil", err)
	}
}

func TestValidateRejectsDuplicateTracepointGroupEvent(t *testing.T) {
	doc := validDoc()
	dup := doc.Tracepoints[0]
	dup.Program = "trace_open_dup" // distinct program, same group+event
	doc.Tracepoints = append(doc.Tracepoints, dup)

	err := validate(&doc)
	if err == nil || !strings.Contains(err.Error(), "duplicate tracepoint") {
		t.Fatalf("validate() = %v, want a duplicate tracepoint error", err)
	}
}

func TestValidateRejectsDuplicateIndexName(t *testing.T) {
	doc := validDoc()
	doc.Schema.Indexes = append(doc.Schema.Indexes, doc.Schema.Indexes[0])

	err := validate(&doc)
	if err == nil || !strings.Contains(err.Error(), "duplicate index") {
		t.Fatalf("validate() = %v, want a duplicate index error", err)
	}
}

func TestValidateRejectsDuplicateKernelConstant(t *testing.T) {
	doc := validDoc()
	dup := doc.KernelEvent.Constants[0]
	dup.Value++ // even a different value must still be rejected as a duplicate name
	doc.KernelEvent.Constants = append(doc.KernelEvent.Constants, dup)

	err := validate(&doc)
	if err == nil || !strings.Contains(err.Error(), "duplicate kernel_event constant") {
		t.Fatalf("validate() = %v, want a duplicate constant error", err)
	}
}

func TestValidateRejectsNonPositiveLiteralCount(t *testing.T) {
	doc := validDoc()
	doc.KernelEvent.Fields = append(doc.KernelEvent.Fields, kfield{Name: "bogus", Type: "char", Count: "-1"})

	err := validate(&doc)
	if err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("validate() = %v, want a non-positive count error", err)
	}
}

func TestValidateRejectsNonPositiveConstantCount(t *testing.T) {
	doc := validDoc()
	doc.KernelEvent.Constants = append(doc.KernelEvent.Constants, kconst{Name: "ZERO_LEN", Value: 0})
	doc.KernelEvent.Fields = append(doc.KernelEvent.Fields, kfield{Name: "bogus", Type: "char", Count: "ZERO_LEN"})

	err := validate(&doc)
	if err == nil || !strings.Contains(err.Error(), "non-positive constant value") {
		t.Fatalf("validate() = %v, want a non-positive constant count error", err)
	}
}

// TestGeneratedFilesMatchEmbeddedHash catches a hand edit to a generated file
// that bypassed the generator, even if abi.yaml was never touched — a case a
// regenerate-and-diff check only catches if someone remembers to run it. It
// needs neither abi.yaml nor a Go/BPF toolchain, so it runs in every `go test
// ./...`, not just the CI lane that reruns the full generator.
//
// Paths are relative to this test's own package directory (internal/abi/gen),
// one level below where the generator itself resolves its default flags (it
// runs via `go:generate` with cwd set to internal/abi).
func TestGeneratedFilesMatchEmbeddedHash(t *testing.T) {
	if err := verifyGenerated("../tracepoints_gen.go", "../schema_gen.go", "../../storage/schema_gen.go", "../kernel_event.gen.h"); err != nil {
		t.Fatal(err)
	}
}
