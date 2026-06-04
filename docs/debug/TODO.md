# SSH Cert Auth — Remaining Work

## Not yet implemented (from fix plan)

- [ ] **sshd logging**: Add `-E /var/log/sshd.log` flag to sshd start in init script — captures auth errors without needing a syslog daemon
- [ ] **cf-ssh-check diagnostic logging**: Log each invocation (uid, curl result, principals) to `/var/log/cf-ssh-check.log` — distinguishes "sshd never ran the script" from "script ran but failed"
- [ ] **Disable password fallback in CLI**: Add `BatchMode=yes` and `PasswordAuthentication=no` to default SSH args in `machines_ssh.go` — cert auth either works or fails cleanly
- [ ] **Dockerfile hardening**: Replace `chown -R openclaw:openclaw /usr/local/bin` with a user-local npm prefix so `/usr/local/bin` stays root-owned

## Future improvements

- [ ] **VM-side checks in ssh-debug**: Use agent exec API to verify path ownership, test cf-ssh-check as nobody, check sshd -T from the CLI
- [ ] **GitHub login principal support**: Metadata server `/v1/ssh-principals` should return identity-provider-specific identifiers (e.g., GitHub username), not just email-derived principals
- [ ] **Inject authorized_keys from metadata**: Store user's SSH public key in profile, serve via `/v1/ssh-pubkeys`, init script writes to `~/.ssh/authorized_keys` — works independently of cert auth
