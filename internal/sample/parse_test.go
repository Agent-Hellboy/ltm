package sample

import "testing"

func TestParseCPUStat(t *testing.T) {
	in := `cpu  100 0 50 800 40 0 10 0 0 0
cpu0 50 0 25 400 20 0 5 0 0 0
procs_running 3
procs_blocked 2
`
	c := parseCPUStat(in)
	// total = 100+0+50+800+40+0+10 = 1000; busy = total - idle(800) - iowait(40) = 160.
	if c.total != 1000 || c.busy != 160 {
		t.Fatalf("busy=%d total=%d, want 160/1000", c.busy, c.total)
	}
	if c.running != 3 || c.blocked != 2 {
		t.Fatalf("running=%d blocked=%d, want 3/2", c.running, c.blocked)
	}
	if got := cpuPctOverInterval(c.busy, c.total); got != 16 {
		t.Fatalf("cpu pct = %v, want 16", got)
	}
}

func TestParseMeminfo(t *testing.T) {
	in := "MemTotal:       16318628 kB\nMemFree: 100 kB\nMemAvailable:    8000000 kB\nSwapTotal:      2000000 kB\nSwapFree:       1999000 kB\n"
	m := parseMeminfo(in)
	if m.total != 16318628 || m.available != 8000000 || m.swapTotal != 2000000 || m.swapFree != 1999000 {
		t.Fatalf("meminfo = %+v", m)
	}
}

func TestParsePressure(t *testing.T) {
	// memory has some+full; cpu has only some.
	some, full := parsePressure("some avg10=1.50 avg60=0.80 avg300=0.20 total=123\nfull avg10=0.30 avg60=0.10 avg300=0.00 total=45\n")
	if some != 1.50 || full != 0.30 {
		t.Fatalf("mem pressure some=%v full=%v, want 1.50/0.30", some, full)
	}
	cpuSome, cpuFull := parsePressure("some avg10=2.00 avg60=1.00 avg300=0.50 total=999\n")
	if cpuSome != 2.00 || cpuFull != 0 {
		t.Fatalf("cpu pressure some=%v full=%v, want 2.00/0", cpuSome, cpuFull)
	}
}

func TestParseProcStat(t *testing.T) {
	// comm contains spaces and a ')': the parser must key off the LAST ')'.
	// Layout: pid (comm) state ppid pgrp ... utime(14) stime(15) ... nthreads(20) ... rss(24)
	// Build fields 3..24 after comm; utime=200 stime=100 -> jiffies 300; nthreads=7; rss=25 pages.
	in := "1234 (weird )name) R 1 1 1 0 -1 0 0 0 0 0 200 100 0 0 20 0 7 0 0 0 25 0 0"
	p := parseProcStat(in)
	if !p.ok {
		t.Fatal("expected ok")
	}
	if p.comm != "weird )name" {
		t.Fatalf("comm=%q, want %q", p.comm, "weird )name")
	}
	if p.state != "R" {
		t.Fatalf("state=%q, want R", p.state)
	}
	if p.jiffies != 300 {
		t.Fatalf("jiffies=%d, want 300", p.jiffies)
	}
	if p.threads != 7 {
		t.Fatalf("threads=%d, want 7", p.threads)
	}
	if p.rssKB != 25*pageKB {
		t.Fatalf("rssKB=%d, want %d", p.rssKB, 25*pageKB)
	}
}

func TestParseNetDevExcludesLoopback(t *testing.T) {
	in := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 1000 10 0 0 0 0 0 0 1000 10 0 0 0 0 0 0
  eth0: 5000 50 1 2 0 0 0 0 7000 70 3 4 0 0 0 0
`
	n := parseNetDev(in)
	if n.rxBytes != 5000 || n.txBytes != 7000 {
		t.Fatalf("rx=%d tx=%d, want 5000/7000 (lo excluded)", n.rxBytes, n.txBytes)
	}
	if n.rxErrs != 1 || n.rxDrop != 2 || n.txErrs != 3 || n.txDrop != 4 {
		t.Fatalf("errs/drops = %+v", n)
	}
}

func TestParseDiskstatsWholeDevicesOnly(t *testing.T) {
	in := `   8       0 sda 100 0 200 0 50 0 400 0 0 0 0
   8       1 sda1 90 0 180 0 40 0 300 0 0 0 0
 259       0 nvme0n1 10 0 40 0 5 0 80 0 0 0 0
`
	// Only whole devices sda + nvme0n1; sda1 (partition) excluded.
	d := parseDiskstats(in, map[string]bool{"sda": true, "nvme0n1": true})
	if d.sectorsRead != 240 || d.sectorsWritten != 480 {
		t.Fatalf("read=%d write=%d, want 240/480", d.sectorsRead, d.sectorsWritten)
	}
}

func TestParseCgroup(t *testing.T) {
	if got := parseCgroup("0::/system.slice/postgres.service\n"); got != "/system.slice/postgres.service" {
		t.Fatalf("v2 cgroup = %q", got)
	}
	if got := parseCgroup("12:memory:/user.slice\n11:cpu:/other\n"); got != "/user.slice" {
		t.Fatalf("v1 cgroup = %q", got)
	}
}

func TestProcCPUPct(t *testing.T) {
	// 500 jiffies over 5s at 100 Hz = 500 / (100*5) = 1.0 = 100%.
	if got := procCPUPct(500, 5); got != 100 {
		t.Fatalf("procCPUPct = %v, want 100", got)
	}
	if got := procCPUPct(100, 0); got != 0 {
		t.Fatalf("zero interval must yield 0, got %v", got)
	}
}
