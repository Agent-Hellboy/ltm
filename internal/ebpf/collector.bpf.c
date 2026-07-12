//go:build ignore

#include "headers/common.h"
#include "../abi/kernel_event.gen.h"

#define PATH_MAX_LEN LTM_PATH_MAX_LEN
#define COMM_LEN LTM_COMM_LEN

typedef struct ltm_kernel_event event;

// Force bpf2go to emit a Go type for the event layout. The events perf-event
// array carries no BTF value type, so without a by-value reference nothing
// pulls the struct into the generated bindings and the Go mirror could drift
// again. Must be by value: bpf2go only generates Go structs for types used by
// value, not for a bare pointer's pointee.
const event _ltm_unused_event __attribute__((unused));

struct path_state {
	char path[PATH_MAX_LEN];
};

struct fd_key {
	__u32 pid;
	__u32 fd;
};

struct sockaddr_in {
	__u16 sin_family;
	__be16 sin_port;
	__be32 sin_addr;
	unsigned char padding[8];
};

struct trace_sched_process_exec {
	unsigned short common_type;
	unsigned char common_flags;
	unsigned char common_preempt_count;
	int common_pid;
	__u32 filename;
	int pid;
	int old_pid;
};

struct trace_sched_process_exit {
	unsigned short common_type;
	unsigned char common_flags;
	unsigned char common_preempt_count;
	int common_pid;
	char comm[COMM_LEN];
	int pid;
	int prio;
};

struct trace_sched_process_fork {
	unsigned short common_type;
	unsigned char common_flags;
	unsigned char common_preempt_count;
	int common_pid;
	char parent_comm[COMM_LEN];
	int parent_pid;
	char child_comm[COMM_LEN];
	int child_pid;
};

struct trace_sys_enter {
	unsigned short common_type;
	unsigned char common_flags;
	unsigned char common_preempt_count;
	int common_pid;
	long id;
	unsigned long args[6];
};

struct trace_sys_exit {
	unsigned short common_type;
	unsigned char common_flags;
	unsigned char common_preempt_count;
	int common_pid;
	long id;
	long ret;
};

struct trace_block_rq_issue {
	unsigned short common_type;
	unsigned char common_flags;
	unsigned char common_preempt_count;
	int common_pid;
	__u32 dev;
	__u64 sector;
	__u32 nr_sector;
	__u32 bytes;
	char rwbs[8];
	char comm[COMM_LEN];
};

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
} events SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, __u32);
	__type(value, struct path_state);
	__uint(max_entries, 4096);
} pending_open SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, struct fd_key);
	__type(value, struct path_state);
	__uint(max_entries, 16384);
} fd_path SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__type(key, __u32);
	__type(value, __u32);
	__uint(max_entries, 1);
} self_pid SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(key_size, sizeof(__u32));
	__uint(value_size, sizeof(event));
	__uint(max_entries, 1);
} scratch SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(key_size, sizeof(__u32));
	__uint(value_size, sizeof(struct path_state));
	__uint(max_entries, 1);
} path_scratch SEC(".maps");

static long (*bpf_map_delete_elem)(void *map, const void *key) = (void *)3;

static __always_inline void fill_common(event *ev) {
	__u64 pid_tgid = bpf_get_current_pid_tgid();
	// Boot-time clock (includes suspend) so timestamps line up with /proc/stat
	// btime in userspace; plain ktime (monotonic) drifts across suspend.
	ev->ts_ns = bpf_ktime_get_boot_ns();
	ev->pid = pid_tgid >> 32;
	ev->uid = (bpf_get_current_uid_gid() & 0xffffffff);
	bpf_get_current_comm(ev->comm, sizeof(ev->comm));
}

static __always_inline int should_skip(void) {
	__u32 zero = 0;
	__u32 *self = bpf_map_lookup_elem(&self_pid, &zero);
	if (!self) {
		return 0;
	}
	__u32 pid = bpf_get_current_pid_tgid() >> 32;
	return pid == *self;
}

static __always_inline int path_boundary(char c) {
	return c == 0 || c == '/';
}

