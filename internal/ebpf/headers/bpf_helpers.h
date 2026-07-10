/* Minimal bpf helper declarations for bpf2go-generated programs. */
#pragma once

#define __uint(name, val) int (*name)[val]
#define __type(name, val) typeof(val) *name
#define __array(name, val) typeof(val) *name[]

#define SEC(name) \
	__attribute__((section(name), used))

#undef __always_inline
#define __always_inline inline __attribute__((always_inline))

enum bpf_map_type {
	BPF_MAP_TYPE_UNSPEC = 0,
	BPF_MAP_TYPE_HASH = 1,
	BPF_MAP_TYPE_ARRAY = 2,
	BPF_MAP_TYPE_PERF_EVENT_ARRAY = 4,
	BPF_MAP_TYPE_PERCPU_HASH = 5,
	BPF_MAP_TYPE_PERCPU_ARRAY = 6,
};

enum {
	BPF_ANY = 0,
	BPF_F_CURRENT_CPU = 0xffffffffULL,
};

static void *(*bpf_map_lookup_elem)(void *map, const void *key) = (void *)1;
static long (*bpf_map_update_elem)(void *map, const void *key, const void *value, __u64 flags) = (void *)2;
static long (*bpf_probe_read)(void *dst, __u32 size, const void *unsafe_ptr) = (void *)4;
static __u64 (*bpf_ktime_get_ns)(void) = (void *)5;
static __u64 (*bpf_ktime_get_boot_ns)(void) = (void *)125;
static long (*bpf_get_current_pid_tgid)(void) = (void *)14;
static long (*bpf_get_current_uid_gid)(void) = (void *)15;
static long (*bpf_get_current_comm)(void *buf, __u32 size) = (void *)16;
static long (*bpf_probe_read_user)(void *dst, __u32 size, const void *unsafe_ptr) = (void *)112;
static long (*bpf_probe_read_kernel)(void *dst, __u32 size, const void *unsafe_ptr) = (void *)113;
static long (*bpf_probe_read_user_str)(void *dst, __u32 size, const void *unsafe_ptr) = (void *)114;
static long (*bpf_probe_read_kernel_str)(void *dst, __u32 size, const void *unsafe_ptr) = (void *)115;
static long (*bpf_perf_event_output)(void *ctx, void *map, __u64 flags, void *data, __u32 size) = (void *)25;
