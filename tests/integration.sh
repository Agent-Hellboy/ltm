#!/usr/bin/env bash
# End-to-end smoke test: record real activity with the eBPF collector, then
# exercise the query surface. Requires a Linux host with root (sudo) and a
# kernel that supports tracepoint BPF programs.
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
trap 'sudo rm -rf "${work_dir}"' EXIT

ltm="${work_dir}/ltm"
db="${work_dir}/ltm.db"
pid_file="${work_dir}/ltm.pid"
ignored_dir="${work_dir}/ignored"
mkdir -p "${ignored_dir}"

go build -o "${ltm}" ./cmd/ltm

"${ltm}" version

# Start recording (eBPF; needs root). start forks the daemon and writes the
# pidfile; the daemon's attach summary prints to this script's output.
sudo "${ltm}" --db "${db}" --pidfile "${pid_file}" --ignore-path "${ignored_dir}" start
sleep 3
if ! sudo "${ltm}" --db "${db}" --pidfile "${pid_file}" status | grep -q "alive=true"; then
  echo "daemon failed to start (eBPF unsupported on this host?)" >&2
  exit 1
fi

# Generate real, identifiable activity.
mark="ltm-ci-$$"
echo "config change" >"/tmp/${mark}.conf"
cat "/tmp/${mark}.conf" >/dev/null
mv "/tmp/${mark}.conf" "/tmp/${mark}.renamed.conf"
rm -f "/tmp/${mark}.renamed.conf"
echo "ignored" >"${ignored_dir}/${mark}.ignored"
cat "${ignored_dir}/${mark}.ignored" >/dev/null
rm -f "${ignored_dir}/${mark}.ignored"
id >/dev/null
getent hosts one.one.one.one >/dev/null 2>&1 || true
sleep 3

sudo "${ltm}" --pidfile "${pid_file}" stop
sleep 1

# Everything below reads the root-owned DB, so run as root too.
sudo "${ltm}" --db "${db}" --pidfile "${pid_file}" status

count="$(sudo "${ltm}" --db "${db}" query sql "SELECT count(*) FROM events" | tail -1)"
echo "captured ${count} events"
if [ "${count}" -le 0 ]; then
  echo "no events captured" >&2
  exit 1
fi

sudo "${ltm}" --db "${db}" query sql "SELECT category, count(*) n FROM events GROUP BY category ORDER BY n DESC"
ignored_count="$(sudo "${ltm}" --db "${db}" query sql "SELECT count(*) FROM events WHERE path LIKE '${ignored_dir}/%' OR old_path LIKE '${ignored_dir}/%'" | tail -1)"
if [ "${ignored_count}" -ne 0 ]; then
  echo "ignore-path forwarding failed: captured ${ignored_count} events under ${ignored_dir}" >&2
  exit 1
fi

timeline_out="$(sudo "${ltm}" --db "${db}" timeline --since 5m --category file --path "/tmp/${mark}%" --limit 50)"
if ! printf '%s\n' "${timeline_out}" | grep -q "${mark}"; then
  echo "timeline missing marker file events for ${mark}" >&2
  echo "${timeline_out}" >&2
  exit 1
fi

diff_out="$(sudo "${ltm}" --db "${db}" diff --from 5m --to now)"
if ! printf '%s\n' "${diff_out}" | grep -q "${mark}"; then
  echo "diff missing marker file ${mark}" >&2
  echo "${diff_out}" >&2
  exit 1
fi

query_out="$(sudo "${ltm}" --db "${db}" query "who modified /tmp/${mark}.renamed.conf?")"
if ! printf '%s\n' "${query_out}" | grep -q "${mark}.renamed.conf"; then
  echo "who-modified query returned no rows for /tmp/${mark}.renamed.conf" >&2
  echo "${query_out}" >&2
  exit 1
fi
if ! printf '%s\n' "${query_out}" | grep -q '^- '; then
  echo "who-modified query had a title but no result rows" >&2
  echo "${query_out}" >&2
  exit 1
fi

# Read-only guard: raw SQL writes must be rejected.
if sudo "${ltm}" --db "${db}" query sql "DELETE FROM events" 2>/dev/null; then
  echo "read-only guard failed: DELETE succeeded" >&2
  exit 1
fi

echo "integration OK"
