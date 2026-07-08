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

	"ltm/storage"
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

	links := make([]link.Link, 0, 10)
	attach := func(l link.Link, err error) error {
		if err != nil {
			return err
		}
		links = append(links, l)
		return nil
	}
	defer func() {
		for _, l := range links {
			_ = l.Close()
		}
	}()

	if err := attach(link.Tracepoint("syscalls", "sys_enter_execve", coll.Programs["trace_sys_enter_execve"], nil)); err != nil {
		return err
	}
	if err := attach(link.Tracepoint("syscalls", "sys_enter_execveat", coll.Programs["trace_sys_enter_execveat"], nil)); err != nil {
		return err
	}
	if err := attach(link.Tracepoint("syscalls", "sys_enter_openat", coll.Programs["trace_sys_enter_openat"], nil)); err != nil {
		return err
	}
	if err := attach(link.Tracepoint("syscalls", "sys_enter_openat2", coll.Programs["trace_sys_enter_openat2"], nil)); err != nil {
		return err
	}
	if err := attach(link.Tracepoint("syscalls", "sys_enter_write", coll.Programs["trace_sys_enter_write"], nil)); err != nil {
		return err
	}
	if err := attach(link.Tracepoint("syscalls", "sys_enter_unlinkat", coll.Programs["trace_sys_enter_unlinkat"], nil)); err != nil {
		return err
	}
	if err := attach(link.Tracepoint("syscalls", "sys_enter_renameat2", coll.Programs["trace_sys_enter_renameat2"], nil)); err != nil {
		return err
	}
	if err := attach(link.Tracepoint("syscalls", "sys_enter_connect", coll.Programs["trace_sys_enter_connect"], nil)); err != nil {
		return err
	}
	if err := attach(link.Tracepoint("syscalls", "sys_enter_bind", coll.Programs["trace_sys_enter_bind"], nil)); err != nil {
		return err
	}
	if err := attach(link.Tracepoint("sched", "sched_process_exit", coll.Programs["trace_sched_process_exit"], nil)); err != nil {
		return err
	}

	perfBuf := r.BufferPages
	if perfBuf <= 0 {
		perfBuf = 8
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
		Metadata:      map[string]any{},
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
	if ev.PID > 0 && ev.PPID == 0 {
		if ppid, ok := readPPID(ev.PID); ok {
			ev.PPID = ppid
		}
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
