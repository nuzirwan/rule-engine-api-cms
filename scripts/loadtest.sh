#!/usr/bin/env bash
# =============================================================================
# loadtest.sh — Load Testing Script for nzr-rules-engine
# =============================================================================
#
# USAGE
#   ./scripts/loadtest.sh [OPTIONS]
#
# OPTIONS
#   --help             Show this help message
#   --scenarios LIST   Comma-separated list of scenarios to run
#                      (default: baseline,normal,spike,sustained)
#                      Available: baseline, normal, spike, sustained
#   --no-check         Skip engine reachability check
#
# ENVIRONMENT VARIABLES
#   ENGINE_URL              Base URL of the engine (default: http://127.0.0.1:8080)
#   ORDER_ID                Order ID for /order/ endpoint (default: ORD-TEST-TRK)
#
#   CONCURRENCY_BASELINE    Concurrent users for baseline (default: 10)
#   CONCURRENCY_NORMAL      Concurrent users for normal load (default: 50)
#   CONCURRENCY_SPIKE       Concurrent users for spike test (default: 200)
#   CONCURRENCY_SUSTAINED   Concurrent users for sustained test (default: 30)
#
#   REQUESTS_BASELINE       Total requests for baseline (default: 1000)
#   REQUESTS_NORMAL         Total requests for normal load (default: 5000)
#   REQUESTS_SPIKE          Total requests for spike test (default: 2000)
#   DURATION_SUSTAINED      Duration in seconds for sustained test (default: 60)
#
# SCENARIOS
#   baseline   10 concurrent, 1000 requests to /readyz — establishes network baseline
#   normal     50 concurrent, 5000 requests to /order/<ORDER_ID> — typical load
#   spike      200 concurrent, 2000 requests — stresses connectors, may trip R10 breakers
#   sustained  30 concurrent for 60s — steady-state endurance, observes long-tail latency
#
# TOOL DETECTION
#   The script auto-detects the best available load tool:
#   1. hey   — preferred; outputs latency percentiles natively
#   2. wrk   — second choice; common on Linux/macOS
#   3. curl  — universal fallback; uses parallel curl workers with awk aggregation
#
# EXAMPLES
#   # Run all scenarios against local engine
#   ./scripts/loadtest.sh
#
#   # Run only spike and sustained scenarios
#   ./scripts/loadtest.sh --scenarios spike,sustained
#
#   # Target a different host
#   ENGINE_URL=http://10.0.0.5:8080 ./scripts/loadtest.sh
#
#   # Quick smoke test with fewer requests
#   REQUESTS_BASELINE=100 REQUESTS_NORMAL=500 ./scripts/loadtest.sh --scenarios baseline,normal
#
# CIRCUIT BREAKER (R10) NOTES
#   The spike scenario (200 concurrent) is designed to stress data-source connections
#   beyond their concurrency limits, potentially triggering per-instance circuit breakers
#   (R10 per docs/hld.md). After the spike, /metrics is checked for breaker state:
#     nzr_breaker_state{...} 0  = closed (normal)
#     nzr_breaker_state{...} 1  = half-open (recovering)
#     nzr_breaker_state{...} 2  = open (tripped)
#   A tripped breaker manifests as HTTP 502/504 responses.
#
# =============================================================================
set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration — all overridable via environment variables
# ---------------------------------------------------------------------------
ENGINE_URL="${ENGINE_URL:-http://127.0.0.1:8080}"
ORDER_ID="${ORDER_ID:-ORD-TEST-TRK}"

CONCURRENCY_BASELINE="${CONCURRENCY_BASELINE:-10}"
CONCURRENCY_NORMAL="${CONCURRENCY_NORMAL:-50}"
CONCURRENCY_SPIKE="${CONCURRENCY_SPIKE:-200}"
CONCURRENCY_SUSTAINED="${CONCURRENCY_SUSTAINED:-30}"

REQUESTS_BASELINE="${REQUESTS_BASELINE:-1000}"
REQUESTS_NORMAL="${REQUESTS_NORMAL:-5000}"
REQUESTS_SPIKE="${REQUESTS_SPIKE:-2000}"
DURATION_SUSTAINED="${DURATION_SUSTAINED:-60}"