static __always_inline int path_ignored_prefix(const char *prefix) {
	if (prefix[0] != '/') {
		return 0;
	}
	if (prefix[1] == 'p' && prefix[2] == 'r' && prefix[3] == 'o' && prefix[4] == 'c' &&
	    path_boundary(prefix[5])) {
		return 1;
	}
	if (prefix[1] == 's' && prefix[2] == 'y' && prefix[3] == 's' && path_boundary(prefix[4])) {
		return 1;
	}
	if (prefix[1] == 'd' && prefix[2] == 'e' && prefix[3] == 'v' && path_boundary(prefix[4])) {
		return 1;
	}
	return 0;
}

static __always_inline int path_ignored_user(const char *path) {
	char buf[7] = {};
	if (bpf_probe_read_user_str(buf, sizeof(buf), path) <= 0) {
		return 0;
	}
	return path_ignored_prefix(buf);
}

static __always_inline void set_category_action(event *ev, const char *category, const char *action) {
	__builtin_memcpy(ev->category, category, 16);
	__builtin_memcpy(ev->action, action, 16);
}

static __always_inline void submit(void *ctx, event *ev) {
	bpf_perf_event_output(ctx, &events, BPF_F_CURRENT_CPU, ev, sizeof(*ev));
}

static __always_inline event *reserve_event(void) {
	__u32 zero = 0;
	event *ev = bpf_map_lookup_elem(&scratch, &zero);
	if (!ev) {
		return 0;
	}
	__builtin_memset(ev, 0, sizeof(*ev));
	return ev;
}

static __always_inline int read_path(char *dst, const char *src) {
	long n = bpf_probe_read_user_str(dst, PATH_MAX_LEN, src);
	return n > 0 ? 0 : -1;
}

static __always_inline struct path_state *path_temp(void) {
	__u32 zero = 0;
	return bpf_map_lookup_elem(&path_scratch, &zero);
}

static __always_inline void save_pending_open(__u32 pid, const char *path) {
	if (path_ignored_user(path)) {
		return;
	}
	struct path_state *state = path_temp();
	if (!state) {
		return;
	}
	bpf_probe_read_user_str(state->path, sizeof(state->path), path);
	bpf_map_update_elem(&pending_open, &pid, state, BPF_ANY);
}

static __always_inline void save_fd_path(__u32 pid, __u32 fd, const char *path) {
	if (fd > 1024 || path_ignored_user(path)) {
		return;
	}
	struct path_state *state = path_temp();
	if (!state) {
		return;
	}
	struct fd_key key = {.pid = pid, .fd = fd};
	bpf_probe_read_user_str(state->path, sizeof(state->path), path);
	bpf_map_update_elem(&fd_path, &key, state, BPF_ANY);
}

static __always_inline void delete_fd_path(__u32 pid, __u32 fd) {
	struct fd_key key = {.pid = pid, .fd = fd};
	bpf_map_delete_elem(&fd_path, &key);
}

static __always_inline struct path_state *lookup_fd_path(__u32 pid, __u32 fd) {
	struct fd_key key = {.pid = pid, .fd = fd};
	return bpf_map_lookup_elem(&fd_path, &key);
}

static __always_inline int emit_event(void *ctx, const char *category, const char *action,
				      const char *path, const char *old_path, __u64 bytes,
				      __s32 fd, __u32 aux, long syscall_nr) {
	if (should_skip()) {
		return 0;
	}
	if (path && path_ignored_user(path)) {
		return 0;
	}
	if (old_path && path_ignored_user(old_path)) {
		return 0;
	}

	event *ev = reserve_event();
	if (!ev) {
		return 0;
	}
	fill_common(ev);
	set_category_action(ev, category, action);
	ev->bytes = bytes;
	ev->fd = fd;
	ev->aux = aux;
	ev->syscall_nr = (__u32)syscall_nr;
	if (path) {
		read_path(ev->path, path);
	}
	if (old_path) {
		read_path(ev->old_path, old_path);
	}
	submit(ctx, ev);
	return 0;
}

