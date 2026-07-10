# Security

## Recording

- Recording (`ltm start`) runs the eBPF collector and requires root or sufficient BPF/perf capabilities (`CAP_BPF`, `CAP_PERFMON`).
- The read/query commands do not need privileges; they open the database read-only.
- The store captures metadata only.
- File contents are not recorded.
- Ignored paths include system pseudo-filesystems and common cache directories.

## Kernel side

- The kernel-side program stays minimal.
- Userspace is responsible for filtering, enrichment, batching, and storage.