SCENARIOS_TO_RUN="${SCENARIOS:-baseline,normal,spike,sustained}"
SKIP_CHECK="${SKIP_CHECK:-false}"

# ---------------------------------------------------------------------------
# Colors for output
# ---------------------------------------------------------------------------
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m' # no color

# ---------------------------------------------------------------------------
# Global results store (parallel arrays)
# ---------------------------------------------------------------------------
RESULT_NAMES=()
RESULT_RPS=()
RESULT_P50=()
RESULT_P95=()
RESULT_P99=()
RESULT_ERRORS=()
RESULT_TOTAL=()

# Temp directory — cleaned on exit
TMPDIR_LOADTEST="$(mktemp -d /tmp/loadtest.XXXXXX)"
trap 'rm -rf "$TMPDIR_LOADTEST"' EXIT

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
log()   { echo -e "${CYAN}[loadtest]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*" >&2; }
err()   { echo -e "${RED}[ERROR]${NC} $*" >&2; }
bold()  { echo -e "${BOLD}$*${NC}"; }

print_usage() {
    sed -n '/^# USAGE/,/^# =====/{/^# =====/d; s/^# \{0,1\}//; p}' "$0"
}

# ---------------------------------------------------------------------------
# Tool detection
# ---------------------------------------------------------------------------
LOAD_TOOL=""

detect_tool() {
    if command -v hey &>/dev/null; then
        LOAD_TOOL="hey"
    elif command -v wrk &>/dev/null; then
        LOAD_TOOL="wrk"
    elif command -v curl &>/dev/null; then
        LOAD_TOOL="curl"
    else
        err "No load testing tool found. Install hey (https://github.com/rakyll/hey) or wrk."
        exit 1
    fi
    log "Using tool: ${BOLD}${LOAD_TOOL}${NC}"
}

# ---------------------------------------------------------------------------
# Engine reachability check
# ---------------------------------------------------------------------------
check_engine() {
    log "Checking engine at ${ENGINE_URL}/livez ..."
    local http_code
    http_code=$(curl -s -o /dev/null -w "%{http_code}" --connect-timeout 5 "${ENGINE_URL}/livez" || true)
    if [[ "$http_code" != "200" ]]; then
        err "Engine not reachable at ${ENGINE_URL} (HTTP ${http_code:-no response})."
        err "Start the engine first: cd engine && go run ./cmd/engine"
        exit 1
    fi
    log "Engine is alive (HTTP 200)."
}

# ---------------------------------------------------------------------------
# hey runner
# ---------------------------------------------------------------------------
run_hey() {
    local name="$1"
    local url="$2"
    local concurrency="$3"
    local requests="$4"     # 0 means duration mode
    local duration="$5"     # seconds; used when requests=0
    local outfile="${TMPDIR_LOADTEST}/${name}.hey.txt"

    local hey_args=(-c "$concurrency" -q 0)
    if [[ "$requests" -gt 0 ]]; then
        hey_args+=(-n "$requests")
    else
        hey_args+=(-z "${duration}s")
    fi
    hey_args+=("$url")

    hey "${hey_args[@]}" 2>&1 | tee "$outfile" || true

    # Parse hey output
    # hey outputs lines like:
    #   Requests/sec: 1234.56
    #   50% in 0.0123 secs
    #   95% in 0.0456 secs
    #   99% in 0.0789 secs
    #   [200] 4950 responses
    #   [502] 50 responses

    local rps p50 p95 p99 errors total
    rps=$(grep -E 'Requests/sec:' "$outfile" | awk '{printf "%.1f", $2}' || echo "N/A")

    p50=$(grep -E '^\s+50%' "$outfile" | awk '{printf "%.1f", $2 * 1000}' || echo "N/A")
    p95=$(grep -E '^\s+95%' "$outfile" | awk '{printf "%.1f", $2 * 1000}' || echo "N/A")
    p99=$(grep -E '^\s+99%' "$outfile" | awk '{printf "%.1f", $2 * 1000}' || echo "N/A")

    total=$(grep -E 'responses' "$outfile" | awk '{sum += $1} END {print sum}' || echo "0")
    errors=$(grep -vE '^\s+\[200\]' "$outfile" | grep -E 'responses' | awk '{sum += $1} END {print sum}' || echo "0")

    store_result "$name" "$rps" "$p50" "$p95" "$p99" "${errors:-0}" "${total:-0}"
}

