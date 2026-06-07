# Fresh Clone

When the user needs a clean clone of the repository (e.g., after unresolvable conflicts or corrupted state):

1. Record the current branch name and remote URL:
   ```bash
   git branch --show-current
   git remote get-url origin
   ```

2. Check for any uncommitted changes and warn the user if there are any:
   ```bash
   git status --short
   ```

3. Move to the parent directory and rename the existing folder as a backup:
   ```bash
   mv <repo-dir> <repo-dir>-backup-$(date +%s)
   ```

4. Clone fresh from the remote:
   ```bash
   git clone <remote-url> <repo-dir>
   ```

5. If the original branch exists on the remote, check it out:
   ```bash
   git checkout <original-branch>
   ```

6. Confirm success:
   ```bash
   git log --oneline -5
   git status
   ```

7. Report the backup location and confirm the new clone is ready.

IMPORTANT: Always warn the user about uncommitted changes before proceeding. The backup directory preserves their previous state.
