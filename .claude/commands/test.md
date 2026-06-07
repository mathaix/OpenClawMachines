Run OpenClaw Machines test suite.

## All Tests

1. **Run all tests (Go + frontend):**
   ```bash
   make test
   ```

## Individual Test Suites

If the user wants specific tests:

2. **Go unit tests:**
   ```bash
   make test-go
   ```

3. **Go unit tests only (skip integration):**
   ```bash
   make test-unit
   ```

4. **Frontend tests (Vitest):**
   ```bash
   make test-frontend
   ```

5. **TypeScript type checking:**
   ```bash
   make typecheck
   ```

## Test Results

6. **Report pass/fail status** and any failures.

## Notes

- Go tests run locally, no GCP resources needed
- Frontend tests use Vitest with React Testing Library
- Always run `make test` before creating a PR