static __always_inline int emit_fd_io(void *ctx, const char *category, const char *action,
				      __u32 fd, __u64 bytes, long syscall_nr) {
	if (should_skip()) {
		return 0;
	}
	__u32 pid = bpf_get_current_pid_tgid() >> 32;
	struct path_state *state = lookup_fd_path(pid, fd);
	if (!state) {
		return emit_event(ctx, category, action, 0, 0, bytes, (__s32)fd, 0, syscall_nr);
	}
	if (path_ignored_prefix(state->path)) {
		return 0;
	}

	event *ev = reserve_event();
	if (!ev) {
		return 0;
	}
	fill_common(ev);
	set_category_action(ev, category, action);
	ev->bytes = bytes;
	ev->fd = (__s32)fd;
	ev->syscall_nr = (__u32)syscall_nr;
	__builtin_memcpy(ev->path, state->path, sizeof(ev->path));
	submit(ctx, ev);
	return 0;
}

static __always_inline int emit_sockaddr(void *ctx, const char *action, const void *uaddr, long syscall_nr) {
	if (should_skip() || !uaddr) {
		return 0;
	}

	__u16 family = 0;
	if (bpf_probe_read_user(&family, sizeof(family), uaddr) < 0 || family != 2) {
		return emit_event(ctx, "network", action, 0, 0, 0, -1, 0, syscall_nr);
	}

	__be16 port = 0;
	__u32 ip4 = 0;
	if (bpf_probe_read_user(&port, sizeof(port), uaddr + 2) < 0) {
		return emit_event(ctx, "network", action, 0, 0, 0, -1, 0, syscall_nr);
	}
	if (bpf_probe_read_user(&ip4, sizeof(ip4), uaddr + 4) < 0) {
		return emit_event(ctx, "network", action, 0, 0, 0, -1, 0, syscall_nr);
	}

	event *ev = reserve_event();
	if (!ev) {
		return 0;
	}
	fill_common(ev);
	set_category_action(ev, "network", action);
	ev->syscall_nr = (__u32)syscall_nr;
	if (action[0] == 'b' || action[0] == 'l') {
		ev->local_port = __builtin_bswap16(port);
		ev->local_ip4 = ip4;
	} else {
		ev->remote_port = __builtin_bswap16(port);
		ev->remote_ip4 = ip4;
	}
	submit(ctx, ev);
	return 0;
}

static __always_inline int handle_open_enter(struct trace_sys_enter *ctx, int path_arg) {
	__u32 pid = bpf_get_current_pid_tgid() >> 32;
	const char *filename = (const char *)ctx->args[path_arg];
	save_pending_open(pid, filename);
	return emit_event(ctx, "file", "open", filename, 0, 0, -1, (__u32)ctx->args[2], ctx->id);
}

static __always_inline int handle_open_exit(struct trace_sys_exit *ctx) {
	if (ctx->ret < 0) {
		__u32 pid = bpf_get_current_pid_tgid() >> 32;
		bpf_map_delete_elem(&pending_open, &pid);
		return 0;
	}
	__u32 pid = bpf_get_current_pid_tgid() >> 32;
	struct path_state *pending = bpf_map_lookup_elem(&pending_open, &pid);
	if (!pending) {
		return 0;
	}
	save_fd_path(pid, (__u32)ctx->ret, pending->path);
	bpf_map_delete_elem(&pending_open, &pid);
	return 0;
}

SEC("tracepoint/sched/sched_process_fork")
int trace_sched_process_fork(struct trace_sched_process_fork *ctx) {
	if (should_skip()) {
		return 0;
	}
	event *ev = reserve_event();
	if (!ev) {
		return 0;
	}
	fill_common(ev);
	set_category_action(ev, "process", "fork");
	ev->pid = ctx->child_pid;
	ev->aux = (__u32)ctx->parent_pid;
	__builtin_memcpy(ev->comm, ctx->child_comm, sizeof(ev->comm));
	submit(ctx, ev);
	return 0;
}

SEC("tracepoint/sched/sched_process_exit")
int trace_sched_process_exit(struct trace_sched_process_exit *ctx) {
	if (should_skip()) {
		return 0;
	}
	event *ev = reserve_event();
	if (!ev) {
		return 0;
	}
	fill_common(ev);
	ev->pid = ctx->pid;
	__builtin_memcpy(ev->comm, ctx->comm, sizeof(ev->comm));
	set_category_action(ev, "process", "exit");
	submit(ctx, ev);
	return 0;
}

