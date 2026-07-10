# AGENTS.md

Contributor guide for `ltm`. User-facing overview: `README.md`. Pipeline:
`docs/architecture.md`. Privileges/data: `docs/security.md`.

## Layout

```text
cmd/ltm/           entrypoint only (public import surface)
internal/
  cli/             commands, global flags, daemon spawn
  daemon/          batching, flushLoop, collector wiring
  collector/       ignore-path filter, bounded buffer
  ebpf/            BPF C, embedded .o, Linux collector + stub
  storage/         SQLite store, Filter, prune
  agent/           NL question → SQL (external CLI)
  diff/            time-window machine-state diff
  query/           deterministic NL templates
tests/             integration.sh (Linux + root)
docs/              architecture, security
```

All implementation under `internal/`. Do not add packages at the repo root.

## Build

```bash
go test ./...
go build -o bin/ltm ./cmd/ltm
make integration          # eBPF smoke; Linux + root
make ebpf                 # after editing collector.bpf.c (clang, Linux)
```

CI: unit tests (Ubuntu + macOS), integration (Ubuntu), BPF rebuild + binary build.

## Hard rules

**CLI.** Global flags before the subcommand. `start` re-execs
`daemon --foreground` with those flags forwarded — keep that ordering.
Defaults: DB `~/.local/share/ltm/ltm.db`, PID `~/.local/run/ltm.pid`.

**Events.** Everything is a `storage.Event`. Keep category/action strings
stable (`process`/`file`/`network`/`memory`/`block`; `exec`/`exit`/`open`/
`write`/`read`/`rename`/`unlink`/`bind`/`connect`/`listen`/…). Bump
`SchemaVersion` only for incompatible replay/query changes. Update
`SchemaDoc` when the `events` schema changes (shown by `ltm query sql` and
embedded in agent prompts).

**Storage.** `Open` = sole writer (WAL, `MaxOpenConns(1)`). `OpenReadOnly` for
every read path (`query_only=ON`); error if the DB file is missing — never
create on a read path. Extend `Filter` instead of new one-off `EventsByX`
helpers. `DroppedBefore` is additive (`SUM`); do not overwrite. `watch` uses
`EventsAfterID` / `LatestEventID` — keep them id-ordered and cheap.

**eBPF.** Source `collector.bpf.c`, embedded `collector_bpfel.o`, loader
`real_linux.go`, stub `real_stub.go`. x86_64 only (`__TARGET_ARCH_x86`).
Headers under `headers/` are minimal stubs. Rebuild `.o` on Linux after BPF
edits; CI rebuilds too. No simulated collector — use `ltm benchmark`.

**Agent.** `LTM_AGENT` / `--agent`: `claude|codex|cursor|gemini|auto|<custom>`.
SQL runs only via `OpenReadOnly` + `RawSQL`; `ExtractSQL` rejects non-SELECT /
multi-statement. Agent failure → stderr warning + `internal/query` templates.
Unit tests use fake shell scripts — never call a real agent CLI.

**Style.** Small focused diffs; extend existing engines; metadata only (no file
contents); respect ignore paths. No `Co-Authored-By` trailers. No
`replace` directives for `github.com/cilium/ebpf`. No secrets or one-off host
paths. Do not change default ignore paths without updating `docs/security.md`.

## Manual check

Linux + root:

```bash
sudo ./bin/ltm start
./bin/ltm status
echo test >> /tmp/probe.txt
./bin/ltm timeline --since 10m
./bin/ltm diff --from 10m --to now
./bin/ltm query "who modified /tmp/probe.txt?"
./bin/ltm query sql "SELECT category, count(*) FROM events GROUP BY category"
sudo ./bin/ltm stop
```

Any OS: `./bin/ltm benchmark --count 500`, then timeline/diff/query against that DB.
