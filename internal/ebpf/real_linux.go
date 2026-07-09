//go:build linux

package ebpf

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "embed"
	ciliumebpf "github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/perf"
	"github.com/cilium/ebpf/rlimit"

	"ltm/internal/storage"
)

//go:embed collector_bpfel.o
var collectorBPFEL []byte

type RealCollector struct {
	BufferPages int
}

func (RealCollector) Name() string { return "ebpf" }

type kernelEvent struct {
	TsNs       uint64
	Bytes      uint64
	Pid        uint32
	Uid        uint32
	LocalPort  uint16
	RemotePort uint16
	LocalIP4   uint32
	RemoteIP4  uint32
	Comm       [16]byte
	Category   [16]byte
	Action     [16]byte
	Path       [128]byte
	OldPath    [128]byte
	SyscallNR  uint32
	FD         int32
	Aux        uint32
}

func (r RealCollector) Run(ctx context.Context, out chan<- storage.Event) error {
	if err := rlimit.RemoveMemlock(); err != nil {
		return err
	}

	bootTime, err := wallClockBootTime()
	if err != nil {
		return err
	}

	spec, err := ciliumebpf.LoadCollectionSpecFromReader(bytes.NewReader(collectorBPFEL))
	if err != nil {
		return fmt.Errorf("load embedded ebpf object: %w", err)
	}

	coll, err := ciliumebpf.NewCollection(spec)
	if err != nil {
		return fmt.Errorf("create ebpf collection: %w", err)
	}
	defer coll.Close()

	if err := setSelfPID(coll); err != nil {
		return err
	}

	links := make([]link.Link, 0, len(collectorTracepoints))
	defer func() {
		for _, l := range links {
			_ = l.Close()
		}
	}()

	var attached, skipped int
	for _, tp := range collectorTracepoints {
		prog, ok := coll.Programs[tp.Program]
		if !ok {
			// A missing program means the embedded collector_bpfel.o is out of
			// sync with the tracepoint table — a build error, not an
			// environment one, so surface it loudly.
			if tp.Optional {
				skipped++
				continue
			}
			return fmt.Errorf("missing bpf program %q; rebuild collector_bpfel.o with `make ebpf`", tp.Program)
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
	if attached == 0 {
		return errors.New("no tracepoints could be attached; ebpf mode needs root with CAP_BPF and CAP_PERFMON")
	}
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "ltm: ebpf collector attached %d tracepoints, skipped %d\n", attached, skipped)
	}

	perfBuf := r.BufferPages
	if perfBuf <= 0 {
		perfBuf = 64
	}
	reader, err := perf.NewReader(coll.Maps["events"], perfBuf*4096)
	if err != nil {
		return fmt.Errorf("create perf reader: %w", err)
	}
	defer reader.Close()

	var dropped uint64
	var sendMu sync.Mutex
	send := func(ev storage.Event) {
		sendMu.Lock()
		defer sendMu.Unlock()
		select {
		case out <- ev:
		default:
		}
	}

	go func() {
		<-ctx.Done()
		_ = reader.Close()
	}()

	for {
		record, err := reader.Read()
		if err != nil {
			if errors.Is(err, perf.ErrClosed) {
				return nil
			}
			return err
		}
		if record.LostSamples != 0 {
			dropped += record.LostSamples
			continue
		}
		var ke kernelEvent
		if err := binary.Read(bytes.NewReader(record.RawSample), binary.LittleEndian, &ke); err != nil {
			continue
		}
		ev := convertKernelEvent(bootTime, ke, dropped)
		dropped = 0
		send(ev)
	}
}

func setSelfPID(coll *ciliumebpf.Collection) error {
	pidMap, ok := coll.Maps["self_pid"]
	if !ok {
		return nil
	}
	key := uint32(0)
	pid := uint32(os.Getpid())
	return pidMap.Update(&key, &pid, ciliumebpf.UpdateAny)
}

func convertKernelEvent(bootTime time.Time, ke kernelEvent, dropped uint64) storage.Event {
	ev := storage.Event{
		SchemaVersion: storage.SchemaVersion,
		Timestamp:     bootTime.Add(time.Duration(ke.TsNs)),
		Category:      cstring(ke.Category[:]),
		Action:        cstring(ke.Action[:]),
		PID:           int(ke.Pid),
		UID:           int(ke.Uid),
		Comm:          cstring(ke.Comm[:]),
		Path:          cstring(ke.Path[:]),
		OldPath:       cstring(ke.OldPath[:]),
		LocalPort:     int(ke.LocalPort),
		RemotePort:    int(ke.RemotePort),
		Metadata: map[string]any{
			"syscall_nr": ke.SyscallNR,
			"fd":         ke.FD,
			"aux":        ke.Aux,
		},
		DroppedBefore: int64(dropped),
	}
	if ke.LocalIP4 != 0 {
		ev.LocalAddr = ipv4String(ke.LocalIP4)
	}
	if ke.RemoteIP4 != 0 {
		ev.RemoteAddr = ipv4String(ke.RemoteIP4)
	}
	if ev.Category == "process" && ev.Action == "exec" && ev.Path != "" {
		ev.Exe = ev.Path
	}
	if ev.Category == "block" {
		ev.Metadata["dev"] = ke.Aux
		ev.Metadata["nr_sector"] = ke.SyscallNR
		ev.Metadata["rwbs"] = cstring(ke.Path[:])
	}
	if ev.Category == "process" && ev.Action == "fork" && ke.Aux != 0 {
		ev.PPID = int(ke.Aux)
	}
	if ev.Category == "process" && ev.Action == "kill" && ke.FD > 0 {
		ev.TargetPID = int(ke.FD)
	}
	if ev.PID > 0 && ev.PPID == 0 && ev.Action != "fork" {
		if ppid, ok := readPPID(ev.PID); ok {
			ev.PPID = ppid
		}
	}
	if ev.Action == "listen" && ev.LocalAddr == "" && ke.FD >= 0 {
		ev.Metadata["listen_fd"] = ke.FD
	}
	return ev
}

func wallClockBootTime() (time.Time, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "btime ") {
			sec, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
			if err != nil {
				return time.Time{}, err
			}
			return time.Unix(sec, 0), nil
		}
	}
	return time.Time{}, errors.New("btime not found in /proc/stat")
}

func cstring(b []byte) string {
	n := bytes.IndexByte(b, 0)
	if n < 0 {
		n = len(b)
	}
	return string(b[:n])
}

func ipv4String(v uint32) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return net.IPv4(b[0], b[1], b[2], b[3]).String()
}

func readPPID(pid int) (int, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) < 4 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[3])
	if err != nil {
		return 0, false
	}
	return ppid, true
}
