//go:build ignore

#include "headers/common.h"

#define PATH_MAX_LEN 128
#define COMM_LEN 16

enum {
	EVENT_PROCESS = 1,
	EVENT_FILE = 2,
	EVENT_NETWORK = 3,
};

enum {
	ACTION_EXEC = 1,
	ACTION_EXIT = 2,
	ACTION_OPEN = 3,
	ACTION_WRITE = 4,
	ACTION_RENAME = 5,
	ACTION_UNLINK = 6,
	ACTION_CONNECT = 7,
	ACTION_BIND = 8,
};

struct event {
	__u64 ts_ns;
	__u64 bytes;
	__u32 pid;
	__u32 uid;
	__u16 local_port;
	__u16 remote_port;
	__u32 local_ip4;
	__u32 remote_ip4;
	char comm[COMM_LEN];
	char category[16];
	char action[16];
	char path[PATH_MAX_LEN];
	char old_path[PATH_MAX_LEN];
};

struct path_state {
	char path[PATH_MAX_LEN];
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

struct trace_sys_enter {
	unsigned short common_type;
	unsigned char common_flags;
	unsigned char common_preempt_count;
	int common_pid;
	long id;
	unsigned long args[6];
};

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
} events SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, __u32);
	__type(value, struct path_state);
	__uint(max_entries, 4096);
} last_path SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(key_size, sizeof(__u32));
	__uint(value_size, sizeof(struct event));
	__uint(max_entries, 1);
} scratch SEC(".maps");

static __always_inline void fill_common(struct event *ev) {
	__u64 pid_tgid = bpf_get_current_pid_tgid();
	ev->ts_ns = bpf_ktime_get_ns();
	ev->pid = pid_tgid >> 32;
	ev->uid = (bpf_get_current_uid_gid() & 0xffffffff);
	bpf_get_current_comm(ev->comm, sizeof(ev->comm));
}

static __always_inline void submit(void *ctx, struct event *ev) {
	bpf_perf_event_output(ctx, &events, BPF_F_CURRENT_CPU, ev, sizeof(*ev));
}

static __always_inline struct event *reserve_event(void) {
	__u32 zero = 0;
	struct event *ev = bpf_map_lookup_elem(&scratch, &zero);
	if (!ev) {
		return 0;
	}
	__builtin_memset(ev, 0, sizeof(*ev));
	return ev;
}

static __always_inline void set_category_action(struct event *ev, const char *category, const char *action) {
	__builtin_memcpy(ev->category, category, 16);
	__builtin_memcpy(ev->action, action, 16);
}

static __always_inline void save_path(__u32 pid, const char *path) {
	struct path_state state = {};
	bpf_probe_read_user_str(state.path, sizeof(state.path), path);
	bpf_map_update_elem(&last_path, &pid, &state, BPF_ANY);
}

static __always_inline void maybe_write_from_last_path(void *ctx, __u64 bytes) {
	__u32 pid = bpf_get_current_pid_tgid() >> 32;
	struct path_state *state = bpf_map_lookup_elem(&last_path, &pid);
	if (!state) {
		return;
	}

	struct event *ev = reserve_event();
	if (!ev) {
		return;
	}
	fill_common(ev);
	set_category_action(ev, "file", "write");
	ev->bytes = bytes;
	__builtin_memcpy(ev->path, state->path, sizeof(ev->path));
	submit(ctx, ev);
}

SEC("tracepoint/syscalls/sys_enter_execve")
int trace_sys_enter_execve(struct trace_sys_enter *ctx) {
	struct event *ev = reserve_event();
	if (!ev) {
		return 0;
	}
	fill_common(ev);
	set_category_action(ev, "process", "exec");
	const char *filename = (const char *)ctx->args[0];
	bpf_probe_read_user_str(ev->path, sizeof(ev->path), filename);
	submit(ctx, ev);
	return 0;
}

SEC("tracepoint/syscalls/sys_enter_execveat")
int trace_sys_enter_execveat(struct trace_sys_enter *ctx) {
	struct event *ev = reserve_event();
	if (!ev) {
		return 0;
	}
	fill_common(ev);
	set_category_action(ev, "process", "exec");
	const char *filename = (const char *)ctx->args[1];
	bpf_probe_read_user_str(ev->path, sizeof(ev->path), filename);
	submit(ctx, ev);
	return 0;
}