# ---------------------------------------------------------------------------
# wrk runner
# ---------------------------------------------------------------------------
run_wrk() {
    local name="$1"
    local url="$2"
    local concurrency="$3"
    local requests="$4"     # wrk is duration-based; derive duration from requests/rps estimate
    local duration="$5"
    local outfile="${TMPDIR_LOADTEST}/${name}.wrk.txt"

    # wrk works on duration; estimate if request count provided
    local dur="${duration}"
    if [[ "$requests" -gt 0 ]]; then
        # Estimate: assume ~500 rps baseline; cap at reasonable duration
        dur=$(( requests / 500 + 5 ))
    fi

    local threads=$(( concurrency < 8 ? concurrency : 8 ))
    wrk -t "$threads" -c "$concurrency" -d "${dur}s" --latency "$url" 2>&1 | tee "$outfile" || true

    # Parse wrk output
    # Requests/sec: 1234.56
    # Latency   50%   1.23ms
    # Latency   95%   4.56ms
    # Latency   99%   7.89ms
    local rps p50 p95 p99 errors total
    rps=$(grep 'Requests/sec:' "$outfile" | awk '{printf "%.1f", $2}' || echo "N/A")

    p50=$(grep 'Latency\s*50%' "$outfile" | awk '{print $3}' | sed 's/ms//' || echo "N/A")
    p95=$(grep 'Latency\s*95%' "$outfile" | awk '{print $3}' | sed 's/ms//' || echo "N/A")
    p99=$(grep 'Latency\s*99%' "$outfile" | awk '{print $3}' | sed 's/ms//' || echo "N/A")

    total=$(grep 'requests in' "$outfile" | awk '{print $1}' || echo "0")
    errors=$(grep -E 'Non-2xx|Socket errors' "$outfile" | awk '{sum += $NF} END {print sum}' || echo "0")

    store_result "$name" "$rps" "$p50" "$p95" "$p99" "${errors:-0}" "${total:-0}"
}

