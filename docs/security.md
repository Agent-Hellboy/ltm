# Security

## Current MVP

- Demo mode does not need elevated privileges.
- The store captures metadata only.
- File contents are not recorded.
- Ignored paths include system pseudo-filesystems and common cache directories.

## Future eBPF mode

- Expected to run as root or with sufficient BPF/perf capabilities.
- The kernel-side program should remain minimal.
- Userspace is responsible for filtering, enrichment, batching, and storage.

