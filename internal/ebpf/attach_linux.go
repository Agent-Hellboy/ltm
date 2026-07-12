//go:build linux

package ebpf

import (
	"fmt"
	"os"

	ciliumebpf "github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"

	"ltm/internal/abi"
)

func attachTracepoints(coll *ciliumebpf.Collection) ([]link.Link, int, int, error) {
	links := make([]link.Link, 0, len(abi.CollectorTracepoints))
	var attached, skipped int

	for _, tp := range abi.CollectorTracepoints {
		prog, ok := coll.Programs[tp.Program]
		if !ok {
			// A missing program means the embedded collector_bpfel.o is out of
			// sync with the tracepoint table — a build error, not an
			// environment one, so surface it loudly.
			if tp.Optional {
				skipped++
				continue
			}
			return nil, 0, 0, fmt.Errorf("missing bpf program %q; rebuild collector_bpfel.o with `make ebpf`", tp.Program)
		}

		l, err := link.Tracepoint(tp.Group, tp.Event, prog, nil)
		if err != nil {
			// A tracepoint may be absent or restricted on a given kernel or in
			// a sandboxed VM. Skip it and keep collecting the rest rather than
			// aborting the whole session.
			fmt.Fprintf(os.Stderr, "ltm: skip tracepoint %s/%s: %v\n", tp.Group, tp.Event, err)
			skipped++
			continue
		}

		links = append(links, l)
		attached++
	}

	return links, attached, skipped, nil
}
