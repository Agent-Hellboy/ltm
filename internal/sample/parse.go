// Package sample collects low-frequency machine-state samples (CPU, memory,
// PSI, disk, network, per-process) from /proc, distinct from the per-event
// activity captured by the eBPF collector. Sampling is Linux-only; other
// platforms get a stub. This file holds the pure parsers (no I/O) so the /proc
// text formats can be unit-tested on any OS.
package sample

import (
	"strconv"
	"strings"
)

// clkTck is the kernel USER_HZ. It is 100 on essentially all Linux/x86_64
// (ltm's only recording target), so CPU-jiffy math uses it as a constant
// rather than a cgo sysconf(_SC_CLK_TCK) call.
const clkTck = 100

// pageKB is the page size in kB (4096-byte pages on x86_64).
const pageKB = 4

// cpuStat holds the aggregate CPU counters and process gauges from /proc/stat.
type cpuStat struct {
	busy, total      uint64
	running, blocked int
}

// parseCPUStat reads the aggregate "cpu" line plus procs_running/procs_blocked
// from /proc/stat. busy excludes idle+iowait; total is the sum of all columns.
func parseCPUStat(s string) cpuStat {
	var c cpuStat
	for line := range strings.SplitSeq(s, "\n") {
		switch {
		case strings.HasPrefix(line, "cpu "):
			fields := strings.Fields(line)[1:]
			for i, f := range fields {
				v, _ := strconv.ParseUint(f, 10, 64)
				c.total += v
				// columns: user nice system idle iowait irq softirq steal ...
				if i != 3 && i != 4 { // not idle, not iowait
					c.busy += v
				}
			}
		case strings.HasPrefix(line, "procs_running "):
			c.running = atoiField(line)
		case strings.HasPrefix(line, "procs_blocked "):
			c.blocked = atoiField(line)
		}
	}
	return c
}

func atoiField(line string) int {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(fields[1])
	return n
}

// parseLoadavg parses /proc/loadavg: "l1 l5 l15 running/total lastpid".
func parseLoadavg(s string) (l1, l5, l15 float64) {
	fields := strings.Fields(s)
	if len(fields) < 3 {
		return 0, 0, 0
	}
	l1, _ = strconv.ParseFloat(fields[0], 64)
	l5, _ = strconv.ParseFloat(fields[1], 64)
	l15, _ = strconv.ParseFloat(fields[2], 64)
	return l1, l5, l15
}

type meminfo struct {
	total, available, swapTotal, swapFree int64
}

// parseMeminfo pulls the four gauges ltm samples from /proc/meminfo (kB).
func parseMeminfo(s string) meminfo {
	var m meminfo
	for line := range strings.SplitSeq(s, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v, _ := strconv.ParseInt(strings.Fields(strings.TrimSpace(val))[0], 10, 64)
		switch key {
		case "MemTotal":
			m.total = v
		case "MemAvailable":
			m.available = v
		case "SwapTotal":
			m.swapTotal = v
		case "SwapFree":
			m.swapFree = v
		}
	}
	return m
}

// parsePressure reads a /proc/pressure/* file, returning the "some" and "full"
// avg10 values (percent). The cpu file has no "full" line, so full is 0 there.
func parsePressure(s string) (some, full float64) {
	for line := range strings.SplitSeq(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		avg10 := pressureAvg10(fields)
		switch fields[0] {
		case "some":
			some = avg10
		case "full":
			full = avg10
		}
	}
	return some, full
}

func pressureAvg10(fields []string) float64 {
	for _, f := range fields {
		if v, ok := strings.CutPrefix(f, "avg10="); ok {
			n, _ := strconv.ParseFloat(v, 64)
			return n
		}
	}
	return 0
}

type diskCounters struct {
	sectorsRead, sectorsWritten uint64
}