SEC("tracepoint/block/block_rq_issue")
int trace_block_rq_issue(struct trace_block_rq_issue *ctx) {
	if (should_skip()) {
		return 0;
	}
	event *ev = reserve_event();
	if (!ev) {
		return 0;
	}
	fill_common(ev);
	set_category_action(ev, "block", "io");
	ev->bytes = ctx->bytes;
	ev->aux = ctx->dev;
	ev->syscall_nr = ctx->nr_sector;
	__builtin_memcpy(ev->comm, ctx->comm, sizeof(ev->comm));
	__builtin_memcpy(ev->path, ctx->rwbs, 8);
	submit(ctx, ev);
	return 0;
}

SEC("tracepoint/syscalls/sys_enter_execve")
int trace_sys_enter_execve(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "process", "exec", (const char *)ctx->args[0], 0, 0, -1, 0, ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_execveat")
int trace_sys_enter_execveat(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "process", "exec", (const char *)ctx->args[1], 0, 0, -1, 0, ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_clone")
int trace_sys_enter_clone(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "process", "clone", 0, 0, 0, -1, (__u32)ctx->args[0], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_clone3")
int trace_sys_enter_clone3(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "process", "clone", 0, 0, 0, -1, 0, ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_kill")
int trace_sys_enter_kill(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "process", "kill", 0, 0, 0, (__s32)ctx->args[0], (__u32)ctx->args[1], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_tgkill")
int trace_sys_enter_tgkill(struct trace_sys_enter *ctx) {
	// tgkill(tgid, tid, sig): target pid is args[1] (tid), signal is args[2].
	return emit_event(ctx, "process", "kill", 0, 0, 0, (__s32)ctx->args[1], (__u32)ctx->args[2], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_open")
int trace_sys_enter_open(struct trace_sys_enter *ctx) {
	return handle_open_enter(ctx, 0);
}

SEC("tracepoint/syscalls/sys_enter_openat")
int trace_sys_enter_openat(struct trace_sys_enter *ctx) {
	return handle_open_enter(ctx, 1);
}

SEC("tracepoint/syscalls/sys_enter_openat2")
int trace_sys_enter_openat2(struct trace_sys_enter *ctx) {
	return handle_open_enter(ctx, 1);
}

SEC("tracepoint/syscalls/sys_exit_open")
int trace_sys_exit_open(struct trace_sys_exit *ctx) {
	return handle_open_exit(ctx);
}

SEC("tracepoint/syscalls/sys_exit_openat")
int trace_sys_exit_openat(struct trace_sys_exit *ctx) {
	return handle_open_exit(ctx);
}

SEC("tracepoint/syscalls/sys_exit_openat2")
int trace_sys_exit_openat2(struct trace_sys_exit *ctx) {
	return handle_open_exit(ctx);
}

SEC("tracepoint/syscalls/sys_enter_close")
int trace_sys_enter_close(struct trace_sys_enter *ctx) {
	__u32 pid = bpf_get_current_pid_tgid() >> 32;
	__u32 fd = (__u32)ctx->args[0];
	delete_fd_path(pid, fd);
	return emit_fd_io(ctx, "file", "close", fd, 0, ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_read")
int trace_sys_enter_read(struct trace_sys_enter *ctx) {
	return emit_fd_io(ctx, "file", "read", (__u32)ctx->args[0], ctx->args[2], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_pread64")
int trace_sys_enter_pread64(struct trace_sys_enter *ctx) {
	return emit_fd_io(ctx, "file", "read", (__u32)ctx->args[0], ctx->args[2], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_write")
int trace_sys_enter_write(struct trace_sys_enter *ctx) {
	return emit_fd_io(ctx, "file", "write", (__u32)ctx->args[0], ctx->args[2], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_pwrite64")
int trace_sys_enter_pwrite64(struct trace_sys_enter *ctx) {
	return emit_fd_io(ctx, "file", "write", (__u32)ctx->args[0], ctx->args[2], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_readv")
int trace_sys_enter_readv(struct trace_sys_enter *ctx) {
	return emit_fd_io(ctx, "file", "read", (__u32)ctx->args[0], 0, ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_writev")
int trace_sys_enter_writev(struct trace_sys_enter *ctx) {
	return emit_fd_io(ctx, "file", "write", (__u32)ctx->args[0], 0, ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_lseek")
int trace_sys_enter_lseek(struct trace_sys_enter *ctx) {
	return emit_fd_io(ctx, "file", "seek", (__u32)ctx->args[0], ctx->args[1], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_truncate")
int trace_sys_enter_truncate(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "file", "truncate", (const char *)ctx->args[0], 0, ctx->args[1], -1, 0, ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_ftruncate")
int trace_sys_enter_ftruncate(struct trace_sys_enter *ctx) {
	return emit_fd_io(ctx, "file", "truncate", (__u32)ctx->args[0], ctx->args[1], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_unlinkat")
int trace_sys_enter_unlinkat(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "file", "unlink", (const char *)ctx->args[1], 0, 0, -1, 0, ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_renameat2")
int trace_sys_enter_renameat2(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "file", "rename", (const char *)ctx->args[3], (const char *)ctx->args[1], 0, -1, 0, ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_linkat")
int trace_sys_enter_linkat(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "file", "link", (const char *)ctx->args[3], (const char *)ctx->args[1], 0, -1, 0, ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_symlinkat")
int trace_sys_enter_symlinkat(struct trace_sys_enter *ctx) {
	// symlinkat(target, newdirfd, linkpath): linkpath is args[2], target args[0].
	return emit_event(ctx, "file", "symlink", (const char *)ctx->args[2], (const char *)ctx->args[0], 0, -1, 0, ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_mkdirat")
int trace_sys_enter_mkdirat(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "file", "mkdir", (const char *)ctx->args[1], 0, 0, -1, (__u32)ctx->args[2], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_mkdir")
int trace_sys_enter_mkdir(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "file", "mkdir", (const char *)ctx->args[0], 0, 0, -1, (__u32)ctx->args[1], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_rmdir")
int trace_sys_enter_rmdir(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "file", "rmdir", (const char *)ctx->args[0], 0, 0, -1, 0, ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_readlinkat")
int trace_sys_enter_readlinkat(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "file", "readlink", (const char *)ctx->args[1], 0, 0, -1, 0, ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_chmod")
int trace_sys_enter_chmod(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "file", "chmod", (const char *)ctx->args[0], 0, 0, -1, (__u32)ctx->args[1], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_fchmod")
int trace_sys_enter_fchmod(struct trace_sys_enter *ctx) {
	return emit_fd_io(ctx, "file", "chmod", (__u32)ctx->args[0], ctx->args[1], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_fchmodat")
int trace_sys_enter_fchmodat(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "file", "chmod", (const char *)ctx->args[1], 0, 0, -1, (__u32)ctx->args[2], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_chown")
int trace_sys_enter_chown(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "file", "chown", (const char *)ctx->args[0], 0, 0, -1, (__u32)ctx->args[1], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_fchown")
int trace_sys_enter_fchown(struct trace_sys_enter *ctx) {
	return emit_fd_io(ctx, "file", "chown", (__u32)ctx->args[0], ctx->args[1], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_fchownat")
int trace_sys_enter_fchownat(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "file", "chown", (const char *)ctx->args[1], 0, 0, -1, (__u32)ctx->args[2], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_newfstatat")
int trace_sys_enter_newfstatat(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "file", "stat", (const char *)ctx->args[1], 0, 0, -1, 0, ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_access")
int trace_sys_enter_access(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "file", "access", (const char *)ctx->args[0], 0, 0, -1, (__u32)ctx->args[1], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_faccessat")
int trace_sys_enter_faccessat(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "file", "access", (const char *)ctx->args[1], 0, 0, -1, (__u32)ctx->args[2], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_pipe2")
int trace_sys_enter_pipe2(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "file", "pipe", 0, 0, 0, -1, (__u32)ctx->args[1], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_dup")
int trace_sys_enter_dup(struct trace_sys_enter *ctx) {
	return emit_fd_io(ctx, "file", "dup", (__u32)ctx->args[0], 0, ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_dup2")
int trace_sys_enter_dup2(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "file", "dup", 0, 0, 0, (__s32)ctx->args[0], (__u32)ctx->args[1], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_dup3")
int trace_sys_enter_dup3(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "file", "dup", 0, 0, 0, (__s32)ctx->args[0], (__u32)ctx->args[1], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_mmap")
int trace_sys_enter_mmap(struct trace_sys_enter *ctx) {
	return emit_fd_io(ctx, "memory", "mmap", (__u32)ctx->args[4], ctx->args[1], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_munmap")
int trace_sys_enter_munmap(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "memory", "munmap", 0, 0, ctx->args[1], -1, 0, ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_mprotect")
int trace_sys_enter_mprotect(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "memory", "mprotect", 0, 0, ctx->args[1], -1, (__u32)ctx->args[2], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_sendfile")
int trace_sys_enter_sendfile(struct trace_sys_enter *ctx) {
	emit_fd_io(ctx, "file", "read", (__u32)ctx->args[1], ctx->args[2], ctx->id);
	return emit_fd_io(ctx, "file", "write", (__u32)ctx->args[0], ctx->args[2], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_copy_file_range")
int trace_sys_enter_copy_file_range(struct trace_sys_enter *ctx) {
	emit_fd_io(ctx, "file", "read", (__u32)ctx->args[0], ctx->args[4], ctx->id);
	return emit_fd_io(ctx, "file", "write", (__u32)ctx->args[2], ctx->args[4], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_splice")
int trace_sys_enter_splice(struct trace_sys_enter *ctx) {
	// splice(fd_in, off_in, fd_out, off_out, len, flags): len is args[4].
	emit_fd_io(ctx, "file", "read", (__u32)ctx->args[0], ctx->args[4], ctx->id);
	return emit_fd_io(ctx, "file", "write", (__u32)ctx->args[2], ctx->args[4], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_socket")
int trace_sys_enter_socket(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "network", "socket", 0, 0, 0, -1, (__u32)ctx->args[0], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_connect")
int trace_sys_enter_connect(struct trace_sys_enter *ctx) {
	return emit_sockaddr(ctx, "connect", (const void *)ctx->args[1], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_bind")
int trace_sys_enter_bind(struct trace_sys_enter *ctx) {
	return emit_sockaddr(ctx, "bind", (const void *)ctx->args[1], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_listen")
int trace_sys_enter_listen(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "network", "listen", 0, 0, 0, (__s32)ctx->args[0], (__u32)ctx->args[1], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_accept")
int trace_sys_enter_accept(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "network", "accept", 0, 0, 0, (__s32)ctx->args[0], 0, ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_accept4")
int trace_sys_enter_accept4(struct trace_sys_enter *ctx) {
	return emit_event(ctx, "network", "accept", 0, 0, 0, (__s32)ctx->args[0], (__u32)ctx->args[3], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_sendto")
int trace_sys_enter_sendto(struct trace_sys_enter *ctx) {
	emit_fd_io(ctx, "network", "send", (__u32)ctx->args[0], ctx->args[2], ctx->id);
	return emit_sockaddr(ctx, "send", (const void *)ctx->args[4], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_recvfrom")
int trace_sys_enter_recvfrom(struct trace_sys_enter *ctx) {
	emit_fd_io(ctx, "network", "recv", (__u32)ctx->args[0], ctx->args[2], ctx->id);
	return emit_sockaddr(ctx, "recv", (const void *)ctx->args[4], ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_sendmsg")
int trace_sys_enter_sendmsg(struct trace_sys_enter *ctx) {
	return emit_fd_io(ctx, "network", "send", (__u32)ctx->args[0], 0, ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_recvmsg")
int trace_sys_enter_recvmsg(struct trace_sys_enter *ctx) {
	return emit_fd_io(ctx, "network", "recv", (__u32)ctx->args[0], 0, ctx->id);
}

SEC("tracepoint/syscalls/sys_enter_shutdown")
int trace_sys_enter_shutdown(struct trace_sys_enter *ctx) {
	return emit_fd_io(ctx, "network", "shutdown", (__u32)ctx->args[0], ctx->args[1], ctx->id);
}

char LICENSE[] SEC("license") = "Dual MIT/GPL";