# ---------------------------------------------------------------------------
# curl fallback runner — parallel workers via background jobs
# ---------------------------------------------------------------------------
run_curl_scenario() {
    local name="$1"
    local url="$2"
    local concurrency="$3"
    local requests="$4"     # 0 = use duration
    local duration="$5"
    local timings_file="${TMPDIR_LOADTEST}/${name}.timings.txt"
    local status_file="${TMPDIR_LOADTEST}/${name}.status.txt"

    > "$timings_file"
    > "$status_file"

    local start_time
    start_time=$(date +%s%3N)  # milliseconds

    if [[ "$requests" -gt 0 ]]; then
        # Request-count mode — distribute requests across concurrent workers
        local reqs_per_worker=$(( requests / concurrency ))
        local remainder=$(( requests - reqs_per_worker * concurrency ))

        log "  Launching ${concurrency} workers × ~${reqs_per_worker} requests each ..."

        local pids=()
        for (( w=0; w<concurrency; w++ )); do
            local worker_reqs=$reqs_per_worker
            if [[ $w -lt $remainder ]]; then
                worker_reqs=$(( worker_reqs + 1 ))
            fi
            local worker_timings="${TMPDIR_LOADTEST}/${name}.w${w}.timings"
            local worker_status="${TMPDIR_LOADTEST}/${name}.w${w}.status"
            (
                for (( r=0; r<worker_reqs; r++ )); do
                    result=$(curl -s -o /dev/null \
                        -w "%{http_code} %{time_total}" \
                        --connect-timeout 5 \
                        --max-time 30 \
                        "$url" 2>/dev/null || echo "000 30.000")
                    echo "${result% *}" >> "$worker_status"
                    # time in seconds → convert to ms
                    echo "${result##* }" >> "$worker_timings"
                done
            ) &
            pids+=($!)
        done

        # Wait for all workers
        for pid in "${pids[@]}"; do
            wait "$pid" 2>/dev/null || true
        done

    else
        # Duration mode — workers run for $duration seconds
        log "  Launching ${concurrency} workers for ${duration}s ..."

        local deadline=$(( $(date +%s) + duration ))
        local pids=()
        for (( w=0; w<concurrency; w++ )); do
            local worker_timings="${TMPDIR_LOADTEST}/${name}.w${w}.timings"
            local worker_status="${TMPDIR_LOADTEST}/${name}.w${w}.status"
            (
                while [[ $(date +%s) -lt $deadline ]]; do
                    result=$(curl -s -o /dev/null \
                        -w "%{http_code} %{time_total}" \
                        --connect-timeout 5 \
                        --max-time 30 \
                        "$url" 2>/dev/null || echo "000 30.000")
                    echo "${result% *}" >> "$worker_status"
                    echo "${result##* }" >> "$worker_timings"
                done
            ) &
            pids+=($!)
        done

        for pid in "${pids[@]}"; do
            wait "$pid" 2>/dev/null || true
        done
    fi

    local end_time
    end_time=$(date +%s%3N)
    local elapsed_ms=$(( end_time - start_time ))

    # Aggregate timing files
    cat "${TMPDIR_LOADTEST}/${name}".w*.timings 2>/dev/null > "$timings_file" || true
    cat "${TMPDIR_LOADTEST}/${name}".w*.status 2>/dev/null > "$status_file" || true

    # Compute percentiles via awk
    local total errors rps p50 p95 p99
    total=$(wc -l < "$timings_file" 2>/dev/null || echo "0")
    total=$(echo "$total" | tr -d ' ')

    errors=$(grep -cE '^[^2]' "$status_file" 2>/dev/null || echo "0") || errors=0

    if [[ "$total" -eq 0 ]]; then
        warn "No timing data collected for scenario ${name}."
        store_result "$name" "N/A" "N/A" "N/A" "N/A" "0" "0"
        return
    fi

    # Convert seconds → ms and compute percentiles
    read -r p50 p95 p99 < <(
        awk '
        {
            # curl outputs time_total in seconds with 6 decimal places
            val = $1 * 1000
            vals[NR] = val
        }
        END {
            # sort
            n = NR
            for (i = 1; i <= n; i++) {
                for (j = i+1; j <= n; j++) {
                    if (vals[i] > vals[j]) {
                        tmp = vals[i]; vals[i] = vals[j]; vals[j] = tmp
                    }
                }
            }
            p50 = vals[int(n * 0.50)]
            p95 = vals[int(n * 0.95)]
            p99 = vals[int(n * 0.99)]
            printf "%.1f %.1f %.1f\n", p50, p95, p99
        }' "$timings_file" 2>/dev/null || echo "N/A N/A N/A"
    )

    # For large result sets, use a faster sort-based percentile
    if [[ "$total" -gt 500 ]]; then
        read -r p50 p95 p99 < <(
            awk '{printf "%.6f\n", $1 * 1000}' "$timings_file" 2>/dev/null \
            | sort -n \
            | awk -v n="$total" '
            BEGIN { p50i=int(n*0.50); p95i=int(n*0.95); p99i=int(n*0.99); idx=0 }
            {
                idx++
                if (idx == p50i) p50 = $1
                if (idx == p95i) p95 = $1
                if (idx == p99i) p99 = $1
            }
            END { printf "%.1f %.1f %.1f\n", p50, p95, p99 }' \
            2>/dev/null || echo "N/A N/A N/A"
        )
    fi

    rps=$(awk "BEGIN { printf \"%.1f\", ${total} / (${elapsed_ms} / 1000.0) }")

    store_result "$name" "$rps" "$p50" "$p95" "$p99" "$errors" "$total"
}

