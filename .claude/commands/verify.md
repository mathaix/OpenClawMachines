Run full quality checks: build, lint, test, and typecheck.

Use this before committing or deploying to catch issues early.

## Run All Checks

Run these in sequence — stop at first failure:

1. **Build + static analysis:**
   ```bash
   make check
   ```
   This runs: Go vet, Go lint, vulnerability check, shellcheck.

2. **Go tests:**
   ```bash
   make test-go
   ```

3. **Frontend typecheck:**
   ```bash
   make typecheck
   ```

## Report Results

- Report pass/fail for each step
- If any step fails, show the error and stop — do not continue to the next step
- If all pass, report "All checks passed"