SEC("tracepoint/sched/sched_process_exit")
int trace_sched_process_exit(struct trace_sched_process_exit *ctx) {
	struct event *ev = reserve_event();
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

SEC("tracepoint/syscalls/sys_enter_openat")
int trace_sys_enter_openat(struct trace_sys_enter *ctx) {
	__u32 pid = bpf_get_current_pid_tgid() >> 32;
	const char *filename = (const char *)ctx->args[1];
	save_path(pid, filename);

	struct event *ev = reserve_event();
	if (!ev) {
		return 0;
	}
	fill_common(ev);
	set_category_action(ev, "file", "open");
	bpf_probe_read_user_str(ev->path, sizeof(ev->path), filename);
	submit(ctx, ev);
	return 0;
}

SEC("tracepoint/syscalls/sys_enter_openat2")
int trace_sys_enter_openat2(struct trace_sys_enter *ctx) {
	__u32 pid = bpf_get_current_pid_tgid() >> 32;
	const char *filename = (const char *)ctx->args[1];
	save_path(pid, filename);

	struct event *ev = reserve_event();
	if (!ev) {
		return 0;
	}
	fill_common(ev);
	set_category_action(ev, "file", "open");
	bpf_probe_read_user_str(ev->path, sizeof(ev->path), filename);
	submit(ctx, ev);
	return 0;
}

SEC("tracepoint/syscalls/sys_enter_write")
int trace_sys_enter_write(struct trace_sys_enter *ctx) {
	maybe_write_from_last_path(ctx, ctx->args[2]);
	return 0;
}

SEC("tracepoint/syscalls/sys_enter_unlinkat")
int trace_sys_enter_unlinkat(struct trace_sys_enter *ctx) {
	const char *pathname = (const char *)ctx->args[1];
	struct event *ev = reserve_event();
	if (!ev) {
		return 0;
	}
	fill_common(ev);
	set_category_action(ev, "file", "unlink");
	bpf_probe_read_user_str(ev->path, sizeof(ev->path), pathname);
	submit(ctx, ev);
	return 0;
}

SEC("tracepoint/syscalls/sys_enter_renameat2")
int trace_sys_enter_renameat2(struct trace_sys_enter *ctx) {
	const char *oldpath = (const char *)ctx->args[1];
	const char *newpath = (const char *)ctx->args[3];
	struct event *ev = reserve_event();
	if (!ev) {
		return 0;
	}
	fill_common(ev);
	set_category_action(ev, "file", "rename");
	bpf_probe_read_user_str(ev->old_path, sizeof(ev->old_path), oldpath);
	bpf_probe_read_user_str(ev->path, sizeof(ev->path), newpath);
	submit(ctx, ev);
	return 0;
}

static __always_inline int read_sockaddr(struct sockaddr_in *dst, const void *addr) {
	return bpf_probe_read_user(dst, sizeof(*dst), addr);
}

SEC("tracepoint/syscalls/sys_enter_connect")
int trace_sys_enter_connect(struct trace_sys_enter *ctx) {
	const void *uaddr = (const void *)ctx->args[1];
	struct sockaddr_in addr = {};
	if (read_sockaddr(&addr, uaddr) < 0 || addr.sin_family != 2) {
		return 0;
	}

	struct event *ev = reserve_event();
	if (!ev) {
		return 0;
	}
	fill_common(ev);
	set_category_action(ev, "network", "connect");
	ev->remote_port = __builtin_bswap16(addr.sin_port);
	ev->remote_ip4 = addr.sin_addr;
	submit(ctx, ev);
	return 0;
}

SEC("tracepoint/syscalls/sys_enter_bind")
int trace_sys_enter_bind(struct trace_sys_enter *ctx) {
	const void *uaddr = (const void *)ctx->args[1];
	struct sockaddr_in addr = {};
	if (read_sockaddr(&addr, uaddr) < 0 || addr.sin_family != 2) {
		return 0;
	}

	struct event *ev = reserve_event();
	if (!ev) {
		return 0;
	}
	fill_common(ev);
	set_category_action(ev, "network", "bind");
	ev->local_port = __builtin_bswap16(addr.sin_port);
	ev->local_ip4 = addr.sin_addr;
	submit(ctx, ev);
	return 0;
}

char LICENSE[] SEC("license") = "Dual MIT/GPL";
