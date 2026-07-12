package ebpf

import (
	"sort"
	"testing"

	"ltm/internal/abi"
)

// TestCollectorProgramsMatchTable is the ground-truth check: it loads the
// embedded, compiled collector object and asserts its program set is exactly
// the set named in abi.CollectorTracepoints. Because the object is what the
// kernel actually loads, this catches a stale collector_bpfel.o, a SEC() rename
// in collector.bpf.c, or an abi.yaml entry with no matching program —
// any drift between the three, in one test.
func TestCollectorProgramsMatchTable(t *testing.T) {
	spec, err := loadCollector()
	if err != nil {
		t.Fatalf("load collector spec: %v", err)
	}

	inObject := make([]string, 0, len(spec.Programs))
	for name := range spec.Programs {
		inObject = append(inObject, name)
	}

	inTable := make([]string, 0, len(abi.CollectorTracepoints))
	for _, tp := range abi.CollectorTracepoints {
		inTable = append(inTable, tp.Program)
	}

	sort.Strings(inObject)
	sort.Strings(inTable)

	if len(inObject) != len(inTable) {
		t.Errorf("program count mismatch: object has %d, table has %d", len(inObject), len(inTable))
	}

	objSet := make(map[string]bool, len(inObject))
	for _, n := range inObject {
		objSet[n] = true
	}
	tblSet := make(map[string]bool, len(inTable))
	for _, n := range inTable {
		tblSet[n] = true
	}

	for _, n := range inTable {
		if !objSet[n] {
			t.Errorf("table names program %q that is absent from collector_bpfel.o (rebuild with `make ebpf`?)", n)
		}
	}
	for _, n := range inObject {
		if !tblSet[n] {
			t.Errorf("object has program %q not named in abi.yaml/generated tracepoint table", n)
		}
	}
}
