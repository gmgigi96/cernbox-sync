#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
COVERAGE_DIR="$ROOT/.coverage"

mkdir -p "$COVERAGE_DIR"

GO_UNIT_OUT="$COVERAGE_DIR/unit.out"
GO_INT_OUT="$COVERAGE_DIR/integration.out"
GO_MERGED_OUT="$COVERAGE_DIR/merged.out"
VITEST_DIR="$COVERAGE_DIR/gui"

pass() { echo -e "\033[32m✓\033[0m $*"; }
fail() { echo -e "\033[31m✗\033[0m $*"; }
header() { echo -e "\n\033[1;34m==> $*\033[0m"; }

GO_UNIT_OK=0
GO_INT_OK=0
GUI_OK=0

# --- Go unit tests ---
header "Go unit tests"
if go test ./... -coverprofile="$GO_UNIT_OUT" 2>&1; then
    pass "Go unit tests passed"
    GO_UNIT_OK=1
else
    fail "Go unit tests failed"
fi

# --- Go integration tests ---
header "Go integration tests"
if go test -tags integration -timeout 120s ./integration/ -coverprofile="$GO_INT_OUT" 2>&1; then
    pass "Go integration tests passed"
    GO_INT_OK=1
else
    fail "Go integration tests failed (is the dev environment running? make dev-up)"
fi

# --- GUI unit tests ---
header "GUI unit tests (Vitest)"
if cd "$ROOT/gui" && npm install --silent && npx vitest run --coverage --coverage.reporter=text --coverage.reporter=html --coverage.reporter=json-summary 2>&1; then
    pass "GUI unit tests passed"
    GUI_OK=1
else
    fail "GUI unit tests failed"
fi
cd "$ROOT"

# --- Merge Go coverage profiles ---
header "Coverage summary"

if [[ $GO_UNIT_OK -eq 1 || $GO_INT_OK -eq 1 ]]; then
    # Merge coverprofiles: keep the mode header once, append remaining lines
    echo "mode: set" > "$GO_MERGED_OUT"
    [[ $GO_UNIT_OK -eq 1 ]] && grep -v "^mode:" "$GO_UNIT_OUT" >> "$GO_MERGED_OUT" || true
    [[ $GO_INT_OK -eq 1 ]]  && grep -v "^mode:" "$GO_INT_OUT"  >> "$GO_MERGED_OUT" || true

    echo ""
    echo "Go unit coverage:"
    [[ $GO_UNIT_OK -eq 1 ]] && go tool cover -func="$GO_UNIT_OUT" | tail -1 || echo "  (skipped)"

    echo ""
    echo "Go integration coverage:"
    [[ $GO_INT_OK -eq 1 ]] && go tool cover -func="$GO_INT_OUT" | tail -1 || echo "  (skipped)"

    echo ""
    echo "Go overall (merged):"
    go tool cover -func="$GO_MERGED_OUT" | tail -1

    echo ""
    echo "HTML report: go tool cover -html=$GO_MERGED_OUT"
fi

if [[ $GUI_OK -eq 1 ]]; then
    echo ""
    echo "GUI coverage:"
    SUMMARY_JSON="$ROOT/gui/coverage/coverage-summary.json"
    if [[ -f "$SUMMARY_JSON" ]]; then
        jq -r '"  lines: \(.total.lines.pct)%  |  statements: \(.total.statements.pct)%  |  functions: \(.total.functions.pct)%  |  branches: \(.total.branches.pct)%"' "$SUMMARY_JSON"
    fi
    echo "  HTML report: $ROOT/gui/coverage/index.html"
fi

# --- Exit code ---
echo ""
FAILURES=0
[[ $GO_UNIT_OK -eq 0 ]] && { fail "Go unit tests failed"; FAILURES=$((FAILURES+1)); }
[[ $GO_INT_OK -eq 0 ]]  && { fail "Go integration tests failed"; FAILURES=$((FAILURES+1)); }
[[ $GUI_OK -eq 0 ]]     && { fail "GUI unit tests failed"; FAILURES=$((FAILURES+1)); }

[[ $FAILURES -eq 0 ]] && pass "All tests passed" || exit 1
