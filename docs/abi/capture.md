# Capture ABI

This document defines the capture-side ABI between the embedded BPF object and
the Go loader in `internal/ebpf`.

It is narrower than the persisted SQL schema ABI. This contract matters to
contributors changing `collector.bpf.c`, `internal/abi/abi.yaml`,
`internal/abi/kernel_event.gen.h`, `internal/abi/tracepoints_gen.go`, or
`real_linux.go`.

## Scope

The capture ABI covers:

- the fact that `ltm` ships a prebuilt embedded BPF object
- the object/program/map names the loader expects
- the binary event layout sent from kernel to userspace
- the loader lifecycle required to start recording

It does not guarantee a public plugin API for third-party BPF objects.

## Embedded object contract

`ltm` does not compile BPF at runtime. The BPF C source is compiled ahead of
time into `internal/ebpf/collector_bpfel.o`, with companion Go bindings in
`internal/ebpf/collector_bpfel.go`, and that object is embedded into the Go
binary.

Source of truth:

- `internal/ebpf/collector.bpf.c`
- `internal/ebpf/collector_bpfel.o`
- `internal/abi/abi.yaml`
- `internal/abi/kernel_event.gen.h`
- `internal/abi/tracepoints_gen.go`
- `internal/ebpf/real_linux.go`

Required loader sequence:

1. Remove memlock limits.
2. Compute wall-clock boot time for timestamp normalization.
3. Load the embedded ELF object from memory.
4. Create the eBPF collection from that object.
5. Populate the `self_pid` map when present.
6. Resolve and attach the expected tracepoint programs.
7. Open a perf reader on the `events` map.
8. Decode kernel event records and convert them into `storage.Event`.

If this sequence changes materially, update this document.

## Compatibility rules

Compatible changes:

- adding optional tracepoints
- adding new `category` / `action` values without changing existing meanings
- adding new internal maps not required by the loader
- enriching userspace conversion logic without changing the kernel event binary layout

Breaking changes:

- renaming or removing the `events` map
- renaming or removing a required program listed in
  `internal/abi/tracepoints_gen.go`
- changing the kernel event struct layout without updating userspace decoding
- changing timestamp units or byte order
- requiring runtime compilation instead of the embedded object

Breaking changes must update both code and this ABI reference.

## Required object artifacts

The Go loader expects the embedded object to provide at least these artifacts.
The normative descriptions live in `internal/abi/abi.yaml`, which generates
`internal/abi/kernel_event.gen.h` and `internal/abi/tracepoints_gen.go`.

### Map names

| Name | Required | Purpose |
|---|---|---|
| `events` | yes | `BPF_MAP_TYPE_PERF_EVENT_ARRAY` used to stream kernel event records to userspace |
| `self_pid` | optional | one-entry map used to suppress events from the daemon process itself |

Other maps such as `pending_open`, `fd_path`, `scratch`, and `path_scratch`
are current implementation details of `collector.bpf.c`. They may change as
long as the loader-visible contract remains intact.

### Program names

Program names are part of the loader contract. The language-neutral manifest is
`internal/abi/abi.yaml`, and `internal/abi/tracepoints_gen.go` mirrors it for
runtime use.

Required non-optional examples:

- `trace_sched_process_fork`
- `trace_sched_process_exit`
- `trace_sys_enter_execve`
- `trace_sys_enter_open`
- `trace_sys_exit_open`
- `trace_sys_enter_read`
- `trace_sys_enter_write`
- `trace_sys_enter_connect`
- `trace_sys_enter_bind`
- `trace_sys_enter_listen`

Optional programs may be absent when their tracepoints are marked optional in
`internal/abi/tracepoints_gen.go`.

Rule:

- if a tracepoint entry is non-optional, the corresponding program must exist
  in the embedded object
- if it is optional, missing attachment is tolerated and recording continues

## Kernel event record ABI

Each emitted perf sample must match the binary layout defined in
`internal/abi/kernel_event.gen.h` and expected by
`internal/ebpf/real_linux.go`.

Current logical fields:

| Field | Type | Meaning |
|---|---|---|
| `ts_ns` | `u64` | boot-time nanoseconds |
| `bytes` | `u64` | requested byte count or action-specific size |
| `pid` | `u32` | actor pid |
| `uid` | `u32` | effective uid |
| `local_port`, `remote_port` | `u16` | network ports |
| `local_ip4`, `remote_ip4` | `u32` | IPv4 addresses |
| `comm` | `[16]byte` | process name |
| `category`, `action` | `[16]byte` | stable event labels |
| `path`, `old_path` | `[128]byte` | primary and secondary path payloads |
| `syscall_nr` | `u32` | syscall number or overloaded block field |
| `fd` | `s32` | file descriptor or target pid carrier for some actions |
| `aux` | `u32` | action-specific integer payload |

Rules:

- field order, width, and endianness must stay aligned with the Go
  `collectorEvent` struct and the C `struct ltm_kernel_event`
- timestamp unit is nanoseconds since boot, not unix epoch nanoseconds
- strings are fixed-size NUL-terminated byte arrays

Userspace is responsible for converting this transport record into the stable
persisted event schema.

## Timestamp normalization

Kernel records use boot-time nanoseconds from `bpf_ktime_get_boot_ns()`.
Userspace adds that duration to the wall-clock boot time derived from
`/proc/stat` `btime`.

This is part of the contract. If the kernel timestamp source changes,
userspace normalization must change with it.

## Transport contract

The current transport from kernel to userspace is a perf event array plus a
perf reader.

Rules:

- the `events` map must remain readable by `perf.NewReader`
- lost perf samples are surfaced through `record.LostSamples` and accumulated
  into `dropped_before` on the next persisted row

Changing transport to a ring buffer or another mechanism is a contract change
and must be documented here.

## Platform contract

- recording is Linux only
- the checked-in object is built for `x86_64` with `-D__TARGET_ARCH_x86`
- non-Linux builds must continue to fail through the stub collector rather than
  pretending to record

## Rebuild rule

After editing `internal/ebpf/collector.bpf.c`, run `make ebpf` to rebuild
`internal/ebpf/collector_bpfel.o` and `internal/ebpf/collector_bpfel.go`.

After editing `internal/abi/abi.yaml`, run `make generate`; if that change
affects the tracepoint table or kernel-event layout, run `make ebpf` too.

If `internal/abi/tracepoints_gen.go` expects a program not present in the
rebuilt object, startup should fail loudly as a build/configuration error.
