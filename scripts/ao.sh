#!/usr/bin/env bash
# Agent Orchestrator (AO) — launches parallel Claude Code agents on GitHub issues
# Each agent gets its own git worktree, fixes the issue, and creates a PR.
#
# Usage:
#   ./scripts/ao.sh 39 40 53              # work on specific issues
#   ./scripts/ao.sh --label code-only     # pick unassigned code-only issues
#   ./scripts/ao.sh --dry-run 39 40       # show what would run without launching
#
# Options:
#   --max-concurrent N   max parallel agents (default: 3)
#   --model MODEL        claude model to use (default: sonnet)
#   --budget USD         max spend per agent (default: 5)
#   --label LABEL        auto-pick unassigned issues with this label
#   --limit N            max issues to pick with --label (default: 3)
#   --dry-run            print commands without executing
#   --verbose            show agent output in real-time (sequential mode)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
AO_DIR="$REPO_ROOT/.claude/ao"
LOG_DIR="$AO_DIR/logs"
PID_DIR="$AO_DIR/pids"

MAX_CONCURRENT=3
MODEL="sonnet"
BUDGET=5
LABEL=""
LIMIT=3
DRY_RUN=false
export VERBOSE=false
ISSUES=()

# --- Argument parsing ---
while [[ $# -gt 0 ]]; do
    case "$1" in
        --max-concurrent) MAX_CONCURRENT="$2"; shift 2 ;;
        --model)          MODEL="$2"; shift 2 ;;
        --budget)         BUDGET="$2"; shift 2 ;;
        --label)          LABEL="$2"; shift 2 ;;
        --limit)          LIMIT="$2"; shift 2 ;;
        --dry-run)        DRY_RUN=true; shift ;;
        --verbose)        VERBOSE=true; shift ;;
        -h|--help)
            head -16 "$0" | tail -15
            exit 0
            ;;
        *)
            if [[ "$1" =~ ^[0-9]+$ ]]; then
                ISSUES+=("$1")
            else
                echo "Unknown argument: $1" >&2
                exit 1
            fi
            shift
            ;;
    esac
done

