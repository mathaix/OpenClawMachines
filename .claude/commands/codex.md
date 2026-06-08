Delegate a task to OpenAI Codex CLI for review or execution.

## Arguments

- `$ARGUMENTS` — the prompt or instructions to send to Codex. If empty, defaults to reviewing the current branch changes.

## Instructions

### Review mode (default when no arguments or arguments mention "review")

If the user provides no arguments, or the arguments are about reviewing something:

```bash
codex review --base main --title "Review from Claude Code"
```

If the user specifies a custom review prompt:

```bash
codex review --base main "CUSTOM_PROMPT_HERE"
```

If reviewing uncommitted changes specifically:

```bash
codex review --uncommitted
```

### Exec mode (when arguments describe a task to perform)

If the user provides a task prompt (not a review), use exec mode:

```bash
codex exec --sandbox read-only "PROMPT_HERE"
```

Use `read-only` sandbox by default for safety. Only use `workspace-write` if the user explicitly asks Codex to make changes.

### Choosing the mode

- No arguments → `codex review --uncommitted` (review current changes)
- Arguments mention a file path (e.g., `docs/plans/foo.md`) → `codex exec --sandbox read-only "Review the design document at [path]. Check for technical feasibility, gaps, and inconsistencies. Categorize findings as CRITICAL, IMPORTANT, or MINOR."`
- Arguments are a general task → `codex exec --sandbox read-only "ARGUMENTS"`

## Running

Run the codex command via the Bash tool. The command may take a few minutes. Use a 600000ms (10 minute) timeout.

Report the full output back to the user.
