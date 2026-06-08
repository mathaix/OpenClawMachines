Commit all changes, push to remote, and create a pull request.

## Instructions

1. First, gather information in parallel:
   - Run `git status` to see untracked and modified files
   - Run `git diff` to see staged and unstaged changes
   - Run `git log -5 --oneline` to see recent commit message style
   - Run `git branch --show-current` to get current branch name
   - Check if branch tracks remote: `git rev-parse --abbrev-ref @{upstream} 2>/dev/null`

2. Analyze all changes and draft a commit message:
   - Summarize the nature of changes (feature, fix, refactor, etc.)
   - Write a concise commit message focusing on "why" not "what"
   - Do NOT commit binaries, .db files, or secrets (.env, credentials)

3. Stage and commit:
   - Add relevant files with `git add`
   - Create commit with message ending with the Claude Code signature:
   ```
   🤖 Generated with [Claude Code](https://claude.com/claude-code)

   Co-Authored-By: Claude <noreply@anthropic.com>
   ```

4. Push to remote:
   - Push with `git push origin <branch-name>`
   - If branch doesn't exist on remote, use `git push -u origin <branch-name>`

5. Create the PR:
   - Use `gh pr create` with a descriptive title and body
   - Include a Summary section with 1-3 bullet points
   - Include a Test plan section with checkboxes
   - End with the Claude Code signature

6. Return the PR URL to the user.

## Example PR format

```bash
gh pr create --title "feat: description of change" --body "$(cat <<'EOF'
## Summary
- First key change
- Second key change

## Test plan
- [ ] Test item 1
- [ ] Test item 2

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```