# --- Auto-pick issues by label ---
if [[ -n "$LABEL" && ${#ISSUES[@]} -eq 0 ]]; then
    echo "Fetching unassigned '$LABEL' issues..."
    mapfile -t ISSUES < <(
        gh issue list -R mathaix/openclawmachines \
            --label "$LABEL" \
            --state open \
            --json number,assignees \
            --jq '.[] | select(.assignees | length == 0) | .number' \
        | head -n "$LIMIT"
    )
    if [[ ${#ISSUES[@]} -eq 0 ]]; then
        echo "No unassigned issues found with label '$LABEL'"
        exit 0
    fi
    echo "Selected issues: ${ISSUES[*]}"
fi

if [[ ${#ISSUES[@]} -eq 0 ]]; then
    echo "Usage: $0 [options] ISSUE_NUMBERS..." >&2
    echo "       $0 --label code-only" >&2
    exit 1
fi

# --- Setup directories ---
mkdir -p "$LOG_DIR" "$PID_DIR"

# --- Colors ---
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# --- Helpers ---
log() { echo -e "${BLUE}[AO]${NC} $*"; }
ok()  { echo -e "${GREEN}[AO]${NC} $*"; }
warn() { echo -e "${YELLOW}[AO]${NC} $*"; }
err() { echo -e "${RED}[AO]${NC} $*" >&2; }

wait_for_slot() {
    while true; do
        local running=0
        for pid_file in "$PID_DIR"/*.pid; do
            [[ -f "$pid_file" ]] || continue
            local pid
            pid=$(cat "$pid_file")
            if kill -0 "$pid" 2>/dev/null; then
                running=$((running + 1))
            fi
        done
        if [[ $running -lt $MAX_CONCURRENT ]]; then
            break
        fi
        sleep 2
    done
}

build_prompt() {
    local number="$1"
    local title="$2"
    local body="$3"
    local labels="$4"

    cat <<PROMPT
You are an autonomous agent working on GitHub issue #${number}.

## Issue
**#${number}: ${title}**
Labels: ${labels}

${body}

## Instructions

1. Read the issue carefully. Understand what needs to change.
2. Find the relevant code. Read it before making changes.
3. Make the fix. Keep changes minimal and focused.
4. Run tests to verify nothing is broken:
   - If you changed Go code: \`make test-go\`
   - If you changed frontend code: \`make test-frontend\` and \`make typecheck\`
   - If you changed worker code: \`make test-worker\`
5. Commit your changes with a descriptive message referencing the issue (e.g., "fix: description (closes #${number})").
6. Push your branch and create a PR using:
   \`\`\`
   gh pr create --title "fix: short description (closes #${number})" --body "\$(cat <<'EOF'
   ## Summary
   - What was wrong
   - What this PR does

   Closes #${number}

   ## Test plan
   - [ ] Describe how to verify

   🤖 Generated with [Claude Code](https://claude.com/claude-code)
   EOF
   )"
   \`\`\`

## Rules
- Do NOT over-engineer. Fix only what the issue describes.
- Do NOT refactor surrounding code.
- Do NOT add comments, docstrings, or type annotations to code you didn't change.
- If you're unsure about something, err on the side of a smaller change.
- Make sure the PR title starts with "fix:" for bugs or "feat:" for enhancements.
PROMPT
}

# --- Main loop ---
log "Starting Agent Orchestrator"
log "Issues: ${ISSUES[*]}"
log "Concurrency: $MAX_CONCURRENT | Model: $MODEL | Budget: \$${BUDGET}/agent"
echo ""

PIDS=()
ISSUE_MAP=()

for issue_num in "${ISSUES[@]}"; do
    # Fetch issue details
    issue_json=$(gh issue view "$issue_num" -R mathaix/openclawmachines --json title,body,labels 2>/dev/null || true)
    if [[ -z "$issue_json" ]]; then
        err "Failed to fetch issue #$issue_num — skipping"
        continue
    fi

    title=$(echo "$issue_json" | jq -r '.title')
    body=$(echo "$issue_json" | jq -r '.body // "No description provided."')
    labels=$(echo "$issue_json" | jq -r '[.labels[].name] | join(", ")')

    # Generate branch name from issue title
    branch_name="ao/issue-${issue_num}"

    log "Issue #${issue_num}: ${title}"
    log "  Branch: ${branch_name}"
    log "  Labels: ${labels}"

    prompt=$(build_prompt "$issue_num" "$title" "$body" "$labels")

    if $DRY_RUN; then
        echo ""
        warn "[DRY RUN] Would launch:"
        echo "  claude -p --worktree '$branch_name' --model '$MODEL' --permission-mode auto --max-budget-usd $BUDGET --no-chrome"
        echo ""
        continue
    fi

    # Wait for a concurrency slot
    wait_for_slot

    log "Launching agent for #${issue_num}..."

    log_file="$LOG_DIR/issue-${issue_num}-$(date +%Y%m%d-%H%M%S).log"

    # Launch claude in background
    claude -p "$prompt" \
        --worktree "$branch_name" \
        --model "$MODEL" \
        --permission-mode auto \
        --max-budget-usd "$BUDGET" \
        --no-chrome \
        > "$log_file" 2>&1 &

    agent_pid=$!
    echo "$agent_pid" > "$PID_DIR/issue-${issue_num}.pid"

    PIDS+=("$agent_pid")
    ISSUE_MAP+=("${issue_num}:${agent_pid}:${log_file}")

    ok "Agent for #${issue_num} running (PID $agent_pid, log: $log_file)"
    echo ""
done

if $DRY_RUN; then
    log "Dry run complete."
    exit 0
fi

# --- Wait for all agents ---
log "Waiting for ${#PIDS[@]} agent(s) to complete..."
echo ""

FAILED=()
SUCCEEDED=()

for entry in "${ISSUE_MAP[@]}"; do
    IFS=: read -r num pid logfile <<< "$entry"

    if wait "$pid" 2>/dev/null; then
        ok "#${num} completed successfully (log: $logfile)"
        SUCCEEDED+=("$num")
    else
        err "#${num} failed (exit code $?, log: $logfile)"
        FAILED+=("$num")
    fi

    # Clean up PID file
    rm -f "$PID_DIR/issue-${num}.pid"
done

# --- Summary ---
echo ""
log "========================================="
log "Agent Orchestrator Summary"
log "========================================="
ok  "Succeeded: ${#SUCCEEDED[@]} — ${SUCCEEDED[*]:-none}"
if [[ ${#FAILED[@]} -gt 0 ]]; then
    err "Failed:    ${#FAILED[@]} — ${FAILED[*]}"
fi
echo ""
log "Logs: $LOG_DIR/"
log "Check PRs: gh pr list -R mathaix/openclawmachines --author @me"
