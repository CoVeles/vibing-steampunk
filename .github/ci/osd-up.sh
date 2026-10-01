#!/usr/bin/env bash
# Download a pinned open-steamgate release binary, start it on throwaway
# state, and wait until it serves. Used by .github/workflows/osd-integration.yml
# and runnable locally:
#
#   .github/ci/osd-up.sh <workdir> [tag]      # tag defaults to .github/ci/$OSD_BINARY.version
#
# OSD_BINARY picks the target by binary name and nothing else: osd (default,
# the Bun build, ADT facade included) or osgo (the Go build, from release 0.5;
# no ADT until 0.6). Both take the same environment: STG_PORT, STG_DB_PATH,
# XDG_DATA_HOME, STG_ADT_SID.
#
# Prints KEY=VALUE lines for the caller on success (SAP_URL, OSD_PID, OSD_TAG,
# OSD_WORKDIR, and OSD_ADT: the HTTP status of HEAD /sap/bc/adt/core/discovery,
# 200 when the target speaks ADT); also appends them to $GITHUB_ENV when set.
# Exit codes: 0 ready, 3 asset unavailable (caller skips), anything else is a
# real failure.
#
# Isolation: the release binary unpacks its seed system into
# $XDG_DATA_HOME/open-steamgate and writes ADT source changes there as files,
# and keeps rows in STG_DB_PATH. A fresh STG_DB_PATH alone does not reset it.
# Every run gets its own XDG_DATA_HOME, HOME, database and process, so a run
# never sees (or touches) the developer's ~/.local/share/open-steamgate.
set -euo pipefail

work=${1:?usage: osd-up.sh <workdir> [tag]}
here=$(cd "$(dirname "$0")" && pwd)
binary=${OSD_BINARY:-osd}
tag=${2:-$(tr -d '[:space:]' < "$here/$binary.version" 2>/dev/null || true)}
if [ -z "$tag" ]; then
  echo "osd-up: no $binary tag pinned in .github/ci/$binary.version" >&2
  exit 3
fi
repo=${OSD_REPO:-oisee/open-steamgate}
port=${STG_PORT:-3030}
timeout_s=${OSD_READY_TIMEOUT:-600}

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64)  asset=$binary-linux-x64 ;;
  Linux/aarch64) asset=$binary-linux-arm64 ;;
  Darwin/arm64)  asset=$binary-darwin-arm64 ;;
  *) echo "osd-up: no OSD asset for $(uname -s)/$(uname -m)" >&2; exit 3 ;;
esac

mkdir -p "$work"/{dl,xdg,home,db,cwd}
work=$(cd "$work" && pwd)

if ! gh release download "$tag" -R "$repo" -p "$asset" -p "$asset.sha256" -D "$work/dl" --clobber; then
  echo "osd-up: $repo $tag has no downloadable $asset" >&2
  exit 3
fi
(cd "$work/dl" && sha256sum -c "$asset.sha256") >&2
bin="$work/$binary-vsp-ci"
cp "$work/dl/$asset" "$bin"
chmod +x "$bin"

# Never the developer's home: XDG_DATA_HOME and HOME both point into $work.
export XDG_DATA_HOME="$work/xdg" HOME="$work/home" STG_DB_PATH="$work/db/osd.sqlite" STG_PORT="$port"
if [ "$binary" = osd ]; then
  (cd "$work/cwd" && "$bin" doctor) > "$work/doctor.log" 2>&1 || true
fi
# osd starts with `up`; osgo takes no subcommand (its port, database and home
# come from the same environment).
start=(up); [ "$binary" = osd ] || start=()
(cd "$work/cwd" && exec "$bin" "${start[@]}") > "$work/osd.log" 2>&1 &
pid=$!

url="http://localhost:$port"
# OSD is ready when its build stamp answers and the generation the source
# built is the one being served (system.serving == system.live). The stamp is
# OSD's own endpoint, not ADT's: a real system answers 404 there.
# OSGo's readiness route is not documented yet (it is not the build stamp);
# until it is, any HTTP answer on / counts.
ready() {
  if [ "$binary" = osd ]; then
    body=$(curl -fsS -m 5 "$url/sap/bc/adt/core/http/build" 2>/dev/null) &&
      jq -e '.system.serving != null and .system.serving == .system.live' <<<"$body" >/dev/null
  else
    body=$(curl -sS -m 5 -o /dev/null -w '{"status":%{http_code}}' "$url/" 2>/dev/null) &&
      [ "$(jq -r .status <<<"$body")" != 000 ]
  fi
}
deadline=$((SECONDS + timeout_s))
until ready; do
  if ! kill -0 "$pid" 2>/dev/null; then
    echo "osd-up: OSD exited before it was ready" >&2
    tail -40 "$work/osd.log" >&2
    exit 1
  fi
  if (( SECONDS > deadline )); then
    echo "osd-up: not ready after ${timeout_s}s" >&2
    tail -40 "$work/osd.log" >&2
    kill "$pid" 2>/dev/null || true
    exit 1
  fi
  sleep 3
done
echo "$body" > "$work/build.json"
# Does it speak ADT? This is the switch between the full suite and a smoke run.
adt=$(curl -s -o /dev/null -I -m 10 -w '%{http_code}' "$url/sap/bc/adt/core/discovery" || true)
echo "osd-up: $binary $tag ready on $url after ${SECONDS}s; HEAD core/discovery: $adt" >&2

out=$(printf 'SAP_URL=%s\nOSD_PID=%s\nOSD_TAG=%s\nOSD_WORKDIR=%s\nOSD_ADT=%s\n' "$url" "$pid" "$binary-$tag" "$work" "$adt")
echo "$out"
if [ -n "${GITHUB_ENV:-}" ]; then
  echo "$out" >> "$GITHUB_ENV"
fi