// parseDiskstats sums sectors read/written across whole block devices only.
// whole names the devices to include (from /sys/block) so partitions are not
// double-counted. Sectors are 512 bytes.
func parseDiskstats(s string, whole map[string]bool) diskCounters {
	var d diskCounters
	for line := range strings.SplitSeq(s, "\n") {
		fields := strings.Fields(line)
		// major minor name reads mergedR sectorsR msR writes mergedW sectorsW ...
		if len(fields) < 10 {
			continue
		}
		if len(whole) > 0 && !whole[fields[2]] {
			continue
		}
		sr, _ := strconv.ParseUint(fields[5], 10, 64)
		sw, _ := strconv.ParseUint(fields[9], 10, 64)
		d.sectorsRead += sr
		d.sectorsWritten += sw
	}
	return d
}

type netCounters struct {
	rxBytes, txBytes uint64
	rxErrs, txErrs   uint64
	rxDrop, txDrop   uint64
}

// parseNetDev sums per-interface counters from /proc/net/dev, excluding
// loopback. Field layout after "iface:" is 16 columns; rx is [0..7], tx [8..15].
func parseNetDev(s string) netCounters {
	var n netCounters
	for line := range strings.SplitSeq(s, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "lo" || name == "" {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 16 {
			continue
		}
		add := func(dst *uint64, idx int) { v, _ := strconv.ParseUint(f[idx], 10, 64); *dst += v }
		add(&n.rxBytes, 0)
		add(&n.rxErrs, 2)
		add(&n.rxDrop, 3)
		add(&n.txBytes, 8)
		add(&n.txErrs, 10)
		add(&n.txDrop, 11)
	}
	return n
}

type procStat struct {
	comm    string
	state   string
	jiffies uint64 // utime+stime
	threads int
	rssKB   int64
	ok      bool
}

// parseProcStat parses /proc/<pid>/stat. comm may contain spaces and
// parentheses, so fixed fields are read after the LAST ')'. Field indices
// (1-based in the manpage) after comm: 3 state, 14 utime, 15 stime, 20
// num_threads, 24 rss(pages).
func parseProcStat(s string) procStat {
	var p procStat
	lp := strings.IndexByte(s, '(')
	rp := strings.LastIndexByte(s, ')')
	if lp < 0 || rp < 0 || rp < lp {
		return p
	}
	p.comm = s[lp+1 : rp]
	fields := strings.Fields(s[rp+1:])
	// fields[0]=state(3) ... utime=field14 -> index 11, stime=15 -> 12,
	// num_threads=20 -> 17, rss=24 -> 21.
	if len(fields) < 22 {
		return p
	}
	p.state = fields[0]
	ut, _ := strconv.ParseUint(fields[11], 10, 64)
	st, _ := strconv.ParseUint(fields[12], 10, 64)
	p.jiffies = ut + st
	p.threads, _ = strconv.Atoi(fields[17])
	pages, _ := strconv.ParseInt(fields[21], 10, 64)
	p.rssKB = pages * pageKB
	p.ok = true
	return p
}

// parseProcIO pulls rchar/wchar (cumulative bytes) from /proc/<pid>/io.
func parseProcIO(s string) (rchar, wchar int64) {
	for line := range strings.SplitSeq(s, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v, _ := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		switch key {
		case "rchar":
			rchar = v
		case "wchar":
			wchar = v
		}
	}
	return rchar, wchar
}

// parseCgroup returns the cgroup path from /proc/<pid>/cgroup. For cgroup v2
// the line is "0::<path>"; for v1 it is "<id>:<controllers>:<path>". Either
// way the path is the segment after the last ':'.
func parseCgroup(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	if i := strings.LastIndexByte(line, ':'); i >= 0 {
		return line[i+1:]
	}
	return ""
}

// cpuPctOverInterval converts a busy/total jiffy delta into a percent.
func cpuPctOverInterval(busyDelta, totalDelta uint64) float64 {
	if totalDelta == 0 {
		return 0
	}
	return 100 * float64(busyDelta) / float64(totalDelta)
}

// procCPUPct converts a process jiffy delta into percent over intervalSec
// (can exceed 100 for multi-threaded processes — percent per core).
func procCPUPct(jiffyDelta uint64, intervalSec float64) float64 {
	if intervalSec <= 0 {
		return 0
	}
	return 100 * float64(jiffyDelta) / (clkTck * intervalSec)
}
