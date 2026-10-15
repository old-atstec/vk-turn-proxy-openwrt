#!/usr/bin/env bash
# Run EVERY test suite in this repository.
#
#   ./tools/test.sh
#
# 🚨 WHY THIS FILE EXISTS. The suites live in two places that no single command
# reaches:
#
#   1. pkg/... and the rest of the root module   — `go test ./...`
#   2. third_party/speedtest-go                  — a SEPARATE module behind a
#      `replace`, so the root `./...` does not see it. Its fork guards had never
#      been run by anything except a human typing the command.
#
# Recording three commands in three files is the same defect as a harness that is
# green, sabotage-validated and not in the repo: a recorded way to run something
# that does not run. One entry point, and it is the one release.sh calls.
set -uo pipefail
cd "$(dirname "$0")/.."

RACE=${RACE:-1}
fail=0
run() {
    echo
    echo "══ $1"
    shift
    if "$@"; then
        echo "   PASS"
    else
        echo "   FAIL"
        fail=1
    fi
}

# An explicit ceiling per test binary: the suites run on loopback stands (no
# internet), and a stand that hangs must fail the gate, not hold it. The
# slowest package (pkg/proxy with VKTP_SLOW) takes ~3 min under -race.
goflags=(-count=1 -timeout=10m)

# timeout(1) where there is one (Linux, coreutils on macOS as gtimeout).
with_timeout() {
    local t=$1; shift
    if command -v timeout >/dev/null; then timeout "$t" "$@"
    elif command -v gtimeout >/dev/null; then gtimeout "$t" "$@"
    else "$@"
    fi
}
[ "$RACE" = "1" ] && goflags+=(-race)

# 🚨 THE GATE RUNS THE SLOW TESTS TOO. A test that waits for a library's own
# timer (pion refreshes a permission every 120 s) skips itself unless VKTP_SLOW is
# set, so that a bare `go test` stays quick -- and a test that only a human or a
# sabotage runner ever switches on is a test the gate cannot see. It costs this
# script about two minutes. VKTP_SLOW= ./tools/test.sh leaves them out.
export VKTP_SLOW=${VKTP_SLOW-1}

# 🚨 MAKE THE MACHINE READY, DO NOT ASSUME IT IS. The fork's
# TestForkDivergesFromUpstreamExactlyHere diffs the vendored tree against
# PRISTINE upstream, which it reads from the module cache. On a clean checkout
# that cache is cold and the test t.Fatals -- correctly, because a guard that
# SKIPS itself when its input is missing is green on every machine nobody set
# up. Fetching it here is what makes this script self-sufficient.
#
# The `@version` form fetches real upstream despite the `replace` in go.mod that
# points the module at ./third_party/speedtest-go -- which is exactly the point:
# the guard needs the original to compare against. Checksum-verified against the
# public sumdb (this repo's go.sum has no showwin entry, again because of the
# replace).
UPSTREAM=github.com/showwin/speedtest-go@v1.7.11
echo "== fetching pristine $UPSTREAM for the fork's upstream-diff guard"
if with_timeout 300 go mod download "$UPSTREAM"; then
    echo "   ready"
else
    echo "   FAIL: could not fetch $UPSTREAM -- the fork's divergence guard cannot run" >&2
    fail=1
fi

run "root module — go test ${goflags[*]} ./..." \
    go test "${goflags[@]}" ./...

# 🚨 The fork's guards include TestForkDivergesFromUpstreamExactlyHere, which
# needs pristine upstream in the module cache. It FATALS rather than skipping if
# it is cold — a guard that skips itself is a guard that is green everywhere it
# has not been set up.
run "vendored fork — separate module, not reached by the root ./..." \
    env -C third_party/speedtest-go go test "${goflags[@]}" ./speedtest/

# The OpenWrt daemon ships as one static binary per router architecture
# (openwrt/: the packages build it through golang-package.mk). A build or vet
# failure on one of them would first show in the package build: the gate builds
# and vets every target here; its tests ran with the root module above.
daemon_targets() {
    local ok=0 t
    for t in linux/arm64 linux/amd64 linux/arm/7 linux/mipsle/softfloat; do
        set -- $(echo "$t" | tr / ' ')
        local env=(CGO_ENABLED=0 GOOS=$1 GOARCH=$2)
        case $2 in
            arm) env+=(GOARM=$3) ;;
            mipsle) env+=(GOMIPS=$3) ;;
        esac
        env "${env[@]}" go build -trimpath -o /dev/null ./cmd/vk-turn-proxyd \
            || { echo "   build $t failed"; ok=1; }
        env "${env[@]}" go vet ./cmd/vk-turn-proxyd \
            || { echo "   vet $t failed"; ok=1; }
    done
    return $ok
}
run "vk-turn-proxyd — built and vetted for the router targets" \
    daemon_targets

# The packaging scripts: build.sh carries its container script in single
# quotes, and an apostrophe in a comment there broke the build twice.
shell_scripts() {
    local ok=0 f
    for f in openwrt/build.sh openwrt/package-native.sh tools/test.sh; do
        bash -n "$f" || { echo "   $f does not parse"; ok=1; }
    done
    for f in openwrt/vk-turn-proxy/files/vkturn.sh openwrt/vk-turn-proxy/files/vkturn-notify.sh; do
        sh -n "$f" || { echo "   $f does not parse"; ok=1; }
    done
    return $ok
}
run "packaging and netifd scripts parse" \
    shell_scripts

echo
if [ "$fail" = 0 ]; then
    echo "all suites passed"
    exit 0
fi
echo "SUITES FAILED"
exit 1