# ---------------------------------------------------------------------------
# Dispatcher — routes to the right runner based on detected tool
# ---------------------------------------------------------------------------
store_result() {
    local name="$1" rps="$2" p50="$3" p95="$4" p99="$5" errors="$6" total="$7"
    RESULT_NAMES+=("$name")
    RESULT_RPS+=("$rps")
    RESULT_P50+=("$p50")
    RESULT_P95+=("$p95")
    RESULT_P99+=("$p99")
    RESULT_ERRORS+=("$errors")
    RESULT_TOTAL+=("$total")
}

run_scenario() {
    local name="$1"
    local url="$2"
    local concurrency="$3"
    local requests="$4"
    local duration="$5"

    echo ""
    bold "=== SCENARIO: ${name} ==="
    log "  URL:         ${url}"
    log "  Concurrency: ${concurrency}"
    if [[ "$requests" -gt 0 ]]; then
        log "  Requests:    ${requests}"
    else
        log "  Duration:    ${duration}s"
    fi
    log "  Tool:        ${LOAD_TOOL}"
    echo ""

    case "$LOAD_TOOL" in
        hey)  run_hey  "$name" "$url" "$concurrency" "$requests" "$duration" ;;
        wrk)  run_wrk  "$name" "$url" "$concurrency" "$requests" "$duration" ;;
        curl) run_curl_scenario "$name" "$url" "$concurrency" "$requests" "$duration" ;;
    esac

    # Print per-scenario result immediately
    local idx=$(( ${#RESULT_NAMES[@]} - 1 ))
    echo ""
    log "  Results:"
    log "    Requests/sec : ${RESULT_RPS[$idx]}"
    log "    Latency p50  : ${RESULT_P50[$idx]} ms"
    log "    Latency p95  : ${RESULT_P95[$idx]} ms"
    log "    Latency p99  : ${RESULT_P99[$idx]} ms"
    log "    Errors       : ${RESULT_ERRORS[$idx]} / ${RESULT_TOTAL[$idx]}"
    if [[ "${RESULT_TOTAL[$idx]}" -gt 0 ]] 2>/dev/null; then
        local err_pct
        err_pct=$(awk "BEGIN { printf \"%.2f\", (${RESULT_ERRORS[$idx]} / ${RESULT_TOTAL[$idx]}) * 100 }")
        log "    Error rate   : ${err_pct}%"
    fi
}

# ---------------------------------------------------------------------------
# Check breaker state via /metrics after spike
# ---------------------------------------------------------------------------
check_breaker_state() {
    log "Checking circuit breaker state via /metrics ..."
    local metrics
    metrics=$(curl -s --connect-timeout 5 "${ENGINE_URL}/metrics" 2>/dev/null || true)

    if [[ -z "$metrics" ]]; then
        warn "Could not fetch /metrics — skipping breaker state check."
        return
    fi

    local breaker_lines
    breaker_lines=$(echo "$metrics" | grep -E 'nzr_breaker' 2>/dev/null || true)

    if [[ -z "$breaker_lines" ]]; then
        log "  No nzr_breaker metrics found (metric name may differ)."
        # Try common alternative names
        breaker_lines=$(echo "$metrics" | grep -iE 'breaker|circuit' 2>/dev/null || true)
    fi

    if [[ -n "$breaker_lines" ]]; then
        echo ""
        bold "--- Circuit Breaker State (from /metrics) ---"
        echo "$breaker_lines"
        echo ""
        # Highlight any open breakers
        if echo "$breaker_lines" | grep -qE '\s2$|\s2\s'; then
            warn "ALERT: One or more circuit breakers are OPEN (state=2). R10 triggered!"
        elif echo "$breaker_lines" | grep -qE '\s1$|\s1\s'; then
            log "  Note: One or more circuit breakers are HALF-OPEN (state=1). Recovering."
        else
            log "  All breakers appear CLOSED (normal state)."
        fi
    else
        log "  No breaker-related metrics found in /metrics output."
    fi
}

# ---------------------------------------------------------------------------
# Summary table
# ---------------------------------------------------------------------------
print_summary() {
    echo ""
    bold "=========================================="
    bold " LOAD TEST SUMMARY"
    bold "=========================================="
    printf "${BOLD}%-18s %8s %8s %8s %8s %12s${NC}\n" \
        "Scenario" "RPS" "p50(ms)" "p95(ms)" "p99(ms)" "Errors/Total"
    printf "%s\n" "-----------------------------------------------------------------------"

    local count=${#RESULT_NAMES[@]}
    for (( i=0; i<count; i++ )); do
        local err_display="${RESULT_ERRORS[$i]}/${RESULT_TOTAL[$i]}"
        # Color errors red if non-zero
        if [[ "${RESULT_ERRORS[$i]}" != "0" && "${RESULT_ERRORS[$i]}" != "N/A" ]]; then
            printf "%-18s %8s %8s %8s %8s ${RED}%12s${NC}\n" \
                "${RESULT_NAMES[$i]}" \
                "${RESULT_RPS[$i]}" \
                "${RESULT_P50[$i]}" \
                "${RESULT_P95[$i]}" \
                "${RESULT_P99[$i]}" \
                "$err_display"
        else
            printf "%-18s %8s %8s %8s %8s ${GREEN}%12s${NC}\n" \
                "${RESULT_NAMES[$i]}" \
                "${RESULT_RPS[$i]}" \
                "${RESULT_P50[$i]}" \
                "${RESULT_P95[$i]}" \
                "${RESULT_P99[$i]}" \
                "$err_display"
        fi
    done

    echo ""
    log "ENGINE_URL: ${ENGINE_URL}"
    log "Load tool:  ${LOAD_TOOL}"
    log "Completed at: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    echo ""
}

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --help|-h)
                print_usage
                exit 0
                ;;
            --scenarios)
                shift
                SCENARIOS_TO_RUN="$1"
                ;;
            --no-check)
                SKIP_CHECK="true"
                ;;
            *)
                err "Unknown option: $1"
                echo "Use --help for usage."
                exit 1
                ;;
        esac
        shift
    done
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
main() {
    parse_args "$@"

    bold "============================================"
    bold " nzr-rules-engine Load Test"
    bold " $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    bold "============================================"

    detect_tool

    if [[ "$SKIP_CHECK" != "true" ]]; then
        check_engine
    fi

    # Convert comma-separated scenarios to array
    IFS=',' read -ra SCENARIO_LIST <<< "$SCENARIOS_TO_RUN"

    local ran_spike=false

    for scenario in "${SCENARIO_LIST[@]}"; do
        scenario=$(echo "$scenario" | tr -d '[:space:]')
        case "$scenario" in
            baseline)
                run_scenario \
                    "baseline" \
                    "${ENGINE_URL}/readyz" \
                    "${CONCURRENCY_BASELINE}" \
                    "${REQUESTS_BASELINE}" \
                    "0"
                ;;
            normal)
                run_scenario \
                    "normal" \
                    "${ENGINE_URL}/order/${ORDER_ID}" \
                    "${CONCURRENCY_NORMAL}" \
                    "${REQUESTS_NORMAL}" \
                    "0"
                ;;
            spike)
                run_scenario \
                    "spike" \
                    "${ENGINE_URL}/order/${ORDER_ID}" \
                    "${CONCURRENCY_SPIKE}" \
                    "${REQUESTS_SPIKE}" \
                    "0"
                ran_spike=true
                ;;
            sustained)
                run_scenario \
                    "sustained" \
                    "${ENGINE_URL}/order/${ORDER_ID}" \
                    "${CONCURRENCY_SUSTAINED}" \
                    "0" \
                    "${DURATION_SUSTAINED}"
                ;;
            *)
                warn "Unknown scenario '${scenario}' — skipping. Valid: baseline, normal, spike, sustained"
                ;;
        esac
    done

    # After spike, check breaker state if engine is available
    if [[ "$ran_spike" == "true" && "$SKIP_CHECK" != "true" ]]; then
        check_breaker_state
    fi

    print_summary
}

main "$@"
