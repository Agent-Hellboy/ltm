//go:build linux

package ebpf

import (
	"bytes"
	"encoding/binary"
	"net"
	"time"

	"ltm/internal/abi"
	"ltm/internal/storage"
)

func decodeKernelEvent(raw []byte) (collectorEvent, error) {
	var ke collectorEvent
	err := binary.Read(bytes.NewReader(raw), binary.LittleEndian, &ke)
	return ke, err
}

func convertKernelEvent(bootTime time.Time, ke collectorEvent, dropped uint64) storage.Event {
	ev := storage.Event{
		SchemaVersion: abi.SchemaVersion,
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
			"syscall_nr": ke.SyscallNr,
			"fd":         ke.Fd,
			"aux":        ke.Aux,
		},
		DroppedBefore: int64(dropped),
	}
	if ke.LocalIp4 != 0 {
		ev.LocalAddr = ipv4String(ke.LocalIp4)
	}
	if ke.RemoteIp4 != 0 {
		ev.RemoteAddr = ipv4String(ke.RemoteIp4)
	}
	if ev.Category == "process" && ev.Action == "exec" && ev.Path != "" {
		ev.Exe = ev.Path
	}
	if ev.Category == "block" {
		// Block events reuse the generic fields to carry disk-request data:
		// ke.Path is the rwbs flag string, ke.Aux the device, ke.SyscallNr the
		// sector count. Replace the misleading generic metadata accordingly.
		rwbs := cstring(ke.Path[:])
		ev.Path = ""
		ev.Metadata = map[string]any{
			"dev":       ke.Aux,
			"nr_sector": ke.SyscallNr,
			"rwbs":      rwbs,
		}
		switch ev.Action {
		case "slow_io":
			// Phase 3: ke.Bytes carries the issue→complete service latency (ns).
			ev.Metadata["latency_ns"] = ke.Bytes
		case "error":
			ev.Metadata["errno"] = ke.Fd
			ev.Metadata["sector"] = ke.Bytes
		}
	}
	if ev.Category == "memory" && ev.Action == "oom_kill" {
		// Phase 2: ke.Bytes carries the victim's resident set size in bytes; pid
		// and uid are the victim's (set in the BPF program, not the current task).
		ev.Metadata = map[string]any{"rss_bytes": ke.Bytes}
	}
	if ev.Category == "process" && ev.Action == "fork" && ke.Aux != 0 {
		ev.PPID = int(ke.Aux)
	}
	if ev.Category == "process" && ev.Action == "kill" && ke.Fd > 0 {
		ev.TargetPID = int(ke.Fd)
	}
	if ev.PID > 0 && ev.PPID == 0 && ev.Action != "fork" {
		if ppid, ok := readPPID(ev.PID); ok {
			ev.PPID = ppid
		}
	}
	if ev.Action == "listen" && ev.LocalAddr == "" && ke.Fd >= 0 {
		ev.Metadata["listen_fd"] = ke.Fd
	}
	return ev
}

// cstring decodes a NUL-terminated C char array. bpf2go emits char[] fields as
// []int8, so this takes []int8 rather than []byte.
func cstring(b []int8) string {
	n := 0
	for n < len(b) && b[n] != 0 {
		n++
	}
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		out[i] = byte(b[i])
	}
	return string(out)
}

// ipv4String renders an IPv4 address held in a uint32 that was read from the
// kernel's network-byte-order sin_addr via a little-endian struct decode. The
// bytes are therefore already in [a,b,c,d] order little-endian, so encode them
// back out little-endian to recover the original dotted quad.
func ipv4String(v uint32) string {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	return net.IPv4(b[0], b[1], b[2], b[3]).String()
}
