//go:build linux

package sample

import (
	"os"
	"strconv"
	"time"

	"ltm/internal/storage"
)

// New returns a Linux /proc-backed Sampler.
func New() Sampler { return &linuxSampler{} }

// Supported reports whether sampling works on this platform.
func Supported() bool { return true }

// linuxSampler holds the previous cumulative counters so each call can report
// deltas. Not safe for concurrent use (see Sampler docs).
type linuxSampler struct {
	haveSys bool
	prevCPU cpuStat
	prevDsk diskCounters
	prevNet netCounters

	prevProc     map[int]uint64 // pid -> prev utime+stime jiffies
	prevProcTime time.Time      // wall clock of the previous Processes call
}

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func (s *linuxSampler) System() (storage.SystemSample, error) {
	cpu := parseCPUStat(readFile("/proc/stat"))
	dsk := parseDiskstats(readFile("/proc/diskstats"), wholeBlockDevices())
	net := parseNetDev(readFile("/proc/net/dev"))
	mem := parseMeminfo(readFile("/proc/meminfo"))
	l1, l5, l15 := parseLoadavg(readFile("/proc/loadavg"))
	cpuSome, _ := parsePressure(readFile("/proc/pressure/cpu"))
	memSome, memFull := parsePressure(readFile("/proc/pressure/memory"))
	ioSome, ioFull := parsePressure(readFile("/proc/pressure/io"))

	out := storage.SystemSample{
		Load1: l1, Load5: l5, Load15: l15,
		ProcsRunning: cpu.running, ProcsBlocked: cpu.blocked,
		MemTotalKB: mem.total, MemAvailableKB: mem.available,
		SwapTotalKB: mem.swapTotal, SwapFreeKB: mem.swapFree,
		PSICPUSomeAvg10: cpuSome,
		PSIMemSomeAvg10: memSome, PSIMemFullAvg10: memFull,
		PSIIOSomeAvg10: ioSome, PSIIOFullAvg10: ioFull,
	}

	// Rates need a previous reading; the first sample reports zero rates.
	if s.haveSys {
		out.CPUPct = cpuPctOverInterval(cpu.busy-s.prevCPU.busy, cpu.total-s.prevCPU.total)
		out.DiskReadKB = sectorsToKB(dsk.sectorsRead - s.prevDsk.sectorsRead)
		out.DiskWriteKB = sectorsToKB(dsk.sectorsWritten - s.prevDsk.sectorsWritten)
		out.NetRxKB = int64((net.rxBytes - s.prevNet.rxBytes) / 1024)
		out.NetTxKB = int64((net.txBytes - s.prevNet.txBytes) / 1024)
		out.NetRxErrs = int64(net.rxErrs - s.prevNet.rxErrs)
		out.NetTxErrs = int64(net.txErrs - s.prevNet.txErrs)
		out.NetRxDrop = int64(net.rxDrop - s.prevNet.rxDrop)
		out.NetTxDrop = int64(net.txDrop - s.prevNet.txDrop)
	}
	s.prevCPU, s.prevDsk, s.prevNet, s.haveSys = cpu, dsk, net, true
	return out, nil
}

func sectorsToKB(sectors uint64) int64 { return int64(sectors) / 2 } // 512B sectors -> KB

func (s *linuxSampler) Processes() ([]storage.ProcessSample, error) {
	now := time.Now()
	// Elapsed wall time since the last call turns a jiffy delta into a percent.
	// Measuring it here (rather than trusting the ticker) keeps CPU% correct
	// even if a tick was late.
	var elapsed float64
	if !s.prevProcTime.IsZero() {
		elapsed = now.Sub(s.prevProcTime).Seconds()
	}

	pids := listPIDs()
	cur := make(map[int]uint64, len(pids))
	out := make([]storage.ProcessSample, 0, len(pids))

	for _, pid := range pids {
		base := "/proc/" + strconv.Itoa(pid)
		st := parseProcStat(readFile(base + "/stat"))
		if !st.ok {
			continue // process exited between listing and read
		}
		cur[pid] = st.jiffies
		rchar, wchar := parseProcIO(readFile(base + "/io"))
		ps := storage.ProcessSample{
			PID: pid, Comm: st.comm, State: st.state,
			RSSKB: st.rssKB, Threads: st.threads,
			ReadBytes: rchar, WriteBytes: wchar,
			Cgroup: parseCgroup(readFile(base + "/cgroup")),
		}
		if elapsed > 0 {
			if prev, ok := s.prevProc[pid]; ok && st.jiffies >= prev {
				ps.CPUPct = procCPUPct(st.jiffies-prev, elapsed)
			}
		}
		out = append(out, ps)
	}
	s.prevProc, s.prevProcTime = cur, now
	return out, nil
}

// listPIDs returns the numeric entries under /proc (live process IDs).
func listPIDs() []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	pids := make([]int, 0, len(entries))
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// wholeBlockDevices lists whole block devices from /sys/block so diskstats
// sums exclude partitions (which would double-count). Empty on failure, which
// parseDiskstats treats as "include everything".
func wholeBlockDevices() map[string]bool {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil
	}
	out := make(map[string]bool, len(entries))
	for _, e := range entries {
		out[e.Name()] = true
	}
	return out
}
