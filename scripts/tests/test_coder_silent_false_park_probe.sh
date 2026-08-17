#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROBE="$REPO_ROOT/scripts/coder_silent_false_park_probe.sh"
TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

mkdir -p "$TMP_ROOT/bin" "$TMP_ROOT/dark-factory/daemon"

# The single-quoted lines are emitted verbatim into fake executables; their
# variables must expand when the fakes run, not while this harness creates them.
# shellcheck disable=SC2016
printf '%s\n' \
    '#!/usr/bin/env bash' \
    'if [[ "$*" == *"merge-base --is-ancestor"* ]]; then' \
    '    [[ "${FAKE_ANCESTRY:-present}" == "present" ]]' \
    '    exit' \
    'fi' \
    'if [[ "$*" == *"rev-parse --short HEAD"* ]]; then' \
    '    echo deadbee' \
    '    exit 0' \
    'fi' \
    'exit 1' > "$TMP_ROOT/bin/git"

# shellcheck disable=SC2016
printf '%s\n' \
    '#!/usr/bin/env bash' \
    'echo "running 2 tests"' \
    'case "${FAKE_CARGO_RESULT:-pass}" in' \
    '  pass)' \
    '    echo "test test_wedge_detection_dispatched_coder_silent_saved_by_transcript_activity ... ok"' \
    '    echo "test test_wedge_detection_dispatched_coder_silent_stale_transcript_still_parks ... ok"' \
    '    ;;' \
    '  ignored)' \
    '    echo "test test_wedge_detection_dispatched_coder_silent_saved_by_transcript_activity ... ignored"' \
    '    echo "test test_wedge_detection_dispatched_coder_silent_stale_transcript_still_parks ... ok"' \
    '    ;;' \
    '  missing)' \
    '    echo "test test_wedge_detection_dispatched_coder_silent ... ok"' \
    '    ;;' \
    '  failure)' \
    '    echo "test test_wedge_detection_dispatched_coder_silent_saved_by_transcript_activity ... FAILED"' \
    '    exit 101' \
    '    ;;' \
    'esac' \
    'echo "test result: ok. 2 passed; 0 failed"' > "$TMP_ROOT/bin/cargo"

chmod +x "$TMP_ROOT/bin/git" "$TMP_ROOT/bin/cargo"

run_probe() {
    local cargo_result="$1"
    local ancestry="${2:-present}"
    local output="$TMP_ROOT/${cargo_result}-${ancestry}.log"

    set +e
    PATH="$TMP_ROOT/bin:$PATH" \
        FAKE_CARGO_RESULT="$cargo_result" \
        FAKE_ANCESTRY="$ancestry" \
        DARK_FACTORY_HOME="$TMP_ROOT/dark-factory" \
        CODER_SILENT_PROBE_LOG="$TMP_ROOT/cargo-${cargo_result}.log" \
        bash "$PROBE" > "$output" 2>&1
    PROBE_EXIT=$?
    set -e
}

run_probe pass
[[ "$PROBE_EXIT" -eq 0 ]] || {
    echo "FAIL: passing required tests should exit 0, got $PROBE_EXIT" >&2
    exit 1
}

run_probe ignored
[[ "$PROBE_EXIT" -eq 1 ]] || {
    echo "FAIL: ignored required test must exit 1, got $PROBE_EXIT" >&2
    exit 1
}

run_probe missing
[[ "$PROBE_EXIT" -eq 1 ]] || {
    echo "FAIL: missing required tests must exit 1, got $PROBE_EXIT" >&2
    exit 1
}

run_probe failure
[[ "$PROBE_EXIT" -eq 1 ]] || {
    echo "FAIL: Cargo failure must exit 1, got $PROBE_EXIT" >&2
    exit 1
}

run_probe pass missing
[[ "$PROBE_EXIT" -eq 2 ]] || {
    echo "FAIL: missing required merge ancestry must exit 2, got $PROBE_EXIT" >&2
    exit 1
}

echo "coder_silent false-park probe regression tests passed"
