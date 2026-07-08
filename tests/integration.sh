#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
bin_dir="${work_dir}/bin"
trap 'rm -rf "${work_dir}"' EXIT

mkdir -p "${bin_dir}"

go build -o "${bin_dir}/ltm" ./cmd/ltm

db_path="${work_dir}/ltm.log"
pid_file="${work_dir}/ltm.pid"
server_log="${work_dir}/server.log"
socket_path="${work_dir}/http.sock"

"${bin_dir}/ltm" --db "${db_path}" --pidfile "${pid_file}" start --mode demo
sleep 2

demo_file="${work_dir}/demo.txt"
echo "hello" > "${demo_file}"
echo "world" >> "${demo_file}"

LTM_HTTP_SOCKET="${socket_path}" go run ./tests/httpserver >"${server_log}" 2>&1 &
server_pid=$!
for _ in $(seq 1 20); do
  if curl -fsS --unix-socket "${socket_path}" http://localhost/ >/dev/null 2>/dev/null; then
    break
  fi
  sleep 1
done
curl -fsS --unix-socket "${socket_path}" http://localhost/ >/dev/null
kill "${server_pid}"
wait "${server_pid}" 2>/dev/null || true

sleep 2

"${bin_dir}/ltm" --db "${db_path}" timeline --since 10m
"${bin_dir}/ltm" --db "${db_path}" diff --from "10m" --to now
"${bin_dir}/ltm" --db "${db_path}" query "what changed before nginx restarted?"

"${bin_dir}/ltm" --pidfile "${pid_file}" stop
