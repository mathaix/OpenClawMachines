Archive the current feature and start a new branch.

## Instructions

1. **Ask for the new branch name:**
   - Ask the user: "What should the new branch be named?"
   - Wait for their response. Store it as `BRANCH_NAME`.

2. **Switch to main and pull latest:**
   ```bash
   git fetch origin && git checkout main && git pull origin main
   ```

3. **Find the last merged PR:**
   ```bash
   gh pr list --state merged --limit 1 --json number,title
   ```
   - Extract the PR number as `PR_NUM` and title as `PR_TITLE`.
   - If no merged PRs found, ask the user for a PR number and title to use.

4. **Archive CurrentFeature.md:**
   ```bash
   git mv docs/CurrentFeature.md docs/Feature_<PR_NUM>.md
   ```
   - If `docs/CurrentFeature.md` doesn't exist, skip this step and note it.

5. **Update RELEASE.md:**
   - If `docs/RELEASE.md` doesn't exist, create it with this content:
     ```
     # Release Notes

     ## Features
     ```
   - Insert a new entry as the **first item** under `## Features`:
     ```
     - **PR #<PR_NUM>**: <PR_TITLE> ([Feature_<PR_NUM>.md](Feature_<PR_NUM>.md))
     ```
   - Keep all existing entries below the new one.

6. **Create fresh CurrentFeature.md:**
   - Write `docs/CurrentFeature.md` with just:
     ```
     # Current Feature: <BRANCH_NAME>
     ```

7. **Create the new branch from main (do NOT commit on main):**
   ```bash
   git checkout -b <BRANCH_NAME>
   ```

8. **Commit the archive on the new branch:**
   ```bash
   git add -A && git commit -m "docs: Archive Feature_<PR_NUM> and start <BRANCH_NAME>"
   ```

9. **Push the branch:**
   ```bash
   git push -u origin <BRANCH_NAME>
   ```

10. **Report to the user:**
    - Archived file: `docs/Feature_<PR_NUM>.md`
    - RELEASE.md entry added
    - New branch: `<BRANCH_NAME>` (checked out and pushed)
