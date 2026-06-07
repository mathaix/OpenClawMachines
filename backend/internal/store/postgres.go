package store

import (
	"context"
	cryptoRand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return &PostgresStore{pool: pool}, nil
}

func (s *PostgresStore) Close() {
	s.pool.Close()
}

func (s *PostgresStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// Pool returns the underlying pgxpool.Pool. Reserved for callers that need
// transaction-scoped pgx behavior the Store interface doesn't expose
// (e.g. advisory locks in maintenance jobs). Most callers should use the
// narrower repo interfaces.
func (s *PostgresStore) Pool() *pgxpool.Pool {
	return s.pool
}

// ---- Users ----

func (s *PostgresStore) CreateUser(ctx context.Context, user *User) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO users (email, name, avatar_url, auth_provider, auth_provider_id, cf_sub, identity_issuer, identity_subject, status)
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8, COALESCE(NULLIF($9, ''), 'active'))
		 RETURNING id, created_at`,
		user.Email, user.Name, user.AvatarURL, user.AuthProvider, user.AuthProviderID,
		user.CfSub, user.IdentityIssuer, user.IdentitySubject, user.Status,
	).Scan(&user.ID, &user.CreatedAt)
}

func (s *PostgresStore) GetUser(ctx context.Context, id int) (*User, error) {
	u := &User{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, name, avatar_url, auth_provider, auth_provider_id, COALESCE(cf_sub, ''), identity_issuer, identity_subject, status, created_at
		 FROM users WHERE id = $1`, id,
	).Scan(&u.ID, &u.Email, &u.Name, &u.AvatarURL, &u.AuthProvider, &u.AuthProviderID, &u.CfSub, &u.IdentityIssuer, &u.IdentitySubject, &u.Status, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *PostgresStore) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	u := &User{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, name, avatar_url, auth_provider, auth_provider_id, COALESCE(cf_sub, ''), identity_issuer, identity_subject, status, created_at
		 FROM users WHERE email = $1`, email,
	).Scan(&u.ID, &u.Email, &u.Name, &u.AvatarURL, &u.AuthProvider, &u.AuthProviderID, &u.CfSub, &u.IdentityIssuer, &u.IdentitySubject, &u.Status, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *PostgresStore) GetUserByProvider(ctx context.Context, provider, providerID string) (*User, error) {
	u := &User{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, name, avatar_url, auth_provider, auth_provider_id, COALESCE(cf_sub, ''), identity_issuer, identity_subject, status, created_at
		 FROM users WHERE auth_provider = $1 AND auth_provider_id = $2`, provider, providerID,
	).Scan(&u.ID, &u.Email, &u.Name, &u.AvatarURL, &u.AuthProvider, &u.AuthProviderID, &u.CfSub, &u.IdentityIssuer, &u.IdentitySubject, &u.Status, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *PostgresStore) GetUserByCfSub(ctx context.Context, cfSub string) (*User, error) {
	u := &User{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, name, avatar_url, auth_provider, auth_provider_id, COALESCE(cf_sub, ''), identity_issuer, identity_subject, status, created_at
		 FROM users WHERE cf_sub = $1`, cfSub,
	).Scan(&u.ID, &u.Email, &u.Name, &u.AvatarURL, &u.AuthProvider, &u.AuthProviderID, &u.CfSub, &u.IdentityIssuer, &u.IdentitySubject, &u.Status, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *PostgresStore) GetUserByIdentity(ctx context.Context, issuer, subject string) (*User, error) {
	u := &User{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, name, avatar_url, auth_provider, auth_provider_id, COALESCE(cf_sub, ''), identity_issuer, identity_subject, status, created_at
		 FROM users WHERE identity_issuer = $1 AND identity_subject = $2`, issuer, subject,
	).Scan(&u.ID, &u.Email, &u.Name, &u.AvatarURL, &u.AuthProvider, &u.AuthProviderID, &u.CfSub, &u.IdentityIssuer, &u.IdentitySubject, &u.Status, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *PostgresStore) UpdateUserCfSub(ctx context.Context, userID int, cfSub string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET cf_sub = $2 WHERE id = $1`,
		userID, cfSub)
	return err
}

func (s *PostgresStore) UpdateUserProvider(ctx context.Context, userID int, provider, providerID string, avatarURL *string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET auth_provider = $2, auth_provider_id = $3, avatar_url = COALESCE($4, avatar_url)
		 WHERE id = $1`,
		userID, provider, providerID, avatarURL)
	return err
}

// ---- Accounts ----

func (s *PostgresStore) CreateAccount(ctx context.Context, a *Account) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO accounts (name, slug, created_by)
		 VALUES ($1, $2, $3)
		 RETURNING id, created_at`,
		a.Name, a.Slug, a.CreatedBy,
	).Scan(&a.ID, &a.CreatedAt)
}

func (s *PostgresStore) CreateAccountWithOwner(ctx context.Context, a *Account, ownerUserID int) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := tx.QueryRow(ctx,
		`INSERT INTO accounts (name, slug, created_by)
		 VALUES ($1, $2, $3)
		 RETURNING id, created_at`,
		a.Name, a.Slug, a.CreatedBy,
	).Scan(&a.ID, &a.CreatedAt); err != nil {
		return err
	}

	var memberCreatedAt time.Time
	if err := tx.QueryRow(ctx,
		`INSERT INTO account_members (account_id, user_id, role)
		 VALUES ($1, $2, 'owner')
		 RETURNING created_at`,
		a.ID, ownerUserID,
	).Scan(&memberCreatedAt); err != nil {
		return fmt.Errorf("create owner membership: %w", err)
	}

	return tx.Commit(ctx)
}

func (s *PostgresStore) UpdateAccountName(ctx context.Context, accountID int, name string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE accounts SET name = $1 WHERE id = $2`, name, accountID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("account not found")
	}
	return nil
}

func (s *PostgresStore) GetAccount(ctx context.Context, id int) (*Account, error) {
	a := &Account{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, slug, created_by, created_at
		 FROM accounts WHERE id = $1`, id,
	).Scan(&a.ID, &a.Name, &a.Slug, &a.CreatedBy, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (s *PostgresStore) GetAccountBySlug(ctx context.Context, slug string) (*Account, error) {
	a := &Account{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, slug, created_by, created_at
		 FROM accounts WHERE slug = $1`, slug,
	).Scan(&a.ID, &a.Name, &a.Slug, &a.CreatedBy, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (s *PostgresStore) ListAccountsByUser(ctx context.Context, userID int) ([]Account, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT a.id, a.name, a.slug, a.created_by, a.created_at
		 FROM accounts a
		 JOIN account_members am ON am.account_id = a.id
		 WHERE am.user_id = $1
		 ORDER BY a.created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	accounts := []Account{}
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Name, &a.Slug, &a.CreatedBy, &a.CreatedAt); err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, nil
}

// ---- Account Members ----

func (s *PostgresStore) AddAccountMember(ctx context.Context, m *AccountMember) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO account_members (account_id, user_id, role)
		 VALUES ($1, $2, $3)
		 RETURNING created_at`,
		m.AccountID, m.UserID, m.Role,
	).Scan(&m.CreatedAt)
}

func (s *PostgresStore) RemoveAccountMember(ctx context.Context, accountID, userID int) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM account_members WHERE account_id = $1 AND user_id = $2`,
		accountID, userID)
	return err
}

func (s *PostgresStore) ListAccountMembers(ctx context.Context, accountID int) ([]AccountMember, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT am.account_id, am.user_id, am.role, am.created_at, u.email, u.name
		 FROM account_members am
		 JOIN users u ON u.id = am.user_id
		 WHERE am.account_id = $1 ORDER BY am.created_at`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	members := []AccountMember{}
	for rows.Next() {
		var m AccountMember
		if err := rows.Scan(&m.AccountID, &m.UserID, &m.Role, &m.CreatedAt, &m.Email, &m.Name); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, nil
}

func (s *PostgresStore) GetAccountMember(ctx context.Context, accountID, userID int) (*AccountMember, error) {
	m := &AccountMember{}
	err := s.pool.QueryRow(ctx,
		`SELECT account_id, user_id, role, created_at
		 FROM account_members WHERE account_id = $1 AND user_id = $2`,
		accountID, userID,
	).Scan(&m.AccountID, &m.UserID, &m.Role, &m.CreatedAt)
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (s *PostgresStore) UpdateAccountMemberRole(ctx context.Context, accountID, userID int, role string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE account_members SET role = $1 WHERE account_id = $2 AND user_id = $3`,
		role, accountID, userID)
	return err
}

// ---- Account Invitations ----

func (s *PostgresStore) CreateInvitation(ctx context.Context, inv *AccountInvitation) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO account_invitations (account_id, email, role, invited_by, expires_at)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, token, status, created_at`,
		inv.AccountID, inv.Email, inv.Role, inv.InvitedBy, inv.ExpiresAt,
	).Scan(&inv.ID, &inv.Token, &inv.Status, &inv.CreatedAt)
}

func (s *PostgresStore) GetInvitationByToken(ctx context.Context, token string) (*AccountInvitation, error) {
	inv := &AccountInvitation{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, account_id, email, role, token, status, invited_by, accepted_by, expires_at, created_at, responded_at
		 FROM account_invitations WHERE token = $1`, token,
	).Scan(&inv.ID, &inv.AccountID, &inv.Email, &inv.Role, &inv.Token, &inv.Status,
		&inv.InvitedBy, &inv.AcceptedBy, &inv.ExpiresAt, &inv.CreatedAt, &inv.RespondedAt)
	if err != nil {
		return nil, err
	}
	return inv, nil
}

func (s *PostgresStore) ListInvitationsByAccount(ctx context.Context, accountID int) ([]AccountInvitation, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, account_id, email, role, status, invited_by, accepted_by, expires_at, created_at, responded_at
		 FROM account_invitations WHERE account_id = $1 ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var invs []AccountInvitation
	for rows.Next() {
		var inv AccountInvitation
		if err := rows.Scan(&inv.ID, &inv.AccountID, &inv.Email, &inv.Role, &inv.Status,
			&inv.InvitedBy, &inv.AcceptedBy, &inv.ExpiresAt, &inv.CreatedAt, &inv.RespondedAt); err != nil {
			return nil, err
		}
		invs = append(invs, inv)
	}
	return invs, nil
}

func (s *PostgresStore) ListPendingInvitationsByEmail(ctx context.Context, email string) ([]AccountInvitation, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT i.id, i.account_id, i.email, i.role, i.status, i.invited_by, i.expires_at, i.created_at,
		        a.name
		 FROM account_invitations i
		 JOIN accounts a ON a.id = i.account_id
		 WHERE LOWER(i.email) = LOWER($1) AND i.status = 'pending' AND i.expires_at > now()
		 ORDER BY i.created_at DESC`, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var invs []AccountInvitation
	for rows.Next() {
		var inv AccountInvitation
		if err := rows.Scan(&inv.ID, &inv.AccountID, &inv.Email, &inv.Role, &inv.Status,
			&inv.InvitedBy, &inv.ExpiresAt, &inv.CreatedAt, &inv.AccountName); err != nil {
			return nil, err
		}
		invs = append(invs, inv)
	}
	return invs, nil
}

func (s *PostgresStore) AcceptInvitation(ctx context.Context, token string, userID int) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var inv AccountInvitation
	err = tx.QueryRow(ctx,
		`SELECT id, account_id, role, status
		 FROM account_invitations
		 WHERE token = $1
		 FOR UPDATE`,
		token,
	).Scan(&inv.ID, &inv.AccountID, &inv.Role, &inv.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("load invitation: %w", err)
	}
	if inv.Status != "pending" {
		return 0, nil
	}

	var existingUserID int
	err = tx.QueryRow(ctx,
		`SELECT user_id
		 FROM account_members
		 WHERE account_id = $1 AND user_id = $2`,
		inv.AccountID, userID,
	).Scan(&existingUserID)
	if err == nil {
		return 0, ErrAlreadyMember
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("check existing membership: %w", err)
	}

	tag, err := tx.Exec(ctx,
		`UPDATE account_invitations
		 SET status = 'accepted', accepted_by = $1, responded_at = now()
		 WHERE id = $2 AND status = 'pending'`,
		userID, inv.ID)
	if err != nil {
		return 0, fmt.Errorf("accept invitation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return 0, nil
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO account_members (account_id, user_id, role)
		 VALUES ($1, $2, $3)`,
		inv.AccountID, userID, inv.Role,
	); err != nil {
		if IsConflict(err) {
			return 0, ErrAlreadyMember
		}
		return 0, fmt.Errorf("add account member: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit invitation accept: %w", err)
	}

	return 1, nil
}

func (s *PostgresStore) DeclineInvitation(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE account_invitations
		 SET status = 'declined', responded_at = now()
		 WHERE token = $1 AND status = 'pending'`, token)
	return err
}

func (s *PostgresStore) RevokeInvitation(ctx context.Context, accountID, id int) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM account_invitations WHERE id = $1 AND account_id = $2 AND status = 'pending'`, id, accountID)
	return err
}

func (s *PostgresStore) ExpireOldInvitations(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE account_invitations SET status = 'expired' WHERE status = 'pending' AND expires_at < now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ---- Machines ----

// machineColumns is the column list for SELECT queries on the machines table.
const machineColumns = `id, account_id, kind, name, slug, preferred_region, status, status_message, vcpus, memory_mb,
	host_id, vm_ip, tunnel_hostname, custom_domain, gateway_token, proxy_token,
	provision_step, provisioning_started_at, provisioning_completed_at,
	budget_microcents, created_at, started_at, stopped_at, data_volume_gb,
	desired_rootfs_version, desired_openclaw_version, desired_channel,
	resolved_rootfs_version, resolved_openclaw_version, actual_rootfs_version, actual_openclaw_version, version_source, runtime_source,
	rootfs_snapshot, openclaw_version, last_started_at, tunnel_id, signing_key,
	home_host_id, storage_mode, backups_enabled, backup_key, browser_vm_id`

func scanMachine(scan func(dest ...any) error) (*Machine, error) {
	m := &Machine{}
	err := scan(&m.ID, &m.AccountID, &m.Kind, &m.Name, &m.Slug, &m.PreferredRegion, &m.Status, &m.StatusMessage,
		&m.VCPUs, &m.MemoryMB, &m.HostID, &m.VMIP, &m.TunnelHostname, &m.CustomDomain,
		&m.GatewayToken, &m.ProxyToken, &m.ProvisionStep, &m.ProvisioningStartedAt, &m.ProvisioningCompletedAt,
		&m.BudgetMicrocents, &m.CreatedAt, &m.StartedAt, &m.StoppedAt, &m.DataVolumeGB,
		&m.DesiredRootfsVersion, &m.DesiredOpenclawVersion, &m.DesiredChannel,
		&m.ResolvedRootfsVersion, &m.ResolvedOpenclawVersion, &m.ActualRootfsVersion, &m.ActualOpenclawVersion, &m.VersionSource, &m.RuntimeSource,
		&m.RootfsSnapshot, &m.OpenclawVersion, &m.LastStartedAt, &m.TunnelID, &m.SigningKey,
		&m.HomeHostID, &m.StorageMode, &m.BackupsEnabled, &m.BackupKey, &m.BrowserVMID)
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (s *PostgresStore) CreateMachine(ctx context.Context, m *Machine) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO machines (
			account_id, kind, name, slug, preferred_region, status, vcpus, memory_mb, data_volume_gb, gateway_token,
			desired_rootfs_version, desired_openclaw_version, desired_channel, version_source, runtime_source
		)
		 VALUES ($1, COALESCE(NULLIF($2, ''), 'openclaw'), $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, COALESCE($15, 'auto'))
		 RETURNING id, created_at`,
		m.AccountID, m.Kind, m.Name, m.Slug, m.PreferredRegion, m.Status, m.VCPUs, m.MemoryMB, m.DataVolumeGB, m.GatewayToken,
		m.DesiredRootfsVersion, m.DesiredOpenclawVersion, m.DesiredChannel, m.VersionSource, m.RuntimeSource,
	).Scan(&m.ID, &m.CreatedAt)
}

func (s *PostgresStore) GetMachine(ctx context.Context, id string) (*Machine, error) {
	return scanMachine(s.pool.QueryRow(ctx,
		`SELECT `+machineColumns+` FROM machines WHERE id = $1`, id).Scan)
}

func (s *PostgresStore) GetMachineBySlug(ctx context.Context, slug string) (*Machine, error) {
	return scanMachine(s.pool.QueryRow(ctx,
		`SELECT `+machineColumns+` FROM machines WHERE slug = $1`, slug).Scan)
}

func (s *PostgresStore) ListMachinesByAccount(ctx context.Context, accountID int) ([]Machine, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+machineColumns+` FROM machines WHERE account_id = $1 ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	machines := []Machine{}
	for rows.Next() {
		m, err := scanMachine(rows.Scan)
		if err != nil {
			return nil, err
		}
		machines = append(machines, *m)
	}
	return machines, nil
}

func (s *PostgresStore) ListMachinesByHost(ctx context.Context, hostID int) ([]Machine, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+machineColumns+` FROM machines WHERE host_id = $1`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	machines := []Machine{}
	for rows.Next() {
		m, err := scanMachine(rows.Scan)
		if err != nil {
			return nil, err
		}
		machines = append(machines, *m)
	}
	return machines, nil
}

func (s *PostgresStore) ListAllMachines(ctx context.Context) ([]Machine, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+machineColumns+` FROM machines ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	machines := []Machine{}
	for rows.Next() {
		m, err := scanMachine(rows.Scan)
		if err != nil {
			return nil, err
		}
		machines = append(machines, *m)
	}
	return machines, nil
}

func (s *PostgresStore) UpdateMachine(ctx context.Context, m *Machine) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machines
		 SET name=$2, vcpus=$3, memory_mb=$4, custom_domain=$5,
		     desired_rootfs_version=$6, desired_openclaw_version=$7, desired_channel=$8,
		     version_source=$9, runtime_source=$10
		 WHERE id = $1`,
		m.ID, m.Name, m.VCPUs, m.MemoryMB, m.CustomDomain,
		m.DesiredRootfsVersion, m.DesiredOpenclawVersion, m.DesiredChannel,
		m.VersionSource, m.RuntimeSource)
	return err
}

// SetMachineTokens stores both gateway and proxy tokens for a machine.
func (s *PostgresStore) SetMachineTokens(ctx context.Context, machineID string, gatewayToken, proxyToken string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machines SET gateway_token = $2, proxy_token = $3 WHERE id = $1`,
		machineID, gatewayToken, proxyToken)
	return err
}

// ListRunningRoutes returns route data for all running machines with valid hosts.
func (s *PostgresStore) ListRunningRoutes(ctx context.Context) ([]RouteData, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.slug, m.id, m.slug, h.tunnel_url, m.proxy_token
		FROM machines m
		JOIN accounts a ON m.account_id = a.id
		JOIN hosts h ON m.host_id = h.id
		WHERE m.status = 'running'
		  AND m.proxy_token IS NOT NULL
		  AND h.tunnel_url IS NOT NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var routes []RouteData
	for rows.Next() {
		var r RouteData
		if err := rows.Scan(&r.AccountSlug, &r.MachineID, &r.MachineSlug, &r.HostHostname, &r.ProxyToken); err != nil {
			return nil, err
		}
		routes = append(routes, r)
	}
	return routes, rows.Err()
}

func (s *PostgresStore) DeleteMachine(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM machines WHERE id = $1`, id)
	return err
}

func (s *PostgresStore) UpdateMachineStatus(ctx context.Context, id, status string, message *string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machines
		 SET status = $2,
		     status_message = $3,
		     provisioning_completed_at = CASE
		       WHEN $2 = 'running' AND provisioning_completed_at IS NULL THEN now()
		       ELSE provisioning_completed_at
		     END,
		     started_at = CASE WHEN $2 = 'running' THEN now() ELSE started_at END,
		     stopped_at = CASE WHEN $2 = 'stopped' THEN now() ELSE stopped_at END
		 WHERE id = $1`,
		id, status, message)
	return err
}

func (s *PostgresStore) UpdateMachineStatusCAS(ctx context.Context, id, expectedStatus, newStatus string, message *string) (bool, error) {
	result, err := s.pool.Exec(ctx,
		`UPDATE machines
		 SET status = $2,
		     status_message = $3,
		     provisioning_completed_at = CASE
		       WHEN $2 = 'running' AND provisioning_completed_at IS NULL THEN now()
		       ELSE provisioning_completed_at
		     END,
		     started_at = CASE WHEN $2 = 'running' THEN now() ELSE started_at END,
		     stopped_at = CASE WHEN $2 = 'stopped' THEN now() ELSE stopped_at END
		 WHERE id = $1 AND status = $4`,
		id, newStatus, message, expectedStatus)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() > 0, nil
}

func (s *PostgresStore) FindStuckMachines(ctx context.Context, stuckThreshold time.Duration) ([]Machine, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, slug, account_id, status, host_id
		 FROM machines
		 WHERE status IN ('provisioning', 'starting', 'stopping')
		   AND COALESCE(provisioning_started_at, created_at) < now() - $1::interval`, fmt.Sprintf("%d seconds", int(stuckThreshold.Seconds())))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var machines []Machine
	for rows.Next() {
		var m Machine
		if err := rows.Scan(&m.ID, &m.Name, &m.Slug, &m.AccountID, &m.Status, &m.HostID); err != nil {
			return nil, err
		}
		machines = append(machines, m)
	}
	return machines, nil
}

func (s *PostgresStore) AssignMachineToHost(ctx context.Context, machineID string, hostID int, vmIP string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machines SET host_id = $2, vm_ip = $3, status = 'provisioning',
		        provisioning_started_at = now()
		 WHERE id = $1`,
		machineID, hostID, vmIP)
	return err
}

func (s *PostgresStore) UnassignMachineFromHost(ctx context.Context, machineID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machines SET host_id = NULL, vm_ip = NULL, status = 'stopped',
		        stopped_at = now()
		 WHERE id = $1`, machineID)
	return err
}

// ---- Hosts ----

func (s *PostgresStore) CreateHost(ctx context.Context, h *Host) error {
	if h.Capabilities == nil {
		h.Capabilities = json.RawMessage(`{}`)
	}
	return s.pool.QueryRow(ctx,
		`INSERT INTO hosts (vm_name, vm_id, zone, region, machine_type, external_ip, internal_ip, tunnel_url, status, source_image, capacity_vcpus, capacity_memory_mb, capacity_disk_mb, capabilities)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		 RETURNING id, created_at`,
		h.VMName, h.VMID, h.Zone, h.Region, h.MachineType, h.ExternalIP, h.InternalIP, h.TunnelURL, h.Status, h.SourceImage, h.CapacityVCPUs, h.CapacityMemoryMB, h.CapacityDiskMB, h.Capabilities,
	).Scan(&h.ID, &h.CreatedAt)
}

func (s *PostgresStore) GetHost(ctx context.Context, id int) (*Host, error) {
	h := &Host{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, vm_name, vm_id, zone, region, machine_type, external_ip, internal_ip,
		        tunnel_url, status, status_message, source_image, capacity_vcpus, capacity_memory_mb, capacity_disk_mb,
		        used_vcpus, used_memory_mb, used_disk_mb, (SELECT COUNT(*) FROM machines WHERE machines.host_id = hosts.id AND machines.status = 'running') AS machine_count, agent_version, openclaw_version,
		        rootfs_version, browser_rootfs_version, last_heartbeat, created_at,
		        provider, provider_class, lifecycle_mode, agent_endpoint, agent_endpoint_type,
		        provider_host_id, provider_metadata, capabilities, labels, agent_token, maintenance_mode
		 FROM hosts WHERE id = $1`, id,
	).Scan(&h.ID, &h.VMName, &h.VMID, &h.Zone, &h.Region, &h.MachineType,
		&h.ExternalIP, &h.InternalIP, &h.TunnelURL, &h.Status, &h.StatusMessage, &h.SourceImage,
		&h.CapacityVCPUs, &h.CapacityMemoryMB, &h.CapacityDiskMB, &h.UsedVCPUs, &h.UsedMemoryMB, &h.UsedDiskMB,
		&h.MachineCount, &h.AgentVersion, &h.OpenclawVersion,
		&h.RootfsVersion, &h.BrowserRootfsVersion, &h.LastHeartbeat, &h.CreatedAt,
		&h.Provider, &h.ProviderClass, &h.LifecycleMode, &h.AgentEndpoint, &h.AgentEndpointType,
		&h.ProviderHostID, &h.ProviderMetadata, &h.Capabilities, &h.Labels, &h.AgentToken, &h.MaintenanceMode)
	if err != nil {
		return nil, err
	}
	return h, nil
}

func (s *PostgresStore) ListHosts(ctx context.Context) ([]Host, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, vm_name, vm_id, zone, region, machine_type, external_ip, internal_ip,
		        tunnel_url, status, status_message, source_image, capacity_vcpus, capacity_memory_mb, capacity_disk_mb,
		        used_vcpus, used_memory_mb, used_disk_mb, (SELECT COUNT(*) FROM machines WHERE machines.host_id = hosts.id AND machines.status = 'running') AS machine_count, agent_version, openclaw_version,
		        rootfs_version, browser_rootfs_version, last_heartbeat, created_at,
		        provider, provider_class, lifecycle_mode, agent_endpoint, agent_endpoint_type,
		        provider_host_id, provider_metadata, capabilities, labels, agent_token, maintenance_mode
		 FROM hosts ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	hosts := make([]Host, 0)
	for rows.Next() {
		var h Host
		if err := rows.Scan(&h.ID, &h.VMName, &h.VMID, &h.Zone, &h.Region, &h.MachineType,
			&h.ExternalIP, &h.InternalIP, &h.TunnelURL, &h.Status, &h.StatusMessage, &h.SourceImage,
			&h.CapacityVCPUs, &h.CapacityMemoryMB, &h.CapacityDiskMB, &h.UsedVCPUs, &h.UsedMemoryMB, &h.UsedDiskMB,
			&h.MachineCount, &h.AgentVersion, &h.OpenclawVersion,
			&h.RootfsVersion, &h.BrowserRootfsVersion, &h.LastHeartbeat, &h.CreatedAt,
			&h.Provider, &h.ProviderClass, &h.LifecycleMode, &h.AgentEndpoint, &h.AgentEndpointType,
			&h.ProviderHostID, &h.ProviderMetadata, &h.Capabilities, &h.Labels, &h.AgentToken, &h.MaintenanceMode); err != nil {
			return nil, err
		}
		hosts = append(hosts, h)
	}
	return hosts, nil
}

func (s *PostgresStore) FindHostWithCapacity(ctx context.Context, vcpus, memoryMB int, region, expectedImage string) (*Host, error) {
	h := &Host{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, vm_name, vm_id, zone, region, machine_type, external_ip, internal_ip,
		        tunnel_url, status, status_message, source_image, capacity_vcpus, capacity_memory_mb, capacity_disk_mb,
		        used_vcpus, used_memory_mb, used_disk_mb, (SELECT COUNT(*) FROM machines WHERE machines.host_id = hosts.id AND machines.status = 'running') AS machine_count, agent_version, openclaw_version,
		        rootfs_version, browser_rootfs_version, last_heartbeat, created_at,
		        provider, provider_class, lifecycle_mode, agent_endpoint, agent_endpoint_type,
		        provider_host_id, provider_metadata, capabilities, labels, agent_token, maintenance_mode
		 FROM hosts
		 WHERE status = 'ready'
		   AND last_heartbeat IS NOT NULL
		   AND last_heartbeat > now() - interval '180 seconds'
		   AND (capacity_vcpus - used_vcpus) >= $1
		   AND (capacity_memory_mb - used_memory_mb) >= $2
		   AND region = $3
		   AND (source_image = $4 OR $4 = '')
		 ORDER BY used_memory_mb DESC
		 LIMIT 1
		 FOR UPDATE SKIP LOCKED`, vcpus, memoryMB, region, expectedImage,
	).Scan(&h.ID, &h.VMName, &h.VMID, &h.Zone, &h.Region, &h.MachineType,
		&h.ExternalIP, &h.InternalIP, &h.TunnelURL, &h.Status, &h.StatusMessage, &h.SourceImage,
		&h.CapacityVCPUs, &h.CapacityMemoryMB, &h.CapacityDiskMB, &h.UsedVCPUs, &h.UsedMemoryMB, &h.UsedDiskMB,
		&h.MachineCount, &h.AgentVersion, &h.OpenclawVersion,
		&h.RootfsVersion, &h.BrowserRootfsVersion, &h.LastHeartbeat, &h.CreatedAt,
		&h.Provider, &h.ProviderClass, &h.LifecycleMode, &h.AgentEndpoint, &h.AgentEndpointType,
		&h.ProviderHostID, &h.ProviderMetadata, &h.Capabilities, &h.Labels, &h.AgentToken, &h.MaintenanceMode)
	if err != nil {
		return nil, err
	}
	return h, nil
}

func (s *PostgresStore) AllocateHostCapacity(ctx context.Context, hostID, vcpus, memoryMB int) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE hosts SET used_vcpus = used_vcpus + $2, used_memory_mb = used_memory_mb + $3
		 WHERE id = $1`, hostID, vcpus, memoryMB)
	return err
}

func (s *PostgresStore) ReleaseHostCapacity(ctx context.Context, hostID, vcpus, memoryMB int) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE hosts SET used_vcpus = GREATEST(0, used_vcpus - $2),
		        used_memory_mb = GREATEST(0, used_memory_mb - $3)
		 WHERE id = $1`, hostID, vcpus, memoryMB)
	return err
}

// ReleaseHostCapacityWithDisk releases all three resource dimensions (vCPU, memory, disk).
func (s *PostgresStore) ReleaseHostCapacityWithDisk(ctx context.Context, hostID, vcpus, memoryMB, diskMB int) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE hosts SET used_vcpus = GREATEST(0, used_vcpus - $2),
		        used_memory_mb = GREATEST(0, used_memory_mb - $3),
		        used_disk_mb = GREATEST(0, used_disk_mb - $4)
		 WHERE id = $1`, hostID, vcpus, memoryMB, diskMB)
	return err
}

// ListEligibleHosts returns hosts that are ready, have fresh heartbeats,
// and have sufficient resources across all three dimensions.
// Read-only, no locks. Returns all candidates for Go-side ranking.
func (s *PostgresStore) ListEligibleHosts(ctx context.Context, filter HostFilter) ([]Host, error) {
	query := `SELECT id, vm_name, vm_id, zone, region, machine_type, external_ip, internal_ip,
	                 tunnel_url, status, status_message, source_image, capacity_vcpus, capacity_memory_mb, capacity_disk_mb,
	                 used_vcpus, used_memory_mb, used_disk_mb, (SELECT COUNT(*) FROM machines WHERE machines.host_id = hosts.id AND machines.status = 'running') AS machine_count, agent_version, openclaw_version,
	                 rootfs_version, browser_rootfs_version, last_heartbeat, created_at,
	                 provider, provider_class, lifecycle_mode, agent_endpoint, agent_endpoint_type,
	                 provider_host_id, provider_metadata, capabilities, labels, agent_token, maintenance_mode
	          FROM hosts
	          WHERE status = 'ready'
	            AND maintenance_mode = false
	            AND last_heartbeat IS NOT NULL
	            AND last_heartbeat > now() - interval '180 seconds'
	            AND (capacity_vcpus - used_vcpus) >= $1
	            AND (capacity_memory_mb - used_memory_mb) >= $2
	            AND (capacity_disk_mb = 0 OR (capacity_disk_mb - used_disk_mb) >= $3)`
	args := []any{filter.MinVCPUs, filter.MinMemoryMB, filter.MinDiskMB}
	if filter.Region != "" {
		query += ` AND region = $4`
		args = append(args, filter.Region)
	}
	if filter.RequireBrowserReflink {
		query += ` AND (
			capabilities @> '{"browser_storage":{"reflink_supported":true}}'::jsonb
			OR (
				NOT (capabilities ? 'browser_storage')
				AND capabilities @> '{"storage":{"reflink_supported":true}}'::jsonb
			)
		)`
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	hosts := make([]Host, 0)
	for rows.Next() {
		var h Host
		if err := rows.Scan(&h.ID, &h.VMName, &h.VMID, &h.Zone, &h.Region, &h.MachineType,
			&h.ExternalIP, &h.InternalIP, &h.TunnelURL, &h.Status, &h.StatusMessage, &h.SourceImage,
			&h.CapacityVCPUs, &h.CapacityMemoryMB, &h.CapacityDiskMB, &h.UsedVCPUs, &h.UsedMemoryMB, &h.UsedDiskMB,
			&h.MachineCount, &h.AgentVersion, &h.OpenclawVersion,
			&h.RootfsVersion, &h.BrowserRootfsVersion, &h.LastHeartbeat, &h.CreatedAt,
			&h.Provider, &h.ProviderClass, &h.LifecycleMode, &h.AgentEndpoint, &h.AgentEndpointType,
			&h.ProviderHostID, &h.ProviderMetadata, &h.Capabilities, &h.Labels, &h.AgentToken, &h.MaintenanceMode); err != nil {
			return nil, err
		}
		hosts = append(hosts, h)
	}
	return hosts, nil
}

// PlaceOnHost atomically claims capacity, allocates a VM IP, and assigns the machine.
// Uses FOR UPDATE SKIP LOCKED on the host row. Returns error if host is contended
// or capacity changed since listing.
func (s *PostgresStore) PlaceOnHost(ctx context.Context, hostID int, machineID string, vcpus, memoryMB, diskMB int) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock host row and re-verify capacity (may have changed since listing)
	// capacity_disk_mb = 0 means disk is not tracked for this host (pre-existing host).
	var hostExists bool
	err = tx.QueryRow(ctx,
		`SELECT true FROM hosts
		 WHERE id = $1
		   AND status = 'ready'
		   AND maintenance_mode = false
		   AND last_heartbeat > now() - interval '180 seconds'
		   AND (capacity_vcpus - used_vcpus) >= $2
		   AND (capacity_memory_mb - used_memory_mb) >= $3
		   AND (capacity_disk_mb = 0 OR (capacity_disk_mb - used_disk_mb) >= $4)
		 FOR UPDATE SKIP LOCKED`,
		hostID, vcpus, memoryMB, diskMB).Scan(&hostExists)
	if err != nil {
		return "", fmt.Errorf("host %d not available or insufficient capacity: %w", hostID, err)
	}

	// Increment counters
	_, err = tx.Exec(ctx,
		`UPDATE hosts SET used_vcpus = used_vcpus + $2,
		        used_memory_mb = used_memory_mb + $3,
		        used_disk_mb = used_disk_mb + $4
		 WHERE id = $1`, hostID, vcpus, memoryMB, diskMB)
	if err != nil {
		return "", fmt.Errorf("allocate host capacity: %w", err)
	}

	// Allocate next free VM IP
	rows, err := tx.Query(ctx,
		`SELECT vm_ip FROM machines WHERE host_id = $1 AND vm_ip IS NOT NULL`, hostID)
	if err != nil {
		return "", fmt.Errorf("list used IPs: %w", err)
	}
	usedIPs := make(map[string]bool)
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			rows.Close()
			return "", fmt.Errorf("scan IP: %w", err)
		}
		usedIPs[ip] = true
	}
	rows.Close()

	var vmIP string
	for i := 10; i <= 250; i++ {
		trial := fmt.Sprintf("192.168.100.%d", i)
		if !usedIPs[trial] {
			vmIP = trial
			break
		}
	}
	if vmIP == "" {
		return "", fmt.Errorf("no available VM IPs on host %d", hostID)
	}

	// Assign machine to host
	_, err = tx.Exec(ctx,
		`UPDATE machines SET host_id = $2, vm_ip = $3, status = 'provisioning',
		        provisioning_started_at = now()
		 WHERE id = $1`, machineID, hostID, vmIP)
	if err != nil {
		return "", fmt.Errorf("assign machine to host: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit placement: %w", err)
	}

	return vmIP, nil
}

// SoftStopMachine releases host capacity but keeps host_id and vm_ip on the machine (host affinity).
// This allows the machine to restart on the same host where its data volume lives.
func (s *PostgresStore) SoftStopMachine(ctx context.Context, machineID string, hostID, vcpus, memoryMB, diskMB int) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Release vCPU and memory (disk stays allocated — data volume persists on host)
	_, err = tx.Exec(ctx,
		`UPDATE hosts SET used_vcpus = GREATEST(0, used_vcpus - $2),
		        used_memory_mb = GREATEST(0, used_memory_mb - $3)
		 WHERE id = $1`, hostID, vcpus, memoryMB)
	if err != nil {
		return fmt.Errorf("release host capacity: %w", err)
	}

	// Set status to stopped but KEEP host_id and vm_ip (host affinity)
	_, err = tx.Exec(ctx,
		`UPDATE machines SET status = 'stopped', stopped_at = now()
		 WHERE id = $1`, machineID)
	if err != nil {
		return fmt.Errorf("update machine status: %w", err)
	}

	return tx.Commit(ctx)
}

// ReAllocateHostCapacity re-allocates capacity on a specific host for a machine restart.
// Checks that the host has sufficient capacity before allocating and returns the VM IP
// to use for the restarted machine. If the machine released its IP while stopped,
// this assigns a fresh free host-local IP in the same transaction.
func (s *PostgresStore) ReAllocateHostCapacity(ctx context.Context, machineID string, hostID, vcpus, memoryMB, diskMB int) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock host row and check capacity/eligibility.
	var availVCPUs, availMemMB int
	err = tx.QueryRow(ctx,
		`SELECT (capacity_vcpus - used_vcpus), (capacity_memory_mb - used_memory_mb)
		 FROM hosts
		 WHERE id = $1
		   AND status = 'ready'
		   AND maintenance_mode = false
		   AND last_heartbeat > now() - interval '180 seconds'
		 FOR UPDATE`, hostID).Scan(&availVCPUs, &availMemMB)
	if err != nil {
		return "", fmt.Errorf("home host %d not eligible for affinity recovery: %w", hostID, err)
	}

	if availVCPUs < vcpus || availMemMB < memoryMB {
		return "", fmt.Errorf("host %d has insufficient capacity (need %d vCPUs/%d MB, have %d/%d)",
			hostID, vcpus, memoryMB, availVCPUs, availMemMB)
	}

	var existingVMIP *string
	if err := tx.QueryRow(ctx,
		`SELECT vm_ip FROM machines WHERE id = $1 FOR UPDATE`, machineID).Scan(&existingVMIP); err != nil {
		return "", fmt.Errorf("lock machine for affinity recovery: %w", err)
	}

	vmIP := ""
	if existingVMIP != nil {
		vmIP = *existingVMIP
	}
	if vmIP == "" {
		err = tx.QueryRow(ctx, `
			WITH used_ips AS (
				SELECT vm_ip FROM machine_placements WHERE host_id = $1 AND released_at IS NULL
				UNION ALL
				SELECT vm_ip FROM browser_vm_placements WHERE host_id = $1 AND released_at IS NULL
				UNION ALL
				SELECT vm_ip FROM machines WHERE host_id = $1 AND vm_ip IS NOT NULL
			),
			candidates AS (
				SELECT '192.168.100.' || n AS ip
				FROM generate_series(10, 250) n
				WHERE '192.168.100.' || n NOT IN (SELECT vm_ip FROM used_ips WHERE vm_ip IS NOT NULL)
				LIMIT 1
			)
			SELECT ip FROM candidates`, hostID).Scan(&vmIP)
		if err != nil {
			return "", fmt.Errorf("no free VM IP on home host %d: %w", hostID, err)
		}
	}

	// Allocate vCPU and memory capacity (disk stays allocated from original placement on soft stop)
	_, err = tx.Exec(ctx,
		`UPDATE hosts SET used_vcpus = used_vcpus + $2, used_memory_mb = used_memory_mb + $3
		 WHERE id = $1`, hostID, vcpus, memoryMB)
	if err != nil {
		return "", fmt.Errorf("allocate host capacity: %w", err)
	}

	// Set machine status to provisioning and make sure host_id/vm_ip are populated.
	_, err = tx.Exec(ctx,
		`UPDATE machines SET host_id = $2, vm_ip = $3, status = 'provisioning', provisioning_started_at = now()
		 WHERE id = $1`, machineID, hostID, vmIP)
	if err != nil {
		return "", fmt.Errorf("update machine status: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return vmIP, nil
}

func (s *PostgresStore) UpdateMachineVersion(ctx context.Context, id string, snapshot, version string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machines
		 SET rootfs_snapshot = $2,
		     openclaw_version = $3,
		     resolved_rootfs_version = NULLIF($2, ''),
		     resolved_openclaw_version = NULLIF($3, ''),
		     version_source = COALESCE(version_source, 'legacy'),
		     last_started_at = now()
		 WHERE id = $1`,
		id, snapshot, version)
	return err
}

func (s *PostgresStore) UpdateMachineActualVersions(ctx context.Context, hostID int, machineID, rootfsVersion, openclawVersion string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machines
		 SET actual_rootfs_version = NULLIF($3, ''),
		     actual_openclaw_version = NULLIF($4, ''),
		     rootfs_snapshot = CASE WHEN NULLIF($3, '') IS NULL THEN rootfs_snapshot ELSE NULLIF($3, '') END,
		     openclaw_version = CASE WHEN NULLIF($4, '') IS NULL THEN openclaw_version ELSE NULLIF($4, '') END
		 WHERE id = $1 AND host_id = $2`,
		machineID, hostID, rootfsVersion, openclawVersion)
	return err
}

func (s *PostgresStore) UpdateMachineTunnel(ctx context.Context, machineID, tunnelID, signingKey string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machines SET tunnel_id = NULLIF($2, ''), signing_key = NULLIF($3, '') WHERE id = $1`,
		machineID, tunnelID, signingKey)
	return err
}

func (s *PostgresStore) ClearMachineTunnel(ctx context.Context, machineID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machines SET tunnel_id = NULL, signing_key = NULL WHERE id = $1`,
		machineID)
	return err
}

func (s *PostgresStore) UpdateHostStatus(ctx context.Context, id int, status string) error {
	_, err := s.pool.Exec(ctx, `UPDATE hosts SET status = $2, status_message = NULL WHERE id = $1`, id, status)
	return err
}

func (s *PostgresStore) UpdateHostStatusMessage(ctx context.Context, id int, status, message string) error {
	_, err := s.pool.Exec(ctx, `UPDATE hosts SET status = $2, status_message = $3 WHERE id = $1`, id, status, message)
	return err
}

func (s *PostgresStore) SetHostMaintenanceMode(ctx context.Context, id int, enabled bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE hosts SET maintenance_mode = $2 WHERE id = $1`, id, enabled)
	return err
}

func (s *PostgresStore) UpdateHostDetails(ctx context.Context, h *Host) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE hosts SET vm_id = $2, external_ip = $3, internal_ip = $4, tunnel_url = $5
		 WHERE id = $1`, h.ID, h.VMID, h.ExternalIP, h.InternalIP, h.TunnelURL)
	return err
}

func (s *PostgresStore) DeleteHost(ctx context.Context, id int) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM hosts WHERE id = $1`, id)
	return err
}

func (s *PostgresStore) UpdateHostSourceImage(ctx context.Context, id int, sourceImage string) error {
	_, err := s.pool.Exec(ctx, `UPDATE hosts SET source_image = $2 WHERE id = $1`, id, sourceImage)
	return err
}

func (s *PostgresStore) UpdateHostVersions(ctx context.Context, id int, agentVersion, openclawVersion string) error {
	_, err := s.pool.Exec(ctx, `UPDATE hosts SET agent_version = $2, openclaw_version = $3 WHERE id = $1`, id, agentVersion, openclawVersion)
	return err
}

// UpdateHostHeartbeat atomically updates the host's last_heartbeat, external_ip,
// and agent_version in a single statement. Returns ipChanged=true if the IP was
// different from what was previously stored.
//
// Uses a CTE to snapshot the old IP before the UPDATE so the comparison is
// accurate (RETURNING runs after SET, so comparing in RETURNING would always
// see the new value).
func (s *PostgresStore) UpdateHostHeartbeat(ctx context.Context, id int, externalIP, agentVersion, rootfsVersion, browserRootfsVersion, openclawVersion string, capabilities json.RawMessage) (bool, error) {
	var ipChanged bool
	capabilitiesJSON := strings.TrimSpace(string(capabilities))
	var capabilitiesArg *string
	if capabilitiesJSON != "" && capabilitiesJSON != "null" {
		capabilitiesArg = &capabilitiesJSON
	}
	err := s.pool.QueryRow(ctx,
		`WITH old AS (
		   SELECT external_ip FROM hosts WHERE id = $1
		 )
		 UPDATE hosts
		 SET last_heartbeat        = now(),
		     external_ip           = $2,
		     agent_version         = $3,
		     rootfs_version        = $4,
		     browser_rootfs_version = $5,
		     openclaw_version       = CASE WHEN $6 = '' THEN openclaw_version ELSE $6 END,
		     capabilities           = CASE
		                                WHEN $7::jsonb IS NULL THEN capabilities
		                                ELSE COALESCE(capabilities, '{}'::jsonb) || $7::jsonb
		                              END
		 FROM old
		 WHERE hosts.id = $1
		 RETURNING (old.external_ip IS DISTINCT FROM $2)`,
		id, externalIP, agentVersion, rootfsVersion, browserRootfsVersion, openclawVersion, capabilitiesArg,
	).Scan(&ipChanged)
	return ipChanged, err
}

func (s *PostgresStore) ListStaleHosts(ctx context.Context, threshold time.Duration) ([]Host, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, vm_name, vm_id, zone, region, machine_type, external_ip, internal_ip,
		        tunnel_url, status, status_message, source_image, capacity_vcpus, capacity_memory_mb, capacity_disk_mb,
		        used_vcpus, used_memory_mb, used_disk_mb, (SELECT COUNT(*) FROM machines WHERE machines.host_id = hosts.id AND machines.status = 'running') AS machine_count, agent_version, openclaw_version,
		        rootfs_version, browser_rootfs_version, last_heartbeat, created_at,
		        provider, provider_class, lifecycle_mode, agent_endpoint, agent_endpoint_type,
		        provider_host_id, provider_metadata, capabilities, labels, agent_token, maintenance_mode
		 FROM hosts
		 WHERE status = 'ready'
		   AND maintenance_mode = false
		   AND (last_heartbeat IS NULL OR last_heartbeat < now() - $1::interval)`, threshold.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	hosts := make([]Host, 0)
	for rows.Next() {
		var h Host
		if err := rows.Scan(&h.ID, &h.VMName, &h.VMID, &h.Zone, &h.Region, &h.MachineType,
			&h.ExternalIP, &h.InternalIP, &h.TunnelURL, &h.Status, &h.StatusMessage, &h.SourceImage,
			&h.CapacityVCPUs, &h.CapacityMemoryMB, &h.CapacityDiskMB, &h.UsedVCPUs, &h.UsedMemoryMB, &h.UsedDiskMB,
			&h.MachineCount, &h.AgentVersion, &h.OpenclawVersion,
			&h.RootfsVersion, &h.BrowserRootfsVersion, &h.LastHeartbeat, &h.CreatedAt,
			&h.Provider, &h.ProviderClass, &h.LifecycleMode, &h.AgentEndpoint, &h.AgentEndpointType,
			&h.ProviderHostID, &h.ProviderMetadata, &h.Capabilities, &h.Labels, &h.AgentToken, &h.MaintenanceMode); err != nil {
			return nil, err
		}
		hosts = append(hosts, h)
	}
	return hosts, nil
}

func (s *PostgresStore) ListUnreachableHosts(ctx context.Context) ([]Host, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, vm_name, vm_id, zone, region, machine_type, external_ip, internal_ip,
		        tunnel_url, status, status_message, source_image, capacity_vcpus, capacity_memory_mb, capacity_disk_mb,
		        used_vcpus, used_memory_mb, used_disk_mb, (SELECT COUNT(*) FROM machines WHERE machines.host_id = hosts.id AND machines.status = 'running') AS machine_count, agent_version, openclaw_version,
		        rootfs_version, browser_rootfs_version, last_heartbeat, created_at,
		        provider, provider_class, lifecycle_mode, agent_endpoint, agent_endpoint_type,
		        provider_host_id, provider_metadata, capabilities, labels, agent_token, maintenance_mode
		 FROM hosts
		 WHERE status = 'unreachable'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	hosts := make([]Host, 0)
	for rows.Next() {
		var h Host
		if err := rows.Scan(&h.ID, &h.VMName, &h.VMID, &h.Zone, &h.Region, &h.MachineType,
			&h.ExternalIP, &h.InternalIP, &h.TunnelURL, &h.Status, &h.StatusMessage, &h.SourceImage,
			&h.CapacityVCPUs, &h.CapacityMemoryMB, &h.CapacityDiskMB, &h.UsedVCPUs, &h.UsedMemoryMB, &h.UsedDiskMB,
			&h.MachineCount, &h.AgentVersion, &h.OpenclawVersion,
			&h.RootfsVersion, &h.BrowserRootfsVersion, &h.LastHeartbeat, &h.CreatedAt,
			&h.Provider, &h.ProviderClass, &h.LifecycleMode, &h.AgentEndpoint, &h.AgentEndpointType,
			&h.ProviderHostID, &h.ProviderMetadata, &h.Capabilities, &h.Labels, &h.AgentToken, &h.MaintenanceMode); err != nil {
			return nil, err
		}
		hosts = append(hosts, h)
	}
	return hosts, nil
}

func (s *PostgresStore) ReconcileHostCapacityVCPUs(ctx context.Context, ratio int) (int, error) {
	// For registered hosts with capabilities JSON containing cpu_threads,
	// recalculate capacity_vcpus = cpu_threads * ratio.
	// For managed (provisioned) hosts we use the machine_type preset to derive
	// physical CPUs, then multiply by ratio.
	//
	// This single UPDATE covers both paths by extracting cpu_threads from the
	// capabilities JSONB column. Managed hosts also get cpu_threads populated
	// at provision time, so this works uniformly.
	tag, err := s.pool.Exec(ctx,
		`UPDATE hosts
		    SET capacity_vcpus = (capabilities->>'cpu_threads')::int * $1
		  WHERE status != 'terminated'
		    AND capabilities->>'cpu_threads' IS NOT NULL
		    AND (capabilities->>'cpu_threads')::int > 0
		    AND capacity_vcpus != (capabilities->>'cpu_threads')::int * $1`,
		ratio)
	if err != nil {
		return 0, fmt.Errorf("reconcile host capacity: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (s *PostgresStore) MarkMachinesOnHostError(ctx context.Context, hostID int, message string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`UPDATE machines SET status = 'error', status_message = $2
		 WHERE host_id = $1 AND status IN ('running', 'provisioning', 'starting')
		 RETURNING id`, hostID, message)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ---- Activity Log ----

const activityColumns = `id, event_id, category, action, status, actor_type, actor_id, actor_name,
	account_id, account_name, machine_id, machine_name, host_id, host_name,
	agent_version, rootfs_version, summary, detail, started_at, duration_ms,
	error_code, error_message`

func scanActivity(rows pgx.Rows, a *ActivityLog) error {
	return rows.Scan(
		&a.ID, &a.EventID, &a.Category, &a.Action, &a.Status,
		&a.ActorType, &a.ActorID, &a.ActorName,
		&a.AccountID, &a.AccountName, &a.MachineID, &a.MachineName,
		&a.HostID, &a.HostName, &a.AgentVersion, &a.RootfsVersion,
		&a.Summary, &a.Detail, &a.StartedAt, &a.DurationMS,
		&a.ErrorCode, &a.ErrorMessage,
	)
}

func (s *PostgresStore) CreateActivity(ctx context.Context, a *ActivityLog) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO activity_log (category, action, status, actor_type, actor_id, actor_name,
			account_id, account_name, machine_id, machine_name, host_id, host_name,
			agent_version, rootfs_version, summary, detail, started_at, duration_ms,
			error_code, error_message)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		 RETURNING id, event_id`,
		a.Category, a.Action, a.Status, a.ActorType, a.ActorID, a.ActorName,
		a.AccountID, a.AccountName, a.MachineID, a.MachineName, a.HostID, a.HostName,
		a.AgentVersion, a.RootfsVersion, a.Summary, a.Detail, a.StartedAt, a.DurationMS,
		a.ErrorCode, a.ErrorMessage,
	).Scan(&a.ID, &a.EventID)
}

func (s *PostgresStore) ListActivities(ctx context.Context, accountID int, filter ActivityFilter) ([]ActivityLog, error) {
	return s.listActivities(ctx, "account_id = $1", accountID, filter)
}

func (s *PostgresStore) ListActivitiesByMachine(ctx context.Context, machineID string, filter ActivityFilter) ([]ActivityLog, error) {
	return s.listActivities(ctx, "machine_id = $1", machineID, filter)
}

func (s *PostgresStore) ListActivitiesAdmin(ctx context.Context, filter ActivityFilter) ([]ActivityLog, error) {
	return s.listActivities(ctx, "", nil, filter)
}

func (s *PostgresStore) listActivities(ctx context.Context, scopeClause string, scopeArg interface{}, filter ActivityFilter) ([]ActivityLog, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}

	var conditions []string
	var args []interface{}
	argIdx := 1

	if scopeClause != "" {
		conditions = append(conditions, scopeClause)
		args = append(args, scopeArg)
		argIdx++
	}
	if filter.Category != "" {
		conditions = append(conditions, fmt.Sprintf("category = $%d", argIdx))
		args = append(args, filter.Category)
		argIdx++
	}
	if filter.Action != "" {
		conditions = append(conditions, fmt.Sprintf("action = $%d", argIdx))
		args = append(args, filter.Action)
		argIdx++
	}
	if filter.Status != "" {
		conditions = append(conditions, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, filter.Status)
		argIdx++
	}
	if filter.ActorType != "" {
		conditions = append(conditions, fmt.Sprintf("actor_type = $%d", argIdx))
		args = append(args, filter.ActorType)
		argIdx++
	}
	if filter.ActorID != nil {
		conditions = append(conditions, fmt.Sprintf("actor_id = $%d", argIdx))
		args = append(args, *filter.ActorID)
		argIdx++
	}
	if filter.AccountID != nil {
		conditions = append(conditions, fmt.Sprintf("account_id = $%d", argIdx))
		args = append(args, *filter.AccountID)
		argIdx++
	}
	if filter.MachineID != nil {
		conditions = append(conditions, fmt.Sprintf("machine_id = $%d", argIdx))
		args = append(args, *filter.MachineID)
		argIdx++
	}
	if filter.HostID != nil {
		conditions = append(conditions, fmt.Sprintf("host_id = $%d", argIdx))
		args = append(args, *filter.HostID)
		argIdx++
	}
	if filter.CursorTime != nil && filter.CursorID != nil {
		conditions = append(conditions, fmt.Sprintf("(started_at, id) < ($%d, $%d)", argIdx, argIdx+1))
		args = append(args, *filter.CursorTime, *filter.CursorID)
		argIdx += 2
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + conditions[0]
		for _, c := range conditions[1:] {
			where += " AND " + c
		}
	}

	query := fmt.Sprintf(`SELECT %s FROM activity_log %s ORDER BY started_at DESC, id DESC LIMIT $%d`,
		activityColumns, where, argIdx)
	args = append(args, limit)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make([]ActivityLog, 0)
	for rows.Next() {
		var a ActivityLog
		if err := scanActivity(rows, &a); err != nil {
			return nil, err
		}
		results = append(results, a)
	}
	return results, nil
}

// ---- Secrets ----

func (s *PostgresStore) SetSecret(ctx context.Context, machineID, key, encryptedValue string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO secrets (machine_id, key, encrypted_value)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (machine_id, key)
		 DO UPDATE SET encrypted_value = EXCLUDED.encrypted_value, updated_at = now()`,
		machineID, key, encryptedValue)
	return err
}

func (s *PostgresStore) ListSecrets(ctx context.Context, machineID string) ([]Secret, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, machine_id, key, created_at, updated_at
		 FROM secrets WHERE machine_id = $1 ORDER BY key`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var secrets []Secret
	for rows.Next() {
		var sec Secret
		if err := rows.Scan(&sec.ID, &sec.MachineID, &sec.Key, &sec.CreatedAt, &sec.UpdatedAt); err != nil {
			return nil, err
		}
		secrets = append(secrets, sec)
	}
	return secrets, nil
}

func (s *PostgresStore) ListSecretsWithValues(ctx context.Context, machineID string) ([]Secret, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, machine_id, key, encrypted_value, created_at, updated_at
		 FROM secrets WHERE machine_id = $1 ORDER BY key`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var secrets []Secret
	for rows.Next() {
		var sec Secret
		if err := rows.Scan(&sec.ID, &sec.MachineID, &sec.Key, &sec.EncryptedValue, &sec.CreatedAt, &sec.UpdatedAt); err != nil {
			return nil, err
		}
		secrets = append(secrets, sec)
	}
	return secrets, nil
}

func (s *PostgresStore) DeleteSecret(ctx context.Context, machineID, key string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM secrets WHERE machine_id = $1 AND key = $2`, machineID, key)
	return err
}

// ---- Credentials (machine-scoped) ----

func (s *PostgresStore) SetMachineCredential(ctx context.Context, machineID string, cred *Credential) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO credentials (account_id, machine_id, provider, credential_type, encrypted_value, label, last_four, expires_at, refresh_token, status, status_detail)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, COALESCE(NULLIF($10, ''), 'active'), $11)
		 ON CONFLICT (machine_id, provider)
		 DO UPDATE SET credential_type = EXCLUDED.credential_type,
		              encrypted_value = EXCLUDED.encrypted_value,
		              label = EXCLUDED.label,
		              last_four = EXCLUDED.last_four,
		              expires_at = EXCLUDED.expires_at,
		              refresh_token = EXCLUDED.refresh_token,
		              status = COALESCE(NULLIF(EXCLUDED.status, ''), 'active'),
		              status_detail = EXCLUDED.status_detail,
		              updated_at = now()
		 RETURNING id, created_at, updated_at`,
		cred.AccountID, machineID, cred.Provider, cred.CredentialType, cred.EncryptedValue,
		cred.Label, cred.LastFour, cred.ExpiresAt, cred.RefreshToken, cred.Status, cred.StatusDetail,
	).Scan(&cred.ID, &cred.CreatedAt, &cred.UpdatedAt)
}

func (s *PostgresStore) ListMachineCredentials(ctx context.Context, machineID string) ([]Credential, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, account_id, machine_id, provider, credential_type, label, last_validated, last_four, expires_at, status, status_detail, created_at, updated_at
		 FROM credentials WHERE machine_id = $1 ORDER BY provider`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var creds []Credential
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.ID, &c.AccountID, &c.MachineID, &c.Provider, &c.CredentialType,
			&c.Label, &c.LastValidated, &c.LastFour, &c.ExpiresAt, &c.Status, &c.StatusDetail, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		creds = append(creds, c)
	}
	return creds, nil
}

func (s *PostgresStore) ListMachineCredentialsWithValues(ctx context.Context, machineID string) ([]Credential, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, account_id, machine_id, provider, credential_type, encrypted_value, label, last_validated, last_four, expires_at, refresh_token, status, status_detail, created_at, updated_at
		 FROM credentials WHERE machine_id = $1 ORDER BY provider`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var creds []Credential
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.ID, &c.AccountID, &c.MachineID, &c.Provider, &c.CredentialType, &c.EncryptedValue,
			&c.Label, &c.LastValidated, &c.LastFour, &c.ExpiresAt, &c.RefreshToken, &c.Status, &c.StatusDetail, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		creds = append(creds, c)
	}
	return creds, nil
}

func (s *PostgresStore) GetMachineCredentialWithValue(ctx context.Context, machineID string, provider string) (*Credential, error) {
	c := &Credential{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, account_id, machine_id, provider, credential_type, encrypted_value, label, last_validated, last_four, expires_at, refresh_token, status, status_detail, created_at, updated_at
		 FROM credentials WHERE machine_id = $1 AND provider = $2`,
		machineID, provider,
	).Scan(&c.ID, &c.AccountID, &c.MachineID, &c.Provider, &c.CredentialType, &c.EncryptedValue,
		&c.Label, &c.LastValidated, &c.LastFour, &c.ExpiresAt, &c.RefreshToken, &c.Status, &c.StatusDetail, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (s *PostgresStore) DeleteMachineCredential(ctx context.Context, machineID string, provider string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM credentials WHERE machine_id = $1 AND provider = $2`, machineID, provider)
	return err
}

func (s *PostgresStore) UpdateCredentialStatus(ctx context.Context, machineID string, provider string, status string, detail *string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE credentials SET status = $1, status_detail = $2, updated_at = now() WHERE machine_id = $3 AND provider = $4`,
		status, detail, machineID, provider)
	return err
}

func (s *PostgresStore) UpdateCredentialTokens(ctx context.Context, credID int, encryptedValue, encryptedRefreshToken string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE credentials SET encrypted_value = $1, refresh_token = $2, expires_at = $3, status = 'active', status_detail = NULL, updated_at = now() WHERE id = $4`,
		encryptedValue, encryptedRefreshToken, expiresAt, credID)
	return err
}

// PlaceMachineOnHost atomically finds a host, allocates capacity, assigns an IP, and places the machine.
// The entire operation runs in a single transaction to prevent duplicate IP assignment races.
// If expectedImage is non-empty, only hosts with matching source_image are considered.
func (s *PostgresStore) PlaceMachineOnHost(ctx context.Context, machineID string, vcpus, memoryMB int, region, expectedImage string) (*Host, string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Find host with capacity (row locked for duration of transaction)
	h := &Host{}
	query := `SELECT id, vm_name, vm_id, zone, region, machine_type, external_ip, internal_ip,
	                 tunnel_url, status, status_message, source_image, capacity_vcpus, capacity_memory_mb, capacity_disk_mb,
	                 used_vcpus, used_memory_mb, used_disk_mb, (SELECT COUNT(*) FROM machines WHERE machines.host_id = hosts.id AND machines.status = 'running') AS machine_count, agent_version, openclaw_version,
	                 rootfs_version, browser_rootfs_version, last_heartbeat, created_at,
	                 provider, provider_class, lifecycle_mode, agent_endpoint, agent_endpoint_type,
	                 provider_host_id, provider_metadata, capabilities, labels, agent_token, maintenance_mode
	          FROM hosts
	          WHERE status = 'ready'
	            AND last_heartbeat IS NOT NULL
	            AND last_heartbeat > now() - interval '180 seconds'
	            AND (capacity_vcpus - used_vcpus) >= $1
	            AND (capacity_memory_mb - used_memory_mb) >= $2`
	args := []any{vcpus, memoryMB}
	argIndex := 3
	if region != "" {
		query += fmt.Sprintf(` AND region = $%d`, argIndex)
		args = append(args, region)
		argIndex++
	}
	if expectedImage != "" {
		query += fmt.Sprintf(` AND source_image = $%d`, argIndex)
		args = append(args, expectedImage)
	}
	query += ` ORDER BY used_memory_mb DESC LIMIT 1 FOR UPDATE`

	err = tx.QueryRow(ctx, query, args...).Scan(
		&h.ID, &h.VMName, &h.VMID, &h.Zone, &h.Region, &h.MachineType,
		&h.ExternalIP, &h.InternalIP, &h.TunnelURL, &h.Status, &h.StatusMessage, &h.SourceImage,
		&h.CapacityVCPUs, &h.CapacityMemoryMB, &h.CapacityDiskMB, &h.UsedVCPUs, &h.UsedMemoryMB, &h.UsedDiskMB,
		&h.MachineCount, &h.AgentVersion, &h.OpenclawVersion,
		&h.RootfsVersion, &h.BrowserRootfsVersion, &h.LastHeartbeat, &h.CreatedAt,
		&h.Provider, &h.ProviderClass, &h.LifecycleMode, &h.AgentEndpoint, &h.AgentEndpointType,
		&h.ProviderHostID, &h.ProviderMetadata, &h.Capabilities, &h.Labels, &h.AgentToken, &h.MaintenanceMode)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", fmt.Errorf("%w (region=%q, image=%q)", ErrNoEligibleHost, region, expectedImage)
		}
		return nil, "", fmt.Errorf("place machine on host: %w", err)
	}

	// Allocate capacity
	_, err = tx.Exec(ctx,
		`UPDATE hosts SET used_vcpus = used_vcpus + $2, used_memory_mb = used_memory_mb + $3
		 WHERE id = $1`, h.ID, vcpus, memoryMB)
	if err != nil {
		return nil, "", fmt.Errorf("allocate host capacity: %w", err)
	}

	// Find next available VM IP (within the held transaction lock)
	rows, err := tx.Query(ctx,
		`SELECT vm_ip FROM machines WHERE host_id = $1 AND vm_ip IS NOT NULL`, h.ID)
	if err != nil {
		return nil, "", fmt.Errorf("list used IPs: %w", err)
	}
	usedIPs := make(map[string]bool)
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			rows.Close()
			return nil, "", fmt.Errorf("scan IP: %w", err)
		}
		usedIPs[ip] = true
	}
	rows.Close()

	var vmIP string
	for i := 10; i <= 250; i++ {
		trial := fmt.Sprintf("192.168.100.%d", i)
		if !usedIPs[trial] {
			vmIP = trial
			break
		}
	}
	if vmIP == "" {
		return nil, "", fmt.Errorf("no available VM IPs on host %d", h.ID)
	}

	// Assign machine to host
	_, err = tx.Exec(ctx,
		`UPDATE machines SET host_id = $2, vm_ip = $3, status = 'provisioning',
		        provisioning_started_at = now()
		 WHERE id = $1`, machineID, h.ID, vmIP)
	if err != nil {
		return nil, "", fmt.Errorf("assign machine to host: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, "", fmt.Errorf("commit placement: %w", err)
	}

	return h, vmIP, nil
}

// PlaceMachineOnSpecificHost places a machine on a specific host, allocating capacity and a VM IP.
// Like PlaceMachineOnHost but targets a specific host instead of auto-selecting.
func (s *PostgresStore) PlaceMachineOnSpecificHost(ctx context.Context, machineID string, hostID, vcpus, memoryMB int) (*Host, string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock host row and verify it has capacity
	h := &Host{}
	err = tx.QueryRow(ctx,
		`SELECT id, vm_name, vm_id, zone, region, machine_type, external_ip, internal_ip,
		        tunnel_url, status, status_message, source_image, capacity_vcpus, capacity_memory_mb, capacity_disk_mb,
		        used_vcpus, used_memory_mb, used_disk_mb, (SELECT COUNT(*) FROM machines WHERE machines.host_id = hosts.id AND machines.status = 'running') AS machine_count, agent_version, openclaw_version,
		        rootfs_version, browser_rootfs_version, last_heartbeat, created_at,
		        provider, provider_class, lifecycle_mode, agent_endpoint, agent_endpoint_type,
		        provider_host_id, provider_metadata, capabilities, labels, agent_token, maintenance_mode
		 FROM hosts
		 WHERE id = $1
		   AND status = 'ready'
		   AND last_heartbeat IS NOT NULL
		   AND last_heartbeat > now() - interval '180 seconds'
		   AND (capacity_vcpus - used_vcpus) >= $2
		   AND (capacity_memory_mb - used_memory_mb) >= $3
		 FOR UPDATE`,
		hostID, vcpus, memoryMB).Scan(
		&h.ID, &h.VMName, &h.VMID, &h.Zone, &h.Region, &h.MachineType,
		&h.ExternalIP, &h.InternalIP, &h.TunnelURL, &h.Status, &h.StatusMessage, &h.SourceImage,
		&h.CapacityVCPUs, &h.CapacityMemoryMB, &h.CapacityDiskMB, &h.UsedVCPUs, &h.UsedMemoryMB, &h.UsedDiskMB,
		&h.MachineCount, &h.AgentVersion, &h.OpenclawVersion,
		&h.RootfsVersion, &h.BrowserRootfsVersion, &h.LastHeartbeat, &h.CreatedAt,
		&h.Provider, &h.ProviderClass, &h.LifecycleMode, &h.AgentEndpoint, &h.AgentEndpointType,
		&h.ProviderHostID, &h.ProviderMetadata, &h.Capabilities, &h.Labels, &h.AgentToken, &h.MaintenanceMode)
	if err != nil {
		return nil, "", fmt.Errorf("target host %d not ready or insufficient capacity: %w", hostID, err)
	}

	// Allocate capacity
	_, err = tx.Exec(ctx,
		`UPDATE hosts SET used_vcpus = used_vcpus + $2, used_memory_mb = used_memory_mb + $3
		 WHERE id = $1`, h.ID, vcpus, memoryMB)
	if err != nil {
		return nil, "", fmt.Errorf("allocate host capacity: %w", err)
	}

	// Find next available VM IP
	rows, err := tx.Query(ctx,
		`SELECT vm_ip FROM machines WHERE host_id = $1 AND vm_ip IS NOT NULL`, h.ID)
	if err != nil {
		return nil, "", fmt.Errorf("list used IPs: %w", err)
	}
	usedIPs := make(map[string]bool)
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			rows.Close()
			return nil, "", fmt.Errorf("scan IP: %w", err)
		}
		usedIPs[ip] = true
	}
	rows.Close()

	var vmIP string
	for i := 10; i <= 250; i++ {
		trial := fmt.Sprintf("192.168.100.%d", i)
		if !usedIPs[trial] {
			vmIP = trial
			break
		}
	}
	if vmIP == "" {
		return nil, "", fmt.Errorf("no available VM IPs on host %d", h.ID)
	}

	// Assign machine to host
	_, err = tx.Exec(ctx,
		`UPDATE machines SET host_id = $2, vm_ip = $3, status = 'provisioning',
		        provisioning_started_at = now()
		 WHERE id = $1`, machineID, h.ID, vmIP)
	if err != nil {
		return nil, "", fmt.Errorf("assign machine to host: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, "", fmt.Errorf("commit placement: %w", err)
	}

	return h, vmIP, nil
}

// ResolveRoute looks up a running machine by account slug + machine slug,
// returning all data needed for the Worker to proxy the request.
func (s *PostgresStore) ResolveRoute(ctx context.Context, accountSlug, machineSlug string) (*ResolvedRoute, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT a.id, m.id, COALESCE(m.proxy_token, ''), COALESCE(h.tunnel_url, '')
		FROM accounts a
		JOIN machines m ON m.account_id = a.id
		JOIN hosts h ON h.id = m.host_id
		WHERE a.slug = $1
		  AND m.slug = $2
		  AND m.status = 'running'
		  AND h.tunnel_url IS NOT NULL
	`, accountSlug, machineSlug)

	var rr ResolvedRoute
	if err := row.Scan(&rr.AccountID, &rr.MachineID, &rr.ProxyToken, &rr.HostHostname); err != nil {
		return nil, err
	}

	// Fetch user IDs who have access to this account
	rows, err := s.pool.Query(ctx,
		`SELECT user_id FROM account_members WHERE account_id = $1`, rr.AccountID)
	if err != nil {
		return nil, fmt.Errorf("fetch account members: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var uid int
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		rr.UserIDs = append(rr.UserIDs, uid)
	}

	return &rr, nil
}

func (s *PostgresStore) IsMachineActive(ctx context.Context, slug string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM machines WHERE slug = $1 AND status IN ('running', 'provisioning'))`,
		slug).Scan(&exists)
	return exists, err
}

// ---- Registry ----

func (s *PostgresStore) ListRegistryEntries(ctx context.Context, entryType string) ([]RegistryEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, type, name, description, version, tier, modes, config_template,
		        infrastructure, required_credentials, status, sort_order, created_at, updated_at
		 FROM registry_entries
		 WHERE type = $1 AND status = 'active'
		 ORDER BY sort_order, name`, entryType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []RegistryEntry
	for rows.Next() {
		var e RegistryEntry
		if err := rows.Scan(&e.ID, &e.Type, &e.Name, &e.Description, &e.Version, &e.Tier,
			&e.Modes, &e.ConfigTemplate, &e.Infrastructure, &e.RequiredCredentials,
			&e.Status, &e.SortOrder, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *PostgresStore) GetRegistryEntry(ctx context.Context, id string) (*RegistryEntry, error) {
	e := &RegistryEntry{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, type, name, description, version, tier, modes, config_template,
		        infrastructure, required_credentials, status, sort_order, created_at, updated_at
		 FROM registry_entries WHERE id = $1`, id,
	).Scan(&e.ID, &e.Type, &e.Name, &e.Description, &e.Version, &e.Tier,
		&e.Modes, &e.ConfigTemplate, &e.Infrastructure, &e.RequiredCredentials,
		&e.Status, &e.SortOrder, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (s *PostgresStore) ListRegistryEntriesAdmin(ctx context.Context, kind string) ([]RegistryEntry, error) {
	var query string
	var args []interface{}
	if kind != "" {
		query = `SELECT id, type, name, description, version, tier, modes, config_template,
		         infrastructure, required_credentials, status, sort_order, created_at, updated_at
		  FROM registry_entries
		  WHERE type = $1
		  ORDER BY sort_order, name`
		args = []interface{}{kind}
	} else {
		query = `SELECT id, type, name, description, version, tier, modes, config_template,
		         infrastructure, required_credentials, status, sort_order, created_at, updated_at
		  FROM registry_entries
		  ORDER BY type, sort_order, name`
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []RegistryEntry
	for rows.Next() {
		var e RegistryEntry
		if err := rows.Scan(&e.ID, &e.Type, &e.Name, &e.Description, &e.Version, &e.Tier,
			&e.Modes, &e.ConfigTemplate, &e.Infrastructure, &e.RequiredCredentials,
			&e.Status, &e.SortOrder, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *PostgresStore) CreateRegistryEntry(ctx context.Context, entry *RegistryEntry) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO registry_entries (id, type, name, description, version, tier, modes, config_template, infrastructure, required_credentials, status, sort_order)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 RETURNING created_at, updated_at`,
		entry.ID, entry.Type, entry.Name, entry.Description, entry.Version, entry.Tier,
		entry.Modes, entry.ConfigTemplate, entry.Infrastructure, entry.RequiredCredentials,
		entry.Status, entry.SortOrder,
	).Scan(&entry.CreatedAt, &entry.UpdatedAt)
}

func (s *PostgresStore) UpdateRegistryEntry(ctx context.Context, entry *RegistryEntry) error {
	result, err := s.pool.Exec(ctx,
		`UPDATE registry_entries
		 SET name = $2, description = $3, config_template = $4, modes = $5,
		     infrastructure = $6, required_credentials = $7, status = $8,
		     sort_order = $9, tier = $10, version = $11, updated_at = NOW()
		 WHERE id = $1`,
		entry.ID, entry.Name, entry.Description, entry.ConfigTemplate, entry.Modes,
		entry.Infrastructure, entry.RequiredCredentials, entry.Status,
		entry.SortOrder, entry.Tier, entry.Version,
	)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("registry entry %q not found", entry.ID)
	}
	return nil
}

func (s *PostgresStore) DeleteRegistryEntry(ctx context.Context, id string) error {
	result, err := s.pool.Exec(ctx,
		`DELETE FROM registry_entries WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("registry entry %q not found", id)
	}
	return nil
}

// ---- Machine Capabilities ----

func (s *PostgresStore) ListMachineCapabilities(ctx context.Context, machineID string) ([]MachineCapability, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT mc.id, mc.machine_id, mc.entry_id, mc.mode, mc.enabled, mc.config_overrides,
		        mc.created_at, mc.updated_at, re.name AS entry_name, re.type AS entry_type
		 FROM machine_capabilities mc
		 JOIN registry_entries re ON mc.entry_id = re.id
		 WHERE mc.machine_id = $1 AND mc.enabled = true`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var caps []MachineCapability
	for rows.Next() {
		var c MachineCapability
		if err := rows.Scan(&c.ID, &c.MachineID, &c.EntryID, &c.Mode, &c.Enabled,
			&c.ConfigOverrides, &c.CreatedAt, &c.UpdatedAt, &c.EntryName, &c.EntryType); err != nil {
			return nil, err
		}
		caps = append(caps, c)
	}
	return caps, rows.Err()
}

func (s *PostgresStore) EnableMachineCapability(ctx context.Context, machineID, entryID string, mode *string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO machine_capabilities (machine_id, entry_id, mode)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (machine_id, entry_id)
		 DO UPDATE SET enabled = true, mode = $3, updated_at = now()`,
		machineID, entryID, mode)
	return err
}

func (s *PostgresStore) UpdateMachineCapabilityOverrides(ctx context.Context, machineID, entryID string, overrides json.RawMessage) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machine_capabilities SET config_overrides = $3, updated_at = now()
		 WHERE machine_id = $1 AND entry_id = $2`,
		machineID, entryID, overrides)
	return err
}

func (s *PostgresStore) DisableMachineCapability(ctx context.Context, machineID, entryID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM machine_capabilities WHERE machine_id = $1 AND entry_id = $2`,
		machineID, entryID)
	return err
}

// ---- Machine Config ----

func (s *PostgresStore) GetMachineConfig(ctx context.Context, machineID string) (*MachineConfigRecord, error) {
	c := &MachineConfigRecord{}
	err := s.pool.QueryRow(ctx,
		`SELECT machine_id, identity_name, identity_avatar, platform_overrides,
		        assembled_config, config_version, updated_at
		 FROM machine_config WHERE machine_id = $1`, machineID,
	).Scan(&c.MachineID, &c.IdentityName, &c.IdentityAvatar, &c.PlatformOverrides,
		&c.AssembledConfig, &c.ConfigVersion, &c.UpdatedAt)
	if err != nil {
		// Return empty record with defaults if not found
		if errors.Is(err, pgx.ErrNoRows) {
			return &MachineConfigRecord{MachineID: machineID}, nil
		}
		return nil, err
	}
	return c, nil
}

func (s *PostgresStore) SetMachineIdentity(ctx context.Context, machineID string, name, avatar *string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO machine_config (machine_id, identity_name, identity_avatar)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (machine_id)
		 DO UPDATE SET identity_name = COALESCE($2, machine_config.identity_name),
		              identity_avatar = COALESCE($3, machine_config.identity_avatar),
		              updated_at = now()`,
		machineID, name, avatar)
	return err
}

func (s *PostgresStore) SetMachineAssembledConfig(ctx context.Context, machineID string, config json.RawMessage, version int) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO machine_config (machine_id, assembled_config, config_version)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (machine_id)
		 DO UPDATE SET assembled_config = $2, config_version = $3, updated_at = now()`,
		machineID, config, version)
	return err
}

func (s *PostgresStore) SetMachinePreferredModel(ctx context.Context, machineID, model string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO machine_config (machine_id, platform_overrides)
		 VALUES ($1, jsonb_build_object('preferred_model', $2::text))
		 ON CONFLICT (machine_id)
		 DO UPDATE SET platform_overrides = COALESCE(machine_config.platform_overrides, '{}'::jsonb) || jsonb_build_object('preferred_model', $2::text),
		              updated_at = now()`,
		machineID, model)
	return err
}

func (s *PostgresStore) SetMachineModelFallbacks(ctx context.Context, machineID string, fallbacks []string) error {
	data, err := json.Marshal(fallbacks)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO machine_config (machine_id, platform_overrides)
		 VALUES ($1, jsonb_build_object('model_fallbacks', $2::jsonb))
		 ON CONFLICT (machine_id)
		 DO UPDATE SET platform_overrides = COALESCE(machine_config.platform_overrides, '{}'::jsonb) || jsonb_build_object('model_fallbacks', $2::jsonb),
		              updated_at = now()`,
		machineID, string(data))
	return err
}

func (s *PostgresStore) GetMachinePreferredModel(ctx context.Context, machineID string) (string, error) {
	var model *string
	err := s.pool.QueryRow(ctx,
		`SELECT platform_overrides->>'preferred_model' FROM machine_config WHERE machine_id = $1`, machineID).Scan(&model)
	if err != nil || model == nil {
		return "", nil
	}
	return *model, nil
}

func (s *PostgresStore) UpdateMachinePlatformOverride(ctx context.Context, machineID, key, value string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO machine_config (machine_id, platform_overrides)
		 VALUES ($1, jsonb_build_object($2::text, $3::text))
		 ON CONFLICT (machine_id)
		 DO UPDATE SET platform_overrides = COALESCE(machine_config.platform_overrides, '{}'::jsonb) || jsonb_build_object($2::text, $3::text),
		              updated_at = now()`,
		machineID, key, value)
	return err
}

func (s *PostgresStore) DeleteMachinePlatformOverride(ctx context.Context, machineID, key string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machine_config
		 SET platform_overrides = platform_overrides - $2,
		     updated_at = now()
		 WHERE machine_id = $1`,
		machineID, key)
	return err
}

// ---- Custom Providers ----

func (s *PostgresStore) CreateCustomProvider(ctx context.Context, p *CustomProvider) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO custom_providers (account_id, name, upstream_host, scheme, auth_method, auth_header, path_prefix, allowed_hosts, is_llm)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT (account_id, name)
		 DO UPDATE SET upstream_host = EXCLUDED.upstream_host,
		              scheme = EXCLUDED.scheme,
		              auth_method = EXCLUDED.auth_method,
		              auth_header = EXCLUDED.auth_header,
		              path_prefix = EXCLUDED.path_prefix,
		              allowed_hosts = EXCLUDED.allowed_hosts,
		              is_llm = EXCLUDED.is_llm
		 RETURNING id, created_at`,
		p.AccountID, p.Name, p.UpstreamHost, p.Scheme, p.AuthMethod, p.AuthHeader, p.PathPrefix, p.AllowedHosts, p.IsLLM,
	).Scan(&p.ID, &p.CreatedAt)
}

func (s *PostgresStore) DeleteCustomProvider(ctx context.Context, accountID int, name string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM custom_providers WHERE account_id = $1 AND name = $2`, accountID, name)
	return err
}

func (s *PostgresStore) ListCustomProviders(ctx context.Context, accountID int) ([]CustomProvider, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, account_id, name, upstream_host, scheme, auth_method, auth_header, path_prefix, allowed_hosts, is_llm, created_at
		 FROM custom_providers WHERE account_id = $1 ORDER BY name`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var providers []CustomProvider
	for rows.Next() {
		var p CustomProvider
		if err := rows.Scan(&p.ID, &p.AccountID, &p.Name, &p.UpstreamHost, &p.Scheme,
			&p.AuthMethod, &p.AuthHeader, &p.PathPrefix, &p.AllowedHosts, &p.IsLLM, &p.CreatedAt); err != nil {
			return nil, err
		}
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

func (s *PostgresStore) GetCustomProvider(ctx context.Context, accountID int, name string) (*CustomProvider, error) {
	p := &CustomProvider{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, account_id, name, upstream_host, scheme, auth_method, auth_header, path_prefix, allowed_hosts, is_llm, created_at
		 FROM custom_providers WHERE account_id = $1 AND name = $2`,
		accountID, name,
	).Scan(&p.ID, &p.AccountID, &p.Name, &p.UpstreamHost, &p.Scheme,
		&p.AuthMethod, &p.AuthHeader, &p.PathPrefix, &p.AllowedHosts, &p.IsLLM, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ---- Machine Operations ----

func (s *PostgresStore) CreateMachineOperation(ctx context.Context, op *MachineOperation) error {
	err := s.pool.QueryRow(ctx,
		`INSERT INTO machine_operations (machine_id, kind, state, idempotency_key)
		 VALUES ($1, $2, 'in_progress', $3)
		 RETURNING id, created_at`,
		op.MachineID, op.Kind, op.IdempotencyKey,
	).Scan(&op.ID, &op.CreatedAt)
	if err != nil {
		// Check for unique constraint violation (concurrent operation)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("machine %s already has an active operation", op.MachineID)
		}
		return err
	}
	return nil
}

func (s *PostgresStore) GetActiveMachineOperation(ctx context.Context, machineID string) (*MachineOperation, error) {
	op := &MachineOperation{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, machine_id, kind, state, idempotency_key, error_code, error_message, created_at, completed_at
		 FROM machine_operations
		 WHERE machine_id = $1 AND state IN ('pending', 'in_progress')
		 ORDER BY created_at DESC LIMIT 1`,
		machineID,
	).Scan(&op.ID, &op.MachineID, &op.Kind, &op.State, &op.IdempotencyKey,
		&op.ErrorCode, &op.ErrorMessage, &op.CreatedAt, &op.CompletedAt)
	if err != nil {
		return nil, err
	}
	return op, nil
}

func (s *PostgresStore) CompleteMachineOperation(ctx context.Context, operationID, state string, errMsg *string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machine_operations SET state = $2, error_message = $3, completed_at = now()
		 WHERE id = $1`,
		operationID, state, errMsg)
	return err
}

func (s *PostgresStore) ExpireStaleOperations(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := time.Now().Add(-olderThan)
	tag, err := s.pool.Exec(ctx,
		`UPDATE machine_operations SET state = 'failed', error_message = 'operation expired (stale)', completed_at = now()
		 WHERE state IN ('pending', 'in_progress') AND created_at < $1`,
		cutoff)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ---- Workflow Projection ----

func (s *PostgresStore) CreateWorkflowRun(ctx context.Context, run *WorkflowRun) error {
	if run.Priority == "" {
		run.Priority = "normal"
	}
	if run.Status == "" {
		run.Status = "queued"
	}

	return s.pool.QueryRow(ctx,
		`INSERT INTO workflow_runs
		    (id, kind, scope_type, scope_id, status, current_phase, requested_by, account_id, priority, input_json, output_json, summary_json, error_code, error_message, started_at, completed_at)
		 VALUES
		    ($1, $2, $3, $4, $5, $6, $7, $8, $9, COALESCE($10, '{}'::jsonb), $11, $12, $13, $14, $15, $16)
		 RETURNING created_at, updated_at`,
		run.ID, run.Kind, run.ScopeType, run.ScopeID, run.Status, run.CurrentPhase, run.RequestedBy, run.AccountID,
		run.Priority, run.InputJSON, run.OutputJSON, run.SummaryJSON, run.ErrorCode, run.ErrorMessage, run.StartedAt, run.CompletedAt,
	).Scan(&run.CreatedAt, &run.UpdatedAt)
}

func scanWorkflowRun(row pgx.Row, run *WorkflowRun) error {
	return row.Scan(
		&run.ID,
		&run.Kind,
		&run.ScopeType,
		&run.ScopeID,
		&run.Status,
		&run.CurrentPhase,
		&run.RequestedBy,
		&run.AccountID,
		&run.Priority,
		&run.InputJSON,
		&run.OutputJSON,
		&run.SummaryJSON,
		&run.ErrorCode,
		&run.ErrorMessage,
		&run.StartedAt,
		&run.CompletedAt,
		&run.CreatedAt,
		&run.UpdatedAt,
	)
}

func (s *PostgresStore) GetWorkflowRun(ctx context.Context, id string) (*WorkflowRun, error) {
	run := &WorkflowRun{}
	err := scanWorkflowRun(s.pool.QueryRow(ctx,
		`SELECT id, kind, scope_type, scope_id, status, current_phase, requested_by, account_id, priority, input_json, output_json, summary_json, error_code, error_message, started_at, completed_at, created_at, updated_at
		 FROM workflow_runs
		 WHERE id = $1`,
		id,
	), run)
	if err != nil {
		return nil, err
	}
	return run, nil
}

func (s *PostgresStore) UpdateWorkflowRunStatus(ctx context.Context, id, status string, phase *string, summary json.RawMessage, errCode, errMsg *string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE workflow_runs
		 SET status = $2,
		     current_phase = $3,
		     summary_json = $4,
		     error_code = $5,
		     error_message = $6,
		     started_at = CASE
		        WHEN started_at IS NULL AND $2 <> 'queued' THEN now()
		        ELSE started_at
		     END,
		     updated_at = now()
		 WHERE id = $1`,
		id, status, phase, summary, errCode, errMsg,
	)
	return err
}

func (s *PostgresStore) CompleteWorkflowRun(ctx context.Context, id, status string, output json.RawMessage, errCode, errMsg *string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE workflow_runs
		 SET status = $2,
		     output_json = $3,
		     error_code = $4,
		     error_message = $5,
		     started_at = COALESCE(started_at, now()),
		     completed_at = now(),
		     updated_at = now()
		 WHERE id = $1`,
		id, status, output, errCode, errMsg,
	)
	return err
}

func (s *PostgresStore) ListWorkflowRunsByScope(ctx context.Context, scopeType, scopeID string, limit int) ([]WorkflowRun, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.pool.Query(ctx,
		`SELECT id, kind, scope_type, scope_id, status, current_phase, requested_by, account_id, priority, input_json, output_json, summary_json, error_code, error_message, started_at, completed_at, created_at, updated_at
		 FROM workflow_runs
		 WHERE scope_type = $1 AND scope_id = $2
		 ORDER BY created_at DESC
		 LIMIT $3`,
		scopeType, scopeID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	runs := make([]WorkflowRun, 0, limit)
	for rows.Next() {
		var run WorkflowRun
		if err := scanWorkflowRun(rows, &run); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *PostgresStore) ListAllWorkflowRuns(ctx context.Context, kind, status string, limit int) ([]WorkflowRun, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `SELECT id, kind, scope_type, scope_id, status, current_phase, requested_by, account_id, priority, input_json, output_json, summary_json, error_code, error_message, started_at, completed_at, created_at, updated_at
		 FROM workflow_runs
		 WHERE ($1 = '' OR kind = $1)
		   AND ($2 = '' OR status = $2)
		 ORDER BY created_at DESC
		 LIMIT $3`

	rows, err := s.pool.Query(ctx, query, kind, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	runs := make([]WorkflowRun, 0, limit)
	for rows.Next() {
		var run WorkflowRun
		if err := scanWorkflowRun(rows, &run); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *PostgresStore) CreateWorkflowEvent(ctx context.Context, event *WorkflowEvent) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO workflow_events (workflow_id, phase, level, event_type, message, details_json)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, created_at`,
		event.WorkflowID, event.Phase, event.Level, event.EventType, event.Message, event.DetailsJSON,
	).Scan(&event.ID, &event.CreatedAt)
}

func (s *PostgresStore) ListWorkflowEvents(ctx context.Context, workflowID string, limit int) ([]WorkflowEvent, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := s.pool.Query(ctx,
		`SELECT id, workflow_id, phase, level, event_type, message, details_json, created_at
		 FROM workflow_events
		 WHERE workflow_id = $1
		 ORDER BY created_at ASC
		 LIMIT $2`,
		workflowID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]WorkflowEvent, 0, limit)
	for rows.Next() {
		var event WorkflowEvent
		if err := rows.Scan(
			&event.ID,
			&event.WorkflowID,
			&event.Phase,
			&event.Level,
			&event.EventType,
			&event.Message,
			&event.DetailsJSON,
			&event.CreatedAt,
		); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *PostgresStore) AcquireWorkflowLock(ctx context.Context, lock *WorkflowLock) error {
	err := s.pool.QueryRow(ctx,
		`INSERT INTO workflow_locks (resource_type, resource_id, lock_kind, workflow_id, lease_expires_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (resource_type, resource_id, lock_kind) DO UPDATE
		 SET workflow_id = EXCLUDED.workflow_id,
		     lease_expires_at = EXCLUDED.lease_expires_at,
		     updated_at = now()
		 WHERE workflow_locks.workflow_id = EXCLUDED.workflow_id
		    OR workflow_locks.lease_expires_at < now()
		 RETURNING created_at, updated_at`,
		lock.ResourceType, lock.ResourceID, lock.LockKind, lock.WorkflowID, lock.LeaseExpiresAt,
	).Scan(&lock.CreatedAt, &lock.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrWorkflowLockHeld
	}
	return err
}

func (s *PostgresStore) RenewWorkflowLock(ctx context.Context, workflowID, resourceType, resourceID, lockKind string, leaseUntil time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE workflow_locks
		 SET lease_expires_at = $5,
		     updated_at = now()
		 WHERE workflow_id = $1 AND resource_type = $2 AND resource_id = $3 AND lock_kind = $4`,
		workflowID, resourceType, resourceID, lockKind, leaseUntil,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrWorkflowLockNotFound
	}
	return nil
}

func (s *PostgresStore) ReleaseWorkflowLocks(ctx context.Context, workflowID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM workflow_locks WHERE workflow_id = $1`,
		workflowID,
	)
	return err
}

func (s *PostgresStore) FindStalledWorkflows(ctx context.Context, staleThreshold time.Duration) ([]StalledWorkflow, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT wr.id, wr.kind, wr.scope_type, wr.scope_id,
		        COALESCE(MAX(we.created_at), wr.created_at) AS last_event_at
		 FROM workflow_runs wr
		 LEFT JOIN workflow_events we ON we.workflow_id = wr.id
		 WHERE wr.status IN ('running', 'waiting')
		 GROUP BY wr.id
		 HAVING COALESCE(MAX(we.created_at), wr.created_at) < now() - $1::interval`, fmt.Sprintf("%d seconds", int(staleThreshold.Seconds())))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stalled []StalledWorkflow
	for rows.Next() {
		var sw StalledWorkflow
		if err := rows.Scan(&sw.ID, &sw.Kind, &sw.ScopeType, &sw.ScopeID, &sw.LastEventAt); err != nil {
			return nil, err
		}
		stalled = append(stalled, sw)
	}
	return stalled, nil
}

// ---- Placement Records ----

func (s *PostgresStore) ReservePlacement(ctx context.Context, machineID string, hostID int, vmIP, operationID string) (*Placement, error) {
	p := &Placement{}
	var opID *string
	if operationID != "" {
		opID = &operationID
	}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO machine_placements (machine_id, host_id, vm_ip, state, created_by_operation_id)
		 VALUES ($1, $2, $3, 'reserved', $4)
		 RETURNING id, machine_id, host_id, vm_ip, state, reserved_at, activated_at, released_at, created_by_operation_id`,
		machineID, hostID, vmIP, opID,
	).Scan(&p.ID, &p.MachineID, &p.HostID, &p.VMIP, &p.State, &p.ReservedAt, &p.ActivatedAt, &p.ReleasedAt, &p.CreatedByOperationID)
	if err != nil {
		return nil, fmt.Errorf("reserve placement: %w", err)
	}
	return p, nil
}

func (s *PostgresStore) ActivatePlacement(ctx context.Context, placementID string) (bool, error) {
	result, err := s.pool.Exec(ctx,
		`UPDATE machine_placements SET state = 'active', activated_at = now()
		 WHERE id = $1 AND state = 'reserved'`, placementID)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() > 0, nil
}

func (s *PostgresStore) ReleasePlacement(ctx context.Context, machineID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machine_placements SET state = 'released', released_at = now()
		 WHERE machine_id = $1 AND released_at IS NULL`, machineID)
	return err
}

func (s *PostgresStore) ReleaseMigrationSource(ctx context.Context, machineID string, expectedHostID int, vcpus, memoryMB int, releaseCapacity bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var currentHostID *int
	if err := tx.QueryRow(ctx, `SELECT host_id FROM machines WHERE id = $1 FOR UPDATE`, machineID).Scan(&currentHostID); err != nil {
		return fmt.Errorf("lock machine: %w", err)
	}

	if currentHostID != nil && *currentHostID == expectedHostID {
		if releaseCapacity {
			if _, err := tx.Exec(ctx,
				`UPDATE hosts
				 SET used_vcpus = GREATEST(0, used_vcpus - $2),
				     used_memory_mb = GREATEST(0, used_memory_mb - $3)
				 WHERE id = $1`,
				expectedHostID, vcpus, memoryMB); err != nil {
				return fmt.Errorf("release host capacity: %w", err)
			}
		}

		if _, err := tx.Exec(ctx,
			`UPDATE machines
			 SET host_id = NULL,
			     vm_ip = NULL,
			     home_host_id = NULL,
			     status = 'stopped',
			     stopped_at = now()
			 WHERE id = $1`,
			machineID); err != nil {
			return fmt.Errorf("unassign machine from source host: %w", err)
		}
	} else if currentHostID == nil {
		if _, err := tx.Exec(ctx,
			`UPDATE machines SET home_host_id = NULL WHERE id = $1`,
			machineID); err != nil {
			return fmt.Errorf("clear home host: %w", err)
		}
	}

	if _, err := tx.Exec(ctx,
		`UPDATE machine_placements
		 SET state = 'released', released_at = now()
		 WHERE machine_id = $1 AND released_at IS NULL`,
		machineID); err != nil {
		return fmt.Errorf("release placement: %w", err)
	}

	return tx.Commit(ctx)
}

func (s *PostgresStore) GetActivePlacement(ctx context.Context, machineID string) (*Placement, error) {
	p := &Placement{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, machine_id, host_id, vm_ip, state, reserved_at, activated_at, released_at, created_by_operation_id
		 FROM machine_placements
		 WHERE machine_id = $1 AND released_at IS NULL
		 ORDER BY reserved_at DESC LIMIT 1`, machineID,
	).Scan(&p.ID, &p.MachineID, &p.HostID, &p.VMIP, &p.State, &p.ReservedAt, &p.ActivatedAt, &p.ReleasedAt, &p.CreatedByOperationID)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (s *PostgresStore) ListActivePlacementsByHost(ctx context.Context, hostID int) ([]Placement, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, machine_id, host_id, vm_ip, state, reserved_at, activated_at, released_at, created_by_operation_id
		 FROM machine_placements
		 WHERE host_id = $1 AND released_at IS NULL`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var placements []Placement
	for rows.Next() {
		var p Placement
		if err := rows.Scan(&p.ID, &p.MachineID, &p.HostID, &p.VMIP, &p.State, &p.ReservedAt, &p.ActivatedAt, &p.ReleasedAt, &p.CreatedByOperationID); err != nil {
			return nil, err
		}
		placements = append(placements, p)
	}
	return placements, rows.Err()
}

func (s *PostgresStore) ResolveCapacityPolicy(ctx context.Context, hostID int, hostPool string) (*CapacityPolicy, error) {
	// Try host-specific override first
	cp := &CapacityPolicy{}
	err := s.pool.QueryRow(ctx,
		`SELECT cp.id, cp.name, cp.scope_type, cp.scope_ref, cp.cpu_overcommit_ratio, cp.memory_overcommit_ratio,
		        cp.reserve_vcpus, cp.reserve_memory_mb, cp.max_machine_count, cp.enabled, cp.created_at, cp.updated_at
		 FROM capacity_policies cp
		 JOIN hosts h ON h.capacity_policy_id = cp.id
		 WHERE h.id = $1 AND cp.enabled = true`, hostID,
	).Scan(&cp.ID, &cp.Name, &cp.ScopeType, &cp.ScopeRef, &cp.CPUOvercommitRatio, &cp.MemoryOvercommitRatio,
		&cp.ReserveVCPUs, &cp.ReserveMemoryMB, &cp.MaxMachineCount, &cp.Enabled, &cp.CreatedAt, &cp.UpdatedAt)
	if err == nil {
		return cp, nil
	}

	// Try pool policy
	err = s.pool.QueryRow(ctx,
		`SELECT id, name, scope_type, scope_ref, cpu_overcommit_ratio, memory_overcommit_ratio,
		        reserve_vcpus, reserve_memory_mb, max_machine_count, enabled, created_at, updated_at
		 FROM capacity_policies
		 WHERE scope_type = 'host_pool' AND scope_ref = $1 AND enabled = true`, hostPool,
	).Scan(&cp.ID, &cp.Name, &cp.ScopeType, &cp.ScopeRef, &cp.CPUOvercommitRatio, &cp.MemoryOvercommitRatio,
		&cp.ReserveVCPUs, &cp.ReserveMemoryMB, &cp.MaxMachineCount, &cp.Enabled, &cp.CreatedAt, &cp.UpdatedAt)
	if err == nil {
		return cp, nil
	}

	// Fall back to global default
	err = s.pool.QueryRow(ctx,
		`SELECT id, name, scope_type, scope_ref, cpu_overcommit_ratio, memory_overcommit_ratio,
		        reserve_vcpus, reserve_memory_mb, max_machine_count, enabled, created_at, updated_at
		 FROM capacity_policies
		 WHERE scope_type = 'global' AND enabled = true
		 ORDER BY created_at ASC LIMIT 1`,
	).Scan(&cp.ID, &cp.Name, &cp.ScopeType, &cp.ScopeRef, &cp.CPUOvercommitRatio, &cp.MemoryOvercommitRatio,
		&cp.ReserveVCPUs, &cp.ReserveMemoryMB, &cp.MaxMachineCount, &cp.Enabled, &cp.CreatedAt, &cp.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("no capacity policy found (host=%d, pool=%s): %w", hostID, hostPool, err)
	}
	return cp, nil
}

func (s *PostgresStore) UpdateMachineHomeHost(ctx context.Context, machineID string, homeHostID *int) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machines SET home_host_id = $2 WHERE id = $1`, machineID, homeHostID)
	return err
}

// ---- Enrollment ----

func (s *PostgresStore) CreateEnrollmentToken(ctx context.Context, t *EnrollmentToken) error {
	// Generate token: ocm_enroll_ + 32 hex chars
	tokenBytes := make([]byte, 16)
	if _, err := cryptoRand.Read(tokenBytes); err != nil {
		return fmt.Errorf("generate token: %w", err)
	}
	t.Token = "ocm_enroll_" + fmt.Sprintf("%x", tokenBytes)

	return s.pool.QueryRow(ctx,
		`INSERT INTO host_enrollment_tokens (token, provider, provider_class, labels, expires_at)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, created_at`,
		t.Token, t.Provider, t.ProviderClass, t.Labels, t.ExpiresAt,
	).Scan(&t.ID, &t.CreatedAt)
}

func (s *PostgresStore) GetEnrollmentToken(ctx context.Context, token string) (*EnrollmentToken, error) {
	t := &EnrollmentToken{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, token, provider, provider_class, labels, used_by_host_id, expires_at, created_at
		 FROM host_enrollment_tokens WHERE token = $1`, token,
	).Scan(&t.ID, &t.Token, &t.Provider, &t.ProviderClass, &t.Labels, &t.UsedByHostID, &t.ExpiresAt, &t.CreatedAt)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (s *PostgresStore) ListEnrollmentTokens(ctx context.Context) ([]EnrollmentToken, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, token, provider, provider_class, labels, used_by_host_id, expires_at, created_at
		 FROM host_enrollment_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tokens := make([]EnrollmentToken, 0)
	for rows.Next() {
		var t EnrollmentToken
		if err := rows.Scan(&t.ID, &t.Token, &t.Provider, &t.ProviderClass, &t.Labels, &t.UsedByHostID, &t.ExpiresAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

func (s *PostgresStore) MarkEnrollmentTokenUsed(ctx context.Context, token string, hostID int) error {
	result, err := s.pool.Exec(ctx,
		`UPDATE host_enrollment_tokens SET used_by_host_id = $2
		 WHERE token = $1 AND used_by_host_id IS NULL AND expires_at > now()`,
		token, hostID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("enrollment token not found, already used, or expired")
	}
	return nil
}

func (s *PostgresStore) CreateRegisteredHost(ctx context.Context, h *Host) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO hosts (vm_name, zone, region, machine_type, status,
		        capacity_vcpus, capacity_memory_mb, capacity_disk_mb, source_image,
		        provider, provider_class, lifecycle_mode,
		        agent_endpoint, agent_endpoint_type, provider_host_id,
		        provider_metadata, capabilities, labels, agent_token)
		 VALUES ($1, $2, $3, $4, 'provisioning',
		        $5, $6, $7, $8,
		        $9, $10, $11,
		        $12, $13, $14,
		        $15, $16, $17, $18)
		 RETURNING id, created_at`,
		h.VMName, h.Zone, h.Region, h.MachineType,
		h.CapacityVCPUs, h.CapacityMemoryMB, h.CapacityDiskMB, h.SourceImage,
		h.Provider, h.ProviderClass, h.LifecycleMode,
		h.AgentEndpoint, h.AgentEndpointType, h.ProviderHostID,
		h.ProviderMetadata, h.Capabilities, h.Labels, h.AgentToken,
	).Scan(&h.ID, &h.CreatedAt)
}

func (s *PostgresStore) UpdateHostAgentEndpoint(ctx context.Context, hostID int, endpoint, endpointType string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE hosts SET agent_endpoint = $2, agent_endpoint_type = $3 WHERE id = $1`,
		hostID, endpoint, endpointType)
	return err
}

func (s *PostgresStore) UpdateHostProviderMetadata(ctx context.Context, hostID int, metadata json.RawMessage) error {
	_, err := s.pool.Exec(ctx, `UPDATE hosts SET provider_metadata = $2 WHERE id = $1`, hostID, metadata)
	return err
}

func (s *PostgresStore) GetHostByAgentToken(ctx context.Context, agentToken string) (*Host, error) {
	h := &Host{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, vm_name, vm_id, zone, region, machine_type, external_ip, internal_ip,
		        tunnel_url, status, status_message, source_image, capacity_vcpus, capacity_memory_mb, capacity_disk_mb,
		        used_vcpus, used_memory_mb, used_disk_mb, (SELECT COUNT(*) FROM machines WHERE machines.host_id = hosts.id AND machines.status = 'running') AS machine_count, agent_version, openclaw_version,
		        rootfs_version, browser_rootfs_version, last_heartbeat, created_at,
		        provider, provider_class, lifecycle_mode, agent_endpoint, agent_endpoint_type,
		        provider_host_id, provider_metadata, capabilities, labels, agent_token, maintenance_mode
		 FROM hosts WHERE agent_token = $1`, agentToken,
	).Scan(&h.ID, &h.VMName, &h.VMID, &h.Zone, &h.Region, &h.MachineType,
		&h.ExternalIP, &h.InternalIP, &h.TunnelURL, &h.Status, &h.StatusMessage, &h.SourceImage,
		&h.CapacityVCPUs, &h.CapacityMemoryMB, &h.CapacityDiskMB, &h.UsedVCPUs, &h.UsedMemoryMB, &h.UsedDiskMB,
		&h.MachineCount, &h.AgentVersion, &h.OpenclawVersion,
		&h.RootfsVersion, &h.BrowserRootfsVersion, &h.LastHeartbeat, &h.CreatedAt,
		&h.Provider, &h.ProviderClass, &h.LifecycleMode, &h.AgentEndpoint, &h.AgentEndpointType,
		&h.ProviderHostID, &h.ProviderMetadata, &h.Capabilities, &h.Labels, &h.AgentToken, &h.MaintenanceMode)
	if err != nil {
		return nil, err
	}
	return h, nil
}

// ---- Backups ----

func (s *PostgresStore) CreateBackupRecord(ctx context.Context, b *BackupRecord) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO machine_backups (machine_id, timestamp, gcs_path, size_bytes, compressed_bytes,
		 checksum_sha256, hmac_sha256, nonce, trigger, host_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING id, created_at`,
		b.MachineID, b.Timestamp, b.GCSPath, b.SizeBytes, b.CompressedBytes,
		b.ChecksumSHA256, b.HMACSHA256, b.Nonce, b.Trigger, b.HostID,
	).Scan(&b.ID, &b.CreatedAt)
}

func (s *PostgresStore) ListBackupRecords(ctx context.Context, machineID string) ([]BackupRecord, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, machine_id, timestamp, gcs_path, size_bytes, compressed_bytes,
		 checksum_sha256, hmac_sha256, nonce, trigger, host_id, created_at
		 FROM machine_backups WHERE machine_id = $1 ORDER BY timestamp DESC`,
		machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var backups []BackupRecord
	for rows.Next() {
		var b BackupRecord
		if err := rows.Scan(&b.ID, &b.MachineID, &b.Timestamp, &b.GCSPath,
			&b.SizeBytes, &b.CompressedBytes, &b.ChecksumSHA256, &b.HMACSHA256,
			&b.Nonce, &b.Trigger, &b.HostID, &b.CreatedAt); err != nil {
			return nil, err
		}
		backups = append(backups, b)
	}
	return backups, nil
}

func (s *PostgresStore) GetBackupRecord(ctx context.Context, id int) (*BackupRecord, error) {
	var b BackupRecord
	err := s.pool.QueryRow(ctx,
		`SELECT id, machine_id, timestamp, gcs_path, size_bytes, compressed_bytes,
		 checksum_sha256, hmac_sha256, nonce, trigger, host_id, created_at
		 FROM machine_backups WHERE id = $1`, id,
	).Scan(&b.ID, &b.MachineID, &b.Timestamp, &b.GCSPath,
		&b.SizeBytes, &b.CompressedBytes, &b.ChecksumSHA256, &b.HMACSHA256,
		&b.Nonce, &b.Trigger, &b.HostID, &b.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *PostgresStore) DeleteBackupRecord(ctx context.Context, id int) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM machine_backups WHERE id = $1`, id)
	return err
}

func (s *PostgresStore) DeleteAllBackupRecords(ctx context.Context, machineID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM machine_backups WHERE machine_id = $1`, machineID)
	return err
}

func (s *PostgresStore) EnableBackups(ctx context.Context, machineID string, backupKey []byte) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machines SET backups_enabled = true, backup_key = $2 WHERE id = $1`,
		machineID, backupKey)
	return err
}

// ListMachinesWithoutBackupKey returns IDs of machines that have no backup_key set.
func (s *PostgresStore) ListMachinesWithoutBackupKey(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM machines WHERE backup_key IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *PostgresStore) DisableBackups(ctx context.Context, machineID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machines SET backups_enabled = false WHERE id = $1`,
		machineID)
	return err
}

// --- MachineAgentRepo ---

func (s *PostgresStore) CreateMachineAgent(ctx context.Context, agent *MachineAgent) error {
	if agent.IsDefault {
		// Atomically demote + insert in a transaction
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if _, err := tx.Exec(ctx,
			`UPDATE machine_agents SET is_default = false, updated_at = now()
			 WHERE machine_id = $1 AND is_default = true`, agent.MachineID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO machine_agents (machine_id, agent_id, name, model, identity_emoji, identity_avatar, soul, is_default, sort_order)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			 RETURNING id, created_at, updated_at`,
			agent.MachineID, agent.AgentID, agent.Name, agent.Model,
			agent.IdentityEmoji, agent.IdentityAvatar, agent.Soul,
			agent.IsDefault, agent.SortOrder,
		).Scan(&agent.ID, &agent.CreatedAt, &agent.UpdatedAt); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	return s.pool.QueryRow(ctx,
		`INSERT INTO machine_agents (machine_id, agent_id, name, model, identity_emoji, identity_avatar, soul, is_default, sort_order)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING id, created_at, updated_at`,
		agent.MachineID, agent.AgentID, agent.Name, agent.Model,
		agent.IdentityEmoji, agent.IdentityAvatar, agent.Soul,
		agent.IsDefault, agent.SortOrder,
	).Scan(&agent.ID, &agent.CreatedAt, &agent.UpdatedAt)
}

func (s *PostgresStore) ListMachineAgents(ctx context.Context, machineID string) ([]MachineAgent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, machine_id, agent_id, name, model, identity_emoji, identity_avatar, soul,
		        is_default, sort_order, created_at, updated_at
		 FROM machine_agents WHERE machine_id = $1 ORDER BY sort_order, created_at`,
		machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []MachineAgent
	for rows.Next() {
		var a MachineAgent
		if err := rows.Scan(&a.ID, &a.MachineID, &a.AgentID, &a.Name, &a.Model,
			&a.IdentityEmoji, &a.IdentityAvatar, &a.Soul,
			&a.IsDefault, &a.SortOrder, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	if agents == nil {
		agents = []MachineAgent{}
	}
	return agents, rows.Err()
}

func (s *PostgresStore) GetMachineAgent(ctx context.Context, machineID, agentID string) (*MachineAgent, error) {
	var a MachineAgent
	err := s.pool.QueryRow(ctx,
		`SELECT id, machine_id, agent_id, name, model, identity_emoji, identity_avatar, soul,
		        is_default, sort_order, created_at, updated_at
		 FROM machine_agents WHERE machine_id = $1 AND agent_id = $2`,
		machineID, agentID,
	).Scan(&a.ID, &a.MachineID, &a.AgentID, &a.Name, &a.Model,
		&a.IdentityEmoji, &a.IdentityAvatar, &a.Soul,
		&a.IsDefault, &a.SortOrder, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *PostgresStore) UpdateMachineAgent(ctx context.Context, agent *MachineAgent) error {
	if agent.IsDefault {
		// Atomically demote others + update in a transaction
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if _, err := tx.Exec(ctx,
			`UPDATE machine_agents SET is_default = false, updated_at = now()
			 WHERE machine_id = $1 AND is_default = true AND agent_id != $2`,
			agent.MachineID, agent.AgentID); err != nil {
			return err
		}
		result, err := tx.Exec(ctx,
			`UPDATE machine_agents
			 SET name = $3, model = $4, identity_emoji = $5, identity_avatar = $6,
			     soul = $7, is_default = $8, sort_order = $9, updated_at = now()
			 WHERE machine_id = $1 AND agent_id = $2`,
			agent.MachineID, agent.AgentID, agent.Name, agent.Model,
			agent.IdentityEmoji, agent.IdentityAvatar, agent.Soul,
			agent.IsDefault, agent.SortOrder)
		if err != nil {
			return err
		}
		if result.RowsAffected() == 0 {
			return fmt.Errorf("agent not found")
		}
		return tx.Commit(ctx)
	}
	result, err := s.pool.Exec(ctx,
		`UPDATE machine_agents
		 SET name = $3, model = $4, identity_emoji = $5, identity_avatar = $6,
		     soul = $7, is_default = $8, sort_order = $9, updated_at = now()
		 WHERE machine_id = $1 AND agent_id = $2`,
		agent.MachineID, agent.AgentID, agent.Name, agent.Model,
		agent.IdentityEmoji, agent.IdentityAvatar, agent.Soul,
		agent.IsDefault, agent.SortOrder)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("agent not found")
	}
	return nil
}

func (s *PostgresStore) DemoteDefaultAgents(ctx context.Context, machineID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machine_agents SET is_default = false, updated_at = now()
		 WHERE machine_id = $1 AND is_default = true`, machineID)
	return err
}

func (s *PostgresStore) DeleteMachineAgent(ctx context.Context, machineID, agentID string) error {
	result, err := s.pool.Exec(ctx,
		`DELETE FROM machine_agents WHERE machine_id = $1 AND agent_id = $2`,
		machineID, agentID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("agent not found")
	}
	return nil
}

// ---- Plugin Catalog ----

func (s *PostgresStore) ListPluginCatalog(ctx context.Context) ([]PluginCatalogEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, description, slot, version, install_kind, artifact_url,
		        artifact_sha256, config_template, status, sort_order, created_at, updated_at
		 FROM plugin_catalog
		 ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []PluginCatalogEntry
	for rows.Next() {
		var e PluginCatalogEntry
		if err := rows.Scan(&e.ID, &e.Name, &e.Description, &e.Slot, &e.Version,
			&e.InstallKind, &e.ArtifactURL, &e.ArtifactSHA256, &e.ConfigTemplate,
			&e.Status, &e.SortOrder, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *PostgresStore) GetPluginCatalogEntry(ctx context.Context, id string) (*PluginCatalogEntry, error) {
	var e PluginCatalogEntry
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, description, slot, version, install_kind, artifact_url,
		        artifact_sha256, config_template, status, sort_order, created_at, updated_at
		 FROM plugin_catalog WHERE id = $1`, id).
		Scan(&e.ID, &e.Name, &e.Description, &e.Slot, &e.Version,
			&e.InstallKind, &e.ArtifactURL, &e.ArtifactSHA256, &e.ConfigTemplate,
			&e.Status, &e.SortOrder, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPluginNotFound
		}
		return nil, err
	}
	return &e, nil
}

func (s *PostgresStore) CreatePluginCatalogEntry(ctx context.Context, entry *PluginCatalogEntry) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO plugin_catalog (id, name, description, slot, version, install_kind,
		        artifact_url, artifact_sha256, config_template, status, sort_order)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 RETURNING created_at, updated_at`,
		entry.ID, entry.Name, entry.Description, entry.Slot, entry.Version,
		entry.InstallKind, entry.ArtifactURL, entry.ArtifactSHA256,
		entry.ConfigTemplate, entry.Status, entry.SortOrder,
	).Scan(&entry.CreatedAt, &entry.UpdatedAt)
}

func (s *PostgresStore) UpdatePluginCatalogEntry(ctx context.Context, entry *PluginCatalogEntry) error {
	result, err := s.pool.Exec(ctx,
		`UPDATE plugin_catalog
		 SET name = $2, description = $3, slot = $4, version = $5, install_kind = $6,
		     artifact_url = $7, artifact_sha256 = $8, config_template = $9,
		     status = $10, sort_order = $11, updated_at = NOW()
		 WHERE id = $1`,
		entry.ID, entry.Name, entry.Description, entry.Slot, entry.Version,
		entry.InstallKind, entry.ArtifactURL, entry.ArtifactSHA256,
		entry.ConfigTemplate, entry.Status, entry.SortOrder)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrPluginNotFound
	}
	return nil
}

func (s *PostgresStore) DeletePluginCatalogEntry(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var enabledCount int
	err = tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM machine_plugins WHERE plugin_id = $1 AND enabled = true`, id).
		Scan(&enabledCount)
	if err != nil {
		return err
	}
	if enabledCount > 0 {
		return fmt.Errorf("%w: %d machine(s)", ErrPluginStillEnabled, enabledCount)
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM machine_plugins WHERE plugin_id = $1 AND enabled = false`, id); err != nil {
		return err
	}

	result, err := tx.Exec(ctx, `DELETE FROM plugin_catalog WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrPluginNotFound
	}

	return tx.Commit(ctx)
}

// ---- Machine Plugins ----

func (s *PostgresStore) ListMachinePlugins(ctx context.Context, machineID string) ([]MachinePlugin, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT mp.id, mp.machine_id, mp.plugin_id, mp.slot, mp.enabled,
		        mp.config_overrides, mp.install_status, mp.installed_version,
		        mp.installed_at, mp.created_at, mp.updated_at
		 FROM machine_plugins mp
		 WHERE mp.machine_id = $1
		 ORDER BY mp.slot, mp.created_at`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var plugins []MachinePlugin
	for rows.Next() {
		var p MachinePlugin
		if err := rows.Scan(&p.ID, &p.MachineID, &p.PluginID, &p.Slot, &p.Enabled,
			&p.ConfigOverrides, &p.InstallStatus, &p.InstalledVersion,
			&p.InstalledAt, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		plugins = append(plugins, p)
	}
	return plugins, rows.Err()
}

func (s *PostgresStore) ListMachinePluginsWithCatalog(ctx context.Context, machineID string) ([]MachinePluginWithCatalog, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT mp.id, mp.machine_id, mp.plugin_id, mp.slot, mp.enabled,
		        mp.config_overrides, mp.install_status, mp.installed_version,
		        mp.installed_at, mp.created_at, mp.updated_at,
		        pc.install_kind, pc.artifact_url, pc.version
		 FROM machine_plugins mp
		 JOIN plugin_catalog pc ON pc.id = mp.plugin_id
		 WHERE mp.machine_id = $1
		 ORDER BY mp.slot, mp.created_at`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var plugins []MachinePluginWithCatalog
	for rows.Next() {
		var p MachinePluginWithCatalog
		if err := rows.Scan(&p.ID, &p.MachineID, &p.PluginID, &p.Slot, &p.Enabled,
			&p.ConfigOverrides, &p.InstallStatus, &p.InstalledVersion,
			&p.InstalledAt, &p.CreatedAt, &p.UpdatedAt,
			&p.InstallKind, &p.ArtifactURL, &p.CatalogVersion); err != nil {
			return nil, err
		}
		plugins = append(plugins, p)
	}
	return plugins, rows.Err()
}

func (s *PostgresStore) EnableMachinePlugin(ctx context.Context, machineID, pluginID string, overrides json.RawMessage) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// 1. Look up catalog entry
	var slot, installKind, catalogVersion string
	err = tx.QueryRow(ctx,
		`SELECT slot, install_kind, version FROM plugin_catalog WHERE id = $1 AND status = 'active'`,
		pluginID).Scan(&slot, &installKind, &catalogVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPluginNotFound
		}
		return fmt.Errorf("looking up plugin catalog: %w", err)
	}

	// 2. Disable other enabled plugins in the same slot
	if _, err := tx.Exec(ctx,
		`UPDATE machine_plugins SET enabled = false, updated_at = NOW()
		 WHERE machine_id = $1 AND slot = $2 AND enabled = true AND plugin_id != $3`,
		machineID, slot, pluginID); err != nil {
		return err
	}

	// 4. Upsert: insert or re-enable with reset install state
	installStatus := "pending"
	var installedVersion *string
	if installKind == "bundled" {
		installStatus = "installed"
		installedVersion = &catalogVersion
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO machine_plugins (machine_id, plugin_id, slot, enabled, config_overrides,
		        install_status, installed_version, installed_at)
		 VALUES ($1, $2, $3, true, $4, $5, $6, CASE WHEN $5 = 'installed' THEN NOW() ELSE NULL END)
		 ON CONFLICT (machine_id, plugin_id) DO UPDATE SET
		     enabled = true,
		     config_overrides = $4,
		     install_status = $5,
		     installed_version = $6,
		     installed_at = CASE WHEN $5 = 'installed' THEN NOW() ELSE NULL END,
		     updated_at = NOW()`,
		machineID, pluginID, slot, overrides, installStatus, installedVersion)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (s *PostgresStore) DisableMachinePlugin(ctx context.Context, machineID, pluginID string) error {
	result, err := s.pool.Exec(ctx,
		`UPDATE machine_plugins SET enabled = false, updated_at = NOW()
		 WHERE machine_id = $1 AND plugin_id = $2 AND enabled = true`,
		machineID, pluginID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrMachinePluginNotFound
	}
	return nil
}

func (s *PostgresStore) UpdateMachinePluginOverrides(ctx context.Context, machineID, pluginID string, overrides json.RawMessage) error {
	result, err := s.pool.Exec(ctx,
		`UPDATE machine_plugins SET config_overrides = $3, updated_at = NOW()
		 WHERE machine_id = $1 AND plugin_id = $2 AND enabled = true`,
		machineID, pluginID, overrides)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrMachinePluginNotFound
	}
	return nil
}

func (s *PostgresStore) UpdateMachinePluginStatus(ctx context.Context, machineID, pluginID, status, version string) error {
	result, err := s.pool.Exec(ctx,
		`UPDATE machine_plugins
		 SET install_status = $3, installed_version = $4, installed_at = CASE WHEN $3 = 'installed' THEN NOW() ELSE installed_at END, updated_at = NOW()
		 WHERE machine_id = $1 AND plugin_id = $2`,
		machineID, pluginID, status, version)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrMachinePluginNotFound
	}
	return nil
}

func (s *PostgresStore) RecordTokenUsage(ctx context.Context, record *TokenUsageRecord) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO token_usage (account_id, machine_id, provider, model, input_tokens, output_tokens, source)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		record.AccountID, record.MachineID, record.Provider, record.Model,
		record.InputTokens, record.OutputTokens, record.Source)
	return err
}

func (s *PostgresStore) ListModelCatalog(ctx context.Context) ([]ModelCatalogEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, label, description, source, tier,
		        gateway_model_id, provider, enabled, sort_order, created_at
		 FROM model_catalog
		 WHERE enabled = true
		 ORDER BY sort_order`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []ModelCatalogEntry
	for rows.Next() {
		var e ModelCatalogEntry
		if err := rows.Scan(&e.ID, &e.Label, &e.Description, &e.Source, &e.Tier,
			&e.GatewayModelID, &e.Provider, &e.Enabled, &e.SortOrder, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *PostgresStore) GetModelCatalogEntry(ctx context.Context, modelID string) (*ModelCatalogEntry, error) {
	var e ModelCatalogEntry
	err := s.pool.QueryRow(ctx,
		`SELECT id, label, description, source, tier,
		        gateway_model_id, provider, enabled, sort_order, created_at
		 FROM model_catalog
		 WHERE id = $1 AND enabled = true`, modelID).
		Scan(&e.ID, &e.Label, &e.Description, &e.Source, &e.Tier,
			&e.GatewayModelID, &e.Provider, &e.Enabled, &e.SortOrder, &e.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

// ---- Opik ----

func (s *PostgresStore) GetMachineByGatewayToken(ctx context.Context, token string) (*Machine, error) {
	return scanMachine(s.pool.QueryRow(ctx,
		`SELECT `+machineColumns+` FROM machines WHERE gateway_token = $1`, token).Scan)
}

func (s *PostgresStore) GetOrCreateOpikProject(ctx context.Context, accountID int, name string) (*OpikProject, error) {
	p := &OpikProject{}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO opik_projects (account_id, name)
		 VALUES ($1, $2)
		 ON CONFLICT (account_id, name) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id, name, account_id, description, created_at`,
		accountID, name).Scan(&p.ID, &p.Name, &p.AccountID, &p.Description, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (s *PostgresStore) GetOpikProjectByName(ctx context.Context, accountID int, name string) (*OpikProject, error) {
	p := &OpikProject{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, account_id, description, created_at
		 FROM opik_projects WHERE account_id = $1 AND name = $2`,
		accountID, name).Scan(&p.ID, &p.Name, &p.AccountID, &p.Description, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (s *PostgresStore) ListOpikProjects(ctx context.Context, accountID int) ([]OpikProject, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, account_id, description, created_at
		 FROM opik_projects WHERE account_id = $1 ORDER BY created_at DESC`,
		accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []OpikProject
	for rows.Next() {
		var p OpikProject
		if err := rows.Scan(&p.ID, &p.Name, &p.AccountID, &p.Description, &p.CreatedAt); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

type opikTraceScanner interface {
	Scan(dest ...any) error
}

func scanOpikTraceListItem(row opikTraceScanner) (OpikTraceListItem, error) {
	var t OpikTraceListItem
	err := row.Scan(
		&t.ID, &t.ProjectID, &t.ProjectName, &t.MachineID, &t.MachineName,
		&t.Name, &t.ThreadID,
		&t.StartTime, &t.EndTime, &t.Input, &t.Output, &t.Metadata,
		&t.Tags, &t.ErrorInfo, &t.Source, &t.CreatedAt, &t.LastUpdatedAt,
		&t.SpanCount, &t.ErrorCount, &t.DurationMS, &t.TotalTokens, &t.TotalEstimatedCost,
		&t.FeedbackCount, &t.AvgFeedbackScore,
	)
	return t, err
}

func normalizeOpikTag(raw string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(raw))), "-")
}

func normalizeOpikTags(rawTags []string) []string {
	if rawTags == nil {
		return nil
	}
	tags := make([]string, 0, len(rawTags))
	seen := map[string]struct{}{}
	for _, raw := range rawTags {
		tag := normalizeOpikTag(raw)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	return tags
}

const opikTraceListSelectSQL = `SELECT t.id::text, t.project_id::text, p.name,
        t.machine_id, COALESCE(m.name, ''),
        t.name, t.thread_id,
        t.start_time, t.end_time, t.input, t.output, t.metadata,
        COALESCE(t.tags, ARRAY[]::text[]), t.error_info, t.source,
        t.created_at, t.last_updated_at,
        COUNT(s.id)::int AS span_count,
        (CASE WHEN t.error_info IS NOT NULL THEN 1 ELSE 0 END
          + COUNT(s.id) FILTER (WHERE s.error_info IS NOT NULL))::int AS error_count,
        GREATEST(EXTRACT(EPOCH FROM (COALESCE(t.end_time, NOW()) - t.start_time)) * 1000, 0)::bigint AS duration_ms,
        COALESCE(SUM(COALESCE(s.total_tokens, 0)), 0)::int AS total_tokens,
        COALESCE(SUM(COALESCE(s.total_estimated_cost, 0)), 0)::double precision AS total_estimated_cost,
        (SELECT COUNT(*) FROM opik_feedback_scores f WHERE f.account_id = t.account_id AND f.trace_id = t.id)::int AS feedback_count,
        (SELECT AVG(f.value)::double precision FROM opik_feedback_scores f WHERE f.account_id = t.account_id AND f.trace_id = t.id) AS avg_feedback_score`

const opikTraceListFromSQL = `
	 FROM opik_traces t
	 JOIN opik_projects p ON p.id = t.project_id
	 LEFT JOIN machines m ON m.id::text = t.machine_id AND m.account_id = t.account_id
	 LEFT JOIN opik_spans s ON s.trace_id = t.id AND s.account_id = $1`

const opikTraceListGroupSQL = `
	 GROUP BY t.id, t.project_id, p.name, t.machine_id, m.name, t.name, t.thread_id,
	          t.start_time, t.end_time, t.input, t.output, t.metadata,
	          t.tags, t.error_info, t.source, t.created_at, t.last_updated_at`

const listOpikTracesByMachineSQL = opikTraceListSelectSQL + opikTraceListFromSQL + `
	 WHERE t.account_id = $1
	   AND t.start_time >= $3
	   AND (
	     t.machine_id = $2
	     OR EXISTS (
	       SELECT 1 FROM opik_spans touched
	       WHERE touched.trace_id = t.id
	         AND touched.account_id = $1
	         AND touched.machine_id = $2
	     )
	   )` + opikTraceListGroupSQL + `
	 ORDER BY t.start_time DESC
	 LIMIT $4`

const getOpikTraceDetailSQL = opikTraceListSelectSQL + opikTraceListFromSQL + `
	 WHERE t.account_id = $1
	   AND t.id = $3
	   AND (
	     t.machine_id = $2
	     OR EXISTS (
	       SELECT 1 FROM opik_spans touched
	       WHERE touched.trace_id = t.id
	         AND touched.account_id = $1
	         AND touched.machine_id = $2
	     )
	   )` + opikTraceListGroupSQL

const getOpikTraceDetailByAccountSQL = opikTraceListSelectSQL + opikTraceListFromSQL + `
	 WHERE t.account_id = $1
	   AND t.id = $2` + opikTraceListGroupSQL

const listOpikTraceSpansSQL = `SELECT s.id::text, s.trace_id::text, s.machine_id, COALESCE(m.name, ''),
        s.parent_span_id::text,
        s.name, s.type, s.start_time, s.end_time, s.input, s.output, s.metadata,
        s.model, s.provider, COALESCE(s.tags, ARRAY[]::text[]), s.usage,
        COALESCE(s.prompt_tokens, 0)::int,
        COALESCE(s.completion_tokens, 0)::int,
        COALESCE(s.total_tokens, 0)::int,
        s.error_info,
        COALESCE(s.total_estimated_cost, 0)::double precision,
        s.duration, s.ttft, s.source, s.created_at, s.last_updated_at
	 FROM opik_spans s
	 LEFT JOIN machines m ON m.id::text = s.machine_id AND m.account_id = s.account_id
	 WHERE s.account_id = $1 AND s.trace_id = $2
	 ORDER BY s.start_time ASC, s.created_at ASC`

const listOpikFeedbackScoresSQL = `SELECT id::text, account_id, trace_id::text, span_id::text,
        name, value, reason, source, created_by, created_at
	 FROM opik_feedback_scores
	 WHERE account_id = $1 AND trace_id = $2
	 ORDER BY created_at DESC`

const createOpikFeedbackScoreSQL = `WITH scoped_trace AS (
        SELECT t.id AS trace_id, NULLIF($3, '')::uuid AS span_id
        FROM opik_traces t
        WHERE t.account_id = $1
          AND t.id = $2
          AND (
            NULLIF($3, '') IS NULL
            OR EXISTS (
              SELECT 1 FROM opik_spans s
              WHERE s.account_id = $1
                AND s.trace_id = t.id
                AND s.id = NULLIF($3, '')::uuid
            )
          )
      )
      INSERT INTO opik_feedback_scores (account_id, trace_id, span_id, name, value, reason, source, created_by)
      SELECT $1, scoped_trace.trace_id, scoped_trace.span_id, $4, $5, NULLIF($6, ''), $7, $8
      FROM scoped_trace
      RETURNING id::text, account_id, trace_id::text, span_id::text,
                name, value, reason, source, created_by, created_at`

const updateOpikTraceTagsSQL = `UPDATE opik_traces
	 SET tags = COALESCE($3::text[], ARRAY[]::text[]),
	     last_updated_at = NOW()
	 WHERE account_id = $1 AND id = $2`

func scanOpikFeedbackScore(row opikTraceScanner) (OpikFeedbackScore, error) {
	var score OpikFeedbackScore
	err := row.Scan(
		&score.ID, &score.AccountID, &score.TraceID, &score.SpanID,
		&score.Name, &score.Value, &score.Reason, &score.Source,
		&score.CreatedBy, &score.CreatedAt,
	)
	return score, err
}

func (s *PostgresStore) ListOpikTraces(ctx context.Context, accountID int, filters OpikTraceSearchFilters) ([]OpikTraceListItem, error) {
	if filters.Limit <= 0 {
		filters.Limit = 50
	}
	if filters.Since.IsZero() {
		filters.Since = time.Now().UTC().Add(-7 * 24 * time.Hour)
	}

	args := []any{accountID, filters.Since}
	nextArg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	var b strings.Builder
	b.WriteString(opikTraceListSelectSQL)
	b.WriteString(opikTraceListFromSQL)
	b.WriteString(`
	 WHERE t.account_id = $1
	   AND t.start_time >= $2`)

	if filters.MachineID != "" {
		arg := nextArg(filters.MachineID)
		b.WriteString(`
	   AND (
	     t.machine_id = ` + arg + `
	     OR EXISTS (
	       SELECT 1 FROM opik_spans touched
	       WHERE touched.trace_id = t.id
	         AND touched.account_id = $1
	         AND touched.machine_id = ` + arg + `
	     )
	   )`)
	}

	if filters.Project != "" {
		arg := nextArg(filters.Project)
		b.WriteString(`
	   AND (p.name = ` + arg + ` OR t.project_id::text = ` + arg + `)`)
	}

	if filters.ThreadID != "" {
		arg := nextArg(filters.ThreadID)
		b.WriteString(`
	   AND t.thread_id = ` + arg)
	}

	switch filters.Status {
	case "error":
		b.WriteString(`
	   AND (
	     t.error_info IS NOT NULL
	     OR EXISTS (
	       SELECT 1 FROM opik_spans err_span
	       WHERE err_span.trace_id = t.id
	         AND err_span.account_id = $1
	         AND err_span.error_info IS NOT NULL
	     )
	   )`)
	case "ok":
		b.WriteString(`
	   AND t.error_info IS NULL
	   AND NOT EXISTS (
	     SELECT 1 FROM opik_spans err_span
	     WHERE err_span.trace_id = t.id
	       AND err_span.account_id = $1
	       AND err_span.error_info IS NOT NULL
	   )`)
	}

	if filters.Query != "" {
		arg := nextArg("%" + filters.Query + "%")
		b.WriteString(`
	   AND (
	     t.id::text ILIKE ` + arg + `
	     OR t.name ILIKE ` + arg + `
	     OR t.thread_id ILIKE ` + arg + `
	     OR p.name ILIKE ` + arg + `
	     OR t.machine_id ILIKE ` + arg + `
	     OR m.name ILIKE ` + arg + `
	     OR array_to_string(COALESCE(t.tags, ARRAY[]::text[]), ' ') ILIKE ` + arg + `
	     OR t.input::text ILIKE ` + arg + `
	     OR t.output::text ILIKE ` + arg + `
	     OR t.metadata::text ILIKE ` + arg + `
	     OR EXISTS (
	       SELECT 1 FROM opik_spans search_span
	       WHERE search_span.trace_id = t.id
	         AND search_span.account_id = $1
	         AND (
	           search_span.name ILIKE ` + arg + `
	           OR search_span.type ILIKE ` + arg + `
	           OR search_span.model ILIKE ` + arg + `
	           OR search_span.provider ILIKE ` + arg + `
	           OR array_to_string(COALESCE(search_span.tags, ARRAY[]::text[]), ' ') ILIKE ` + arg + `
	           OR search_span.input::text ILIKE ` + arg + `
	           OR search_span.output::text ILIKE ` + arg + `
	           OR search_span.metadata::text ILIKE ` + arg + `
	         )
	     )
	   )`)
	}

	if tags := normalizeOpikTags(filters.Tags); len(tags) > 0 {
		arg := nextArg(tags)
		b.WriteString(`
	   AND (
	     t.tags @> ` + arg + `::text[]
	     OR EXISTS (
	       SELECT 1 FROM opik_spans tag_span
	       WHERE tag_span.trace_id = t.id
	         AND tag_span.account_id = $1
	         AND tag_span.tags @> ` + arg + `::text[]
	     )
	   )`)
	}

	switch filters.Feedback {
	case "unreviewed":
		b.WriteString(`
	   AND NOT EXISTS (
	     SELECT 1 FROM opik_feedback_scores feedback
	     WHERE feedback.account_id = $1
	       AND feedback.trace_id = t.id
	   )`)
	case "reviewed":
		b.WriteString(`
	   AND EXISTS (
	     SELECT 1 FROM opik_feedback_scores feedback
	     WHERE feedback.account_id = $1
	       AND feedback.trace_id = t.id
	   )`)
	case "low":
		threshold := 0.5
		if filters.MaxFeedback != nil {
			threshold = *filters.MaxFeedback
		}
		arg := nextArg(threshold)
		b.WriteString(`
	   AND EXISTS (
	     SELECT 1 FROM opik_feedback_scores feedback
	     WHERE feedback.account_id = $1
	       AND feedback.trace_id = t.id
	   )
	   AND (
	     SELECT AVG(feedback.value)::double precision
	     FROM opik_feedback_scores feedback
	     WHERE feedback.account_id = $1
	       AND feedback.trace_id = t.id
	   ) <= ` + arg)
	default:
		if filters.MaxFeedback != nil {
			arg := nextArg(*filters.MaxFeedback)
			b.WriteString(`
	   AND EXISTS (
	     SELECT 1 FROM opik_feedback_scores feedback
	     WHERE feedback.account_id = $1
	       AND feedback.trace_id = t.id
	   )
	   AND (
	     SELECT AVG(feedback.value)::double precision
	     FROM opik_feedback_scores feedback
	     WHERE feedback.account_id = $1
	       AND feedback.trace_id = t.id
	   ) <= ` + arg)
		}
	}

	b.WriteString(opikTraceListGroupSQL)

	var having []string
	if filters.MinTokens != nil {
		arg := nextArg(*filters.MinTokens)
		having = append(having, `COALESCE(SUM(COALESCE(s.total_tokens, 0)), 0) >= `+arg)
	}
	if filters.MinDurationMS != nil {
		arg := nextArg(*filters.MinDurationMS)
		having = append(having, `GREATEST(EXTRACT(EPOCH FROM (COALESCE(t.end_time, NOW()) - t.start_time)) * 1000, 0)::bigint >= `+arg)
	}
	if len(having) > 0 {
		b.WriteString(`
	 HAVING `)
		b.WriteString(strings.Join(having, `
	    AND `))
	}

	limitArg := nextArg(filters.Limit)
	b.WriteString(`
	 ORDER BY t.start_time DESC
	 LIMIT ` + limitArg)

	rows, err := s.pool.Query(ctx, b.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	traces := []OpikTraceListItem{}
	for rows.Next() {
		t, err := scanOpikTraceListItem(rows)
		if err != nil {
			return nil, err
		}
		traces = append(traces, t)
	}
	return traces, rows.Err()
}

func (s *PostgresStore) ListOpikTracesByMachine(ctx context.Context, accountID int, machineID string, since time.Time, limit int) ([]OpikTraceListItem, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, listOpikTracesByMachineSQL, accountID, machineID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	traces := []OpikTraceListItem{}
	for rows.Next() {
		t, err := scanOpikTraceListItem(rows)
		if err != nil {
			return nil, err
		}
		traces = append(traces, t)
	}
	return traces, rows.Err()
}

func (s *PostgresStore) listOpikTraceSpans(ctx context.Context, accountID int, traceID string) ([]OpikTraceSpan, error) {
	rows, err := s.pool.Query(ctx, listOpikTraceSpansSQL, accountID, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	spans := []OpikTraceSpan{}
	for rows.Next() {
		var sp OpikTraceSpan
		if err := rows.Scan(
			&sp.ID, &sp.TraceID, &sp.MachineID, &sp.MachineName, &sp.ParentSpanID,
			&sp.Name, &sp.Type, &sp.StartTime, &sp.EndTime,
			&sp.Input, &sp.Output, &sp.Metadata, &sp.Model, &sp.Provider,
			&sp.Tags, &sp.Usage, &sp.PromptTokens, &sp.CompletionTokens,
			&sp.TotalTokens, &sp.ErrorInfo, &sp.TotalEstimatedCost,
			&sp.Duration, &sp.TTFT, &sp.Source, &sp.CreatedAt, &sp.LastUpdatedAt,
		); err != nil {
			return nil, err
		}
		spans = append(spans, sp)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return spans, nil
}

func (s *PostgresStore) ListOpikFeedbackScores(ctx context.Context, accountID int, traceID string) ([]OpikFeedbackScore, error) {
	rows, err := s.pool.Query(ctx, listOpikFeedbackScoresSQL, accountID, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	scores := []OpikFeedbackScore{}
	for rows.Next() {
		score, err := scanOpikFeedbackScore(rows)
		if err != nil {
			return nil, err
		}
		scores = append(scores, score)
	}
	return scores, rows.Err()
}

func (s *PostgresStore) CreateOpikFeedbackScore(ctx context.Context, score *OpikFeedbackScore) (*OpikFeedbackScore, error) {
	if score.Source == "" {
		score.Source = "human"
	}
	spanID := ""
	if score.SpanID != nil {
		spanID = *score.SpanID
	}
	reason := ""
	if score.Reason != nil {
		reason = *score.Reason
	}

	created, err := scanOpikFeedbackScore(s.pool.QueryRow(
		ctx,
		createOpikFeedbackScoreSQL,
		score.AccountID,
		score.TraceID,
		spanID,
		score.Name,
		score.Value,
		reason,
		score.Source,
		score.CreatedBy,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrOpikRecordNotFound
		}
		return nil, err
	}
	return &created, nil
}

func (s *PostgresStore) UpdateOpikTraceTags(ctx context.Context, accountID int, traceID string, tags []string) error {
	tags = normalizeOpikTags(tags)
	result, err := s.pool.Exec(ctx, updateOpikTraceTagsSQL, accountID, traceID, tags)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrOpikRecordNotFound
	}
	return nil
}

func (s *PostgresStore) GetOpikTraceDetailByAccount(ctx context.Context, accountID int, traceID string) (*OpikTraceDetail, error) {
	trace, err := scanOpikTraceListItem(s.pool.QueryRow(ctx, getOpikTraceDetailByAccountSQL, accountID, traceID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrOpikRecordNotFound
		}
		return nil, err
	}

	spans, err := s.listOpikTraceSpans(ctx, accountID, traceID)
	if err != nil {
		return nil, err
	}
	feedback, err := s.ListOpikFeedbackScores(ctx, accountID, traceID)
	if err != nil {
		return nil, err
	}
	return &OpikTraceDetail{Trace: trace, Spans: spans, Feedback: feedback}, nil
}

func (s *PostgresStore) GetOpikTraceDetail(ctx context.Context, accountID int, machineID, traceID string) (*OpikTraceDetail, error) {
	trace, err := scanOpikTraceListItem(s.pool.QueryRow(ctx, getOpikTraceDetailSQL, accountID, machineID, traceID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrOpikRecordNotFound
		}
		return nil, err
	}

	spans, err := s.listOpikTraceSpans(ctx, accountID, traceID)
	if err != nil {
		return nil, err
	}
	feedback, err := s.ListOpikFeedbackScores(ctx, accountID, traceID)
	if err != nil {
		return nil, err
	}
	return &OpikTraceDetail{Trace: trace, Spans: spans, Feedback: feedback}, nil
}

func (s *PostgresStore) UpsertOpikTrace(ctx context.Context, trace *OpikTrace) error {
	tags := normalizeOpikTags(trace.Tags)
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO opik_traces (id, project_id, account_id, machine_id, name, thread_id,
		 start_time, end_time, input, output, metadata, tags, error_info, source, is_placeholder)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, false)
		 ON CONFLICT (id) DO UPDATE SET
		   machine_id  = CASE WHEN opik_traces.is_placeholder THEN EXCLUDED.machine_id ELSE opik_traces.machine_id END,
		   project_id  = COALESCE(EXCLUDED.project_id, opik_traces.project_id),
		   start_time  = CASE WHEN opik_traces.is_placeholder THEN EXCLUDED.start_time ELSE opik_traces.start_time END,
		   name        = COALESCE(EXCLUDED.name, opik_traces.name),
		   thread_id   = COALESCE(EXCLUDED.thread_id, opik_traces.thread_id),
		   end_time    = COALESCE(EXCLUDED.end_time, opik_traces.end_time),
		   input       = COALESCE(EXCLUDED.input, opik_traces.input),
		   output      = COALESCE(EXCLUDED.output, opik_traces.output),
		   metadata    = COALESCE(EXCLUDED.metadata, opik_traces.metadata),
		   tags        = COALESCE(EXCLUDED.tags, opik_traces.tags),
		   error_info  = COALESCE(EXCLUDED.error_info, opik_traces.error_info),
		   source      = COALESCE(EXCLUDED.source, opik_traces.source),
		   is_placeholder = false,
		   last_updated_at = NOW()
		 WHERE opik_traces.account_id = EXCLUDED.account_id
		   AND (opik_traces.machine_id = EXCLUDED.machine_id OR opik_traces.is_placeholder)`,
		trace.ID, trace.ProjectID, trace.AccountID, trace.MachineID,
		trace.Name, trace.ThreadID, trace.StartTime, trace.EndTime,
		trace.Input, trace.Output, trace.Metadata, tags,
		trace.ErrorInfo, trace.Source)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrOpikScopeConflict
	}
	return nil
}

func (s *PostgresStore) UpsertOpikTraces(ctx context.Context, traces []OpikTrace) error {
	for i := range traces {
		if err := s.UpsertOpikTrace(ctx, &traces[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) UpdateOpikTrace(ctx context.Context, accountID int, machineID, id string, trace *OpikTrace) error {
	tags := normalizeOpikTags(trace.Tags)
	tag, err := s.pool.Exec(ctx,
		`UPDATE opik_traces SET
		   name        = COALESCE($4, name),
		   thread_id   = COALESCE($5, thread_id),
		   end_time    = COALESCE($6, end_time),
		   input       = COALESCE($7, input),
		   output      = COALESCE($8, output),
		   metadata    = COALESCE($9, metadata),
		   tags        = COALESCE($10, tags),
		   error_info  = COALESCE($11, error_info),
		   source      = COALESCE($12, source),
		   last_updated_at = NOW()
		 WHERE id = $1 AND account_id = $2 AND machine_id = $3`,
		id, accountID, machineID, trace.Name, trace.ThreadID, trace.EndTime,
		trace.Input, trace.Output, trace.Metadata, tags,
		trace.ErrorInfo, trace.Source)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrOpikRecordNotFound
	}
	return nil
}

func (s *PostgresStore) UpsertOpikSpan(ctx context.Context, span *OpikSpan) error {
	// Extract token counts from usage JSONB into dedicated columns
	span.extractTokenCounts()
	tags := normalizeOpikTags(span.Tags)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx,
		`INSERT INTO opik_traces (id, project_id, account_id, machine_id, start_time, is_placeholder)
		 SELECT $1, $2, $3, $4, $5, true
		 WHERE NOT EXISTS (
		   SELECT 1 FROM opik_traces
		   WHERE id = $1 AND account_id <> $3
		 )
		 ON CONFLICT (id) DO NOTHING`,
		span.TraceID, span.ProjectID, span.AccountID, span.MachineID, span.StartTime)
	if err != nil {
		return err
	}

	var traceID string
	err = tx.QueryRow(ctx,
		`SELECT id::text FROM opik_traces WHERE id = $1 AND account_id = $2`,
		span.TraceID, span.AccountID,
	).Scan(&traceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOpikScopeConflict
		}
		return err
	}

	tag, err := tx.Exec(ctx,
		`INSERT INTO opik_spans (id, trace_id, project_id, account_id, machine_id,
		 parent_span_id, name, type, start_time, end_time,
		 input, output, metadata, model, provider,
		 tags, usage, prompt_tokens, completion_tokens, total_tokens,
		 error_info, total_estimated_cost, duration, ttft, source)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25)
		 ON CONFLICT (id) DO UPDATE SET
		   parent_span_id     = COALESCE(EXCLUDED.parent_span_id, opik_spans.parent_span_id),
		   name               = COALESCE(EXCLUDED.name, opik_spans.name),
		   end_time           = COALESCE(EXCLUDED.end_time, opik_spans.end_time),
		   input              = COALESCE(EXCLUDED.input, opik_spans.input),
		   output             = COALESCE(EXCLUDED.output, opik_spans.output),
		   metadata           = COALESCE(EXCLUDED.metadata, opik_spans.metadata),
		   model              = COALESCE(EXCLUDED.model, opik_spans.model),
		   provider           = COALESCE(EXCLUDED.provider, opik_spans.provider),
		   tags               = COALESCE(EXCLUDED.tags, opik_spans.tags),
		   usage              = COALESCE(EXCLUDED.usage, opik_spans.usage),
		   prompt_tokens      = COALESCE(EXCLUDED.prompt_tokens, opik_spans.prompt_tokens),
		   completion_tokens  = COALESCE(EXCLUDED.completion_tokens, opik_spans.completion_tokens),
		   total_tokens       = COALESCE(EXCLUDED.total_tokens, opik_spans.total_tokens),
		   error_info         = COALESCE(EXCLUDED.error_info, opik_spans.error_info),
		   total_estimated_cost = COALESCE(EXCLUDED.total_estimated_cost, opik_spans.total_estimated_cost),
		   duration           = COALESCE(EXCLUDED.duration, opik_spans.duration),
		   ttft               = COALESCE(EXCLUDED.ttft, opik_spans.ttft),
		   source             = COALESCE(EXCLUDED.source, opik_spans.source),
		   last_updated_at    = NOW()
		 WHERE opik_spans.account_id = EXCLUDED.account_id
		   AND opik_spans.machine_id = EXCLUDED.machine_id`,
		span.ID, span.TraceID, span.ProjectID, span.AccountID, span.MachineID,
		span.ParentSpanID, span.Name, span.Type, span.StartTime, span.EndTime,
		span.Input, span.Output, span.Metadata, span.Model, span.Provider,
		tags, span.Usage, span.PromptTokens, span.CompletionTokens, span.TotalTokens,
		span.ErrorInfo, span.TotalEstimatedCost, span.Duration, span.TTFT, span.Source)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrOpikScopeConflict
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) UpsertOpikSpans(ctx context.Context, spans []OpikSpan) error {
	for i := range spans {
		if err := s.UpsertOpikSpan(ctx, &spans[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) UpdateOpikSpan(ctx context.Context, accountID int, machineID, id string, span *OpikSpan) error {
	span.extractTokenCounts()
	tags := normalizeOpikTags(span.Tags)

	tag, err := s.pool.Exec(ctx,
		`UPDATE opik_spans SET
		   parent_span_id     = COALESCE($4, parent_span_id),
		   name               = COALESCE($5, name),
		   end_time           = COALESCE($6, end_time),
		   input              = COALESCE($7, input),
		   output             = COALESCE($8, output),
		   metadata           = COALESCE($9, metadata),
		   model              = COALESCE($10, model),
		   provider           = COALESCE($11, provider),
		   tags               = COALESCE($12, tags),
		   usage              = COALESCE($13, usage),
		   prompt_tokens      = COALESCE($14, prompt_tokens),
		   completion_tokens  = COALESCE($15, completion_tokens),
		   total_tokens       = COALESCE($16, total_tokens),
		   error_info         = COALESCE($17, error_info),
		   total_estimated_cost = COALESCE($18, total_estimated_cost),
		   duration           = COALESCE($19, duration),
		   ttft               = COALESCE($20, ttft),
		   source             = COALESCE($21, source),
		   last_updated_at    = NOW()
		 WHERE id = $1 AND account_id = $2 AND machine_id = $3`,
		id, accountID, machineID, span.ParentSpanID, span.Name, span.EndTime,
		span.Input, span.Output, span.Metadata, span.Model, span.Provider,
		tags, span.Usage, span.PromptTokens, span.CompletionTokens, span.TotalTokens,
		span.ErrorInfo, span.TotalEstimatedCost, span.Duration, span.TTFT, span.Source)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrOpikRecordNotFound
	}
	return nil
}

// ---- Integration Catalog ----

func (s *PostgresStore) ListIntegrationCatalog(ctx context.Context) ([]IntegrationCatalogEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, icon, toolkit, auth_config_id, category, sort_order, enabled, created_at, updated_at
		 FROM integration_catalog ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []IntegrationCatalogEntry
	for rows.Next() {
		var e IntegrationCatalogEntry
		if err := rows.Scan(&e.ID, &e.Name, &e.Icon, &e.Toolkit, &e.AuthConfigID,
			&e.Category, &e.SortOrder, &e.Enabled, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *PostgresStore) ListConfiguredIntegrations(ctx context.Context) ([]IntegrationCatalogEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, icon, toolkit, auth_config_id, category, sort_order, enabled, created_at, updated_at
		 FROM integration_catalog WHERE enabled = true AND auth_config_id IS NOT NULL
		 ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []IntegrationCatalogEntry
	for rows.Next() {
		var e IntegrationCatalogEntry
		if err := rows.Scan(&e.ID, &e.Name, &e.Icon, &e.Toolkit, &e.AuthConfigID,
			&e.Category, &e.SortOrder, &e.Enabled, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *PostgresStore) GetIntegrationCatalogEntry(ctx context.Context, id string) (*IntegrationCatalogEntry, error) {
	var e IntegrationCatalogEntry
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, icon, toolkit, auth_config_id, category, sort_order, enabled, created_at, updated_at
		 FROM integration_catalog WHERE id = $1`, id).
		Scan(&e.ID, &e.Name, &e.Icon, &e.Toolkit, &e.AuthConfigID,
			&e.Category, &e.SortOrder, &e.Enabled, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *PostgresStore) CreateIntegrationCatalogEntry(ctx context.Context, e IntegrationCatalogEntry) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO integration_catalog (id, name, icon, toolkit, auth_config_id, category, sort_order, enabled)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		e.ID, e.Name, e.Icon, e.Toolkit, e.AuthConfigID, e.Category, e.SortOrder, e.Enabled)
	return err
}

func (s *PostgresStore) UpdateIntegrationCatalogEntry(ctx context.Context, e IntegrationCatalogEntry) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE integration_catalog SET name=$2, icon=$3, toolkit=$4, auth_config_id=$5,
		 category=$6, sort_order=$7, enabled=$8, updated_at=NOW() WHERE id=$1`,
		e.ID, e.Name, e.Icon, e.Toolkit, e.AuthConfigID, e.Category, e.SortOrder, e.Enabled)
	return err
}

func (s *PostgresStore) DeleteIntegrationCatalogEntry(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM integration_catalog WHERE id = $1`, id)
	return err
}

// ---- Integration Events ----

func (s *PostgresStore) LogIntegrationEvent(ctx context.Context, accountID int, machineID, integrationID, event string, metadata json.RawMessage) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO integration_events (account_id, machine_id, integration_id, event, metadata)
		 VALUES ($1, $2, $3, $4, $5)`,
		accountID, machineID, integrationID, event, metadata)
	return err
}

func (s *PostgresStore) HasIntegrationEvent(ctx context.Context, machineID, integrationID, event string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM integration_events WHERE machine_id=$1 AND integration_id=$2 AND event=$3)`,
		machineID, integrationID, event).Scan(&exists)
	return exists, err
}

// ---- Browse Sessions ----

func (s *PostgresStore) CreateBrowseSession(ctx context.Context, session *BrowseSession) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO browse_sessions (machine_id, account_id, cdp_target, expires_at)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, created_at, last_heartbeat`,
		session.MachineID, session.AccountID, session.CDPTarget, session.ExpiresAt,
	).Scan(&session.ID, &session.CreatedAt, &session.LastHeartbeat)
}

func (s *PostgresStore) GetBrowseSession(ctx context.Context, id string) (*BrowseSession, error) {
	bs := &BrowseSession{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, machine_id, account_id, cdp_target, created_at, expires_at, last_heartbeat
		 FROM browse_sessions WHERE id = $1`, id,
	).Scan(&bs.ID, &bs.MachineID, &bs.AccountID, &bs.CDPTarget, &bs.CreatedAt, &bs.ExpiresAt, &bs.LastHeartbeat)
	if err != nil {
		return nil, err
	}
	return bs, nil
}

func (s *PostgresStore) HeartbeatBrowseSession(ctx context.Context, id string, expiresAt time.Time) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE browse_sessions SET last_heartbeat = NOW(), expires_at = $1 WHERE id = $2 AND expires_at > NOW()`,
		expiresAt, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("browse session not found")
	}
	return nil
}

func (s *PostgresStore) DeleteBrowseSession(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM browse_sessions WHERE id = $1`, id)
	return err
}

func (s *PostgresStore) ListExpiredBrowseSessions(ctx context.Context) ([]BrowseSession, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, machine_id, account_id, cdp_target, created_at, expires_at, last_heartbeat
		 FROM browse_sessions WHERE expires_at < NOW()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := []BrowseSession{}
	for rows.Next() {
		var bs BrowseSession
		if err := rows.Scan(&bs.ID, &bs.MachineID, &bs.AccountID, &bs.CDPTarget, &bs.CreatedAt, &bs.ExpiresAt, &bs.LastHeartbeat); err != nil {
			return nil, err
		}
		sessions = append(sessions, bs)
	}
	return sessions, nil
}

// ---- Browser VMs ----

const browserVMColumns = `id, account_id, slug, name, host_id, vm_ip, status,
	vcpus, memory_mb, cdp_port, desired_rootfs_manifest, desired_rootfs_version,
	rootfs_version, error_message, created_at, updated_at`

func scanBrowserVM(scan func(dest ...any) error) (*BrowserVM, error) {
	bvm := &BrowserVM{}
	err := scan(&bvm.ID, &bvm.AccountID, &bvm.Slug, &bvm.Name, &bvm.HostID, &bvm.VMIP,
		&bvm.Status, &bvm.VCPUs, &bvm.MemoryMB, &bvm.CDPPort,
		&bvm.DesiredRootfsManifest, &bvm.DesiredRootfsVersion, &bvm.RootfsVersion,
		&bvm.ErrorMessage, &bvm.CreatedAt, &bvm.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return bvm, nil
}

func (s *PostgresStore) CreateBrowserVM(ctx context.Context, bvm *BrowserVM) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO browser_vms (account_id, slug, name, vcpus, memory_mb, cdp_port, desired_rootfs_manifest, desired_rootfs_version)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING id, created_at, updated_at`,
		bvm.AccountID, bvm.Slug, bvm.Name, bvm.VCPUs, bvm.MemoryMB, bvm.CDPPort, bvm.DesiredRootfsManifest, bvm.DesiredRootfsVersion,
	).Scan(&bvm.ID, &bvm.CreatedAt, &bvm.UpdatedAt)
}

func (s *PostgresStore) GetBrowserVM(ctx context.Context, id string) (*BrowserVM, error) {
	return scanBrowserVM(s.pool.QueryRow(ctx,
		`SELECT `+browserVMColumns+` FROM browser_vms WHERE id = $1`, id).Scan)
}

func (s *PostgresStore) ListBrowserVMsByAccount(ctx context.Context, accountID int) ([]BrowserVM, error) {
	// LEFT JOIN machines so the list endpoint can render "paired with"
	// without a second round trip per VM. machines.browser_vm_id is the
	// pairing column; NULL → unpaired (paired_machine stays nil).
	rows, err := s.pool.Query(ctx,
		`SELECT b.id, b.account_id, b.slug, b.name, b.host_id, b.vm_ip, b.status,
		        b.vcpus, b.memory_mb, b.cdp_port, b.desired_rootfs_manifest,
		        b.desired_rootfs_version, b.rootfs_version, b.error_message,
		        b.created_at, b.updated_at,
		        m.id, m.name
		 FROM browser_vms b
		 LEFT JOIN machines m ON m.browser_vm_id = b.id
		 WHERE b.account_id = $1
		 ORDER BY b.created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bvms []BrowserVM
	for rows.Next() {
		var bvm BrowserVM
		var pairedID, pairedName *string
		if err := rows.Scan(&bvm.ID, &bvm.AccountID, &bvm.Slug, &bvm.Name, &bvm.HostID, &bvm.VMIP,
			&bvm.Status, &bvm.VCPUs, &bvm.MemoryMB, &bvm.CDPPort,
			&bvm.DesiredRootfsManifest, &bvm.DesiredRootfsVersion, &bvm.RootfsVersion,
			&bvm.ErrorMessage, &bvm.CreatedAt, &bvm.UpdatedAt,
			&pairedID, &pairedName); err != nil {
			return nil, err
		}
		if pairedID != nil {
			bvm.PairedMachine = &BrowserVMPairedMachine{ID: *pairedID, Name: derefOrEmpty(pairedName)}
		}
		bvms = append(bvms, bvm)
	}
	return bvms, nil
}

func (s *PostgresStore) ListBrowserVMsRequiringReconciliation(ctx context.Context) ([]BrowserVM, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+browserVMColumns+`
		 FROM browser_vms b
		 WHERE b.status = 'error'
		   AND b.host_id IS NOT NULL
		   AND b.vm_ip IS NOT NULL
		   AND EXISTS (
		       SELECT 1 FROM browser_vm_placements p
		       WHERE p.browser_vm_id = b.id
		         AND p.released_at IS NULL
		   )
		 ORDER BY b.updated_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bvms []BrowserVM
	for rows.Next() {
		bvm, err := scanBrowserVM(rows.Scan)
		if err != nil {
			return nil, err
		}
		bvms = append(bvms, *bvm)
	}
	return bvms, rows.Err()
}

// derefOrEmpty returns *s if non-nil, else "". Used for nullable text
// columns where the caller wants the value but tolerates NULL as empty.
func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (s *PostgresStore) UpdateBrowserVMStatus(ctx context.Context, id, status string, errMsg *string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE browser_vms SET status = $2, error_message = $3, updated_at = now() WHERE id = $1`,
		id, status, errMsg)
	return err
}

func (s *PostgresStore) AssignBrowserVMToHost(ctx context.Context, id string, hostID int, vmIP string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE browser_vms SET host_id = $2, vm_ip = $3, updated_at = now() WHERE id = $1`,
		id, hostID, vmIP)
	return err
}

func (s *PostgresStore) UnassignBrowserVMFromHost(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE browser_vms SET host_id = NULL, vm_ip = NULL, updated_at = now() WHERE id = $1`, id)
	return err
}

func (s *PostgresStore) DeleteBrowserVM(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM browser_vms WHERE id = $1`, id)
	return err
}

func (s *PostgresStore) PairBrowserVM(ctx context.Context, machineID, browserVMID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machines SET browser_vm_id = $2 WHERE id = $1`, machineID, browserVMID)
	return err
}

func (s *PostgresStore) UnpairBrowserVM(ctx context.Context, machineID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machines SET browser_vm_id = NULL WHERE id = $1`, machineID)
	return err
}

func (s *PostgresStore) GetMachineByBrowserVMID(ctx context.Context, browserVMID string) (*Machine, error) {
	return scanMachine(s.pool.QueryRow(ctx,
		`SELECT `+machineColumns+` FROM machines WHERE browser_vm_id = $1`, browserVMID).Scan)
}

const browserVMTargetHostHeartbeatMaxAge = 180 * time.Second

func validateBrowserVMTargetHostEligibility(hostID int, status string, maintenanceMode bool, lastHeartbeat *time.Time, now time.Time) error {
	if status != "ready" {
		return fmt.Errorf("host %d is %s, not ready", hostID, status)
	}
	if maintenanceMode {
		return fmt.Errorf("host %d is in maintenance mode", hostID)
	}
	if lastHeartbeat == nil {
		return fmt.Errorf("host %d has no heartbeat", hostID)
	}
	if lastHeartbeat.Before(now.Add(-browserVMTargetHostHeartbeatMaxAge)) {
		return fmt.Errorf("host %d heartbeat is stale", hostID)
	}
	return nil
}

func (s *PostgresStore) PlaceBrowserVMOnHost(ctx context.Context, hostID int, browserVMID string, vcpus, memoryMB int) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock host row and check capacity atomically
	var (
		status          string
		maintenanceMode bool
		lastHeartbeat   *time.Time
		availVCPUs      int
		availMemMB      int
	)
	err = tx.QueryRow(ctx, `
		SELECT status, maintenance_mode, last_heartbeat,
		       capacity_vcpus - used_vcpus, capacity_memory_mb - used_memory_mb
		FROM hosts WHERE id = $1 FOR UPDATE`, hostID).Scan(
		&status, &maintenanceMode, &lastHeartbeat, &availVCPUs, &availMemMB)
	if err != nil {
		return "", fmt.Errorf("lock host %d: %w", hostID, err)
	}
	if err := validateBrowserVMTargetHostEligibility(hostID, status, maintenanceMode, lastHeartbeat, time.Now()); err != nil {
		return "", err
	}
	if availVCPUs < vcpus || availMemMB < memoryMB {
		return "", fmt.Errorf("insufficient capacity on host %d (need %d vCPUs/%d MB, have %d/%d)", hostID, vcpus, memoryMB, availVCPUs, availMemMB)
	}

	// Find free IP and insert placement in one query.
	// Exclude IPs held by:
	//   - active machine_placements on this host
	//   - active browser_vm_placements on this host
	//   - machines with host_id = this host (soft-stopped machines keep vm_ip for affinity
	//     even after their placement row is released, so we must respect that reservation)
	var vmIP string
	err = tx.QueryRow(ctx, `
		WITH used_ips AS (
			SELECT vm_ip FROM machine_placements WHERE host_id = $1 AND released_at IS NULL
			UNION ALL
			SELECT vm_ip FROM browser_vm_placements WHERE host_id = $1 AND released_at IS NULL
			UNION ALL
			SELECT vm_ip FROM machines WHERE host_id = $1 AND vm_ip IS NOT NULL
		),
		candidates AS (
			SELECT '192.168.100.' || n AS ip
			FROM generate_series(2, 254) n
			WHERE '192.168.100.' || n NOT IN (SELECT vm_ip FROM used_ips WHERE vm_ip IS NOT NULL)
			LIMIT 1
		)
		INSERT INTO browser_vm_placements (browser_vm_id, host_id, vm_ip, state)
		SELECT $2, $1, ip, 'reserved' FROM candidates
		RETURNING vm_ip`, hostID, browserVMID).Scan(&vmIP)
	if err != nil {
		return "", fmt.Errorf("no free IP on host %d: %w", hostID, err)
	}

	// Debit capacity
	_, err = tx.Exec(ctx,
		`UPDATE hosts SET used_vcpus = used_vcpus + $2, used_memory_mb = used_memory_mb + $3 WHERE id = $1`,
		hostID, vcpus, memoryMB)
	if err != nil {
		return "", fmt.Errorf("debit host capacity: %w", err)
	}

	return vmIP, tx.Commit(ctx)
}

func (s *PostgresStore) ActivateBrowserVMPlacement(ctx context.Context, browserVMID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE browser_vm_placements SET state = 'active', activated_at = now()
		 WHERE browser_vm_id = $1 AND released_at IS NULL`, browserVMID)
	return err
}

// ReleaseBrowserVMPlacement marks the active browser VM placement as released
// and credits the host's capacity accounting back in one transaction. Without
// the transaction, a failure between the two statements would release the
// placement row while leaving `hosts.used_*` inflated — leaking capacity on
// the host until the next full reconcile.
//
// pgx.ErrNoRows from the initial UPDATE is swallowed: "no active placement"
// is the idempotent success case (caller released a placement that was
// already released, or was never reserved), not an error.
func (s *PostgresStore) ReleaseBrowserVMPlacement(ctx context.Context, browserVMID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var hostID, vcpus, memoryMB int
	err = tx.QueryRow(ctx, `
		UPDATE browser_vm_placements SET state = 'released', released_at = now()
		WHERE browser_vm_id = $1 AND released_at IS NULL
		RETURNING host_id,
			(SELECT vcpus FROM browser_vms WHERE id = $1),
			(SELECT memory_mb FROM browser_vms WHERE id = $1)`,
		browserVMID).Scan(&hostID, &vcpus, &memoryMB)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("release browser VM placement: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE hosts SET used_vcpus = used_vcpus - $2, used_memory_mb = used_memory_mb - $3 WHERE id = $1`,
		hostID, vcpus, memoryMB); err != nil {
		return fmt.Errorf("credit host capacity: %w", err)
	}

	return tx.Commit(ctx)
}

// ---- Provider Catalog ----

func (s *PostgresStore) ListProviderCatalog(ctx context.Context) ([]ProviderCatalogEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, label, category, upstream_host, upstream_path_prefix, scheme,
		        auth_method, auth_header, allowed_hosts, env_var, credential_type,
		        placeholder, icon_letter, icon_bg, validation,
		        exec_secret_id, runtime_provider_id, plugin_id,
		        autodetect_order, sort_order, enabled, created_at
		 FROM provider_catalog
		 WHERE enabled = true
		 ORDER BY sort_order`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []ProviderCatalogEntry
	for rows.Next() {
		var e ProviderCatalogEntry
		if err := rows.Scan(&e.ID, &e.Label, &e.Category, &e.UpstreamHost, &e.UpstreamPathPrefix, &e.Scheme,
			&e.AuthMethod, &e.AuthHeader, &e.AllowedHosts, &e.EnvVar, &e.CredentialType,
			&e.Placeholder, &e.IconLetter, &e.IconBg, &e.Validation,
			&e.ExecSecretID, &e.RuntimeProviderID, &e.PluginID,
			&e.AutodetectOrder, &e.SortOrder, &e.Enabled, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *PostgresStore) ListProviderCatalogByCategory(ctx context.Context, category string) ([]ProviderCatalogEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, label, category, upstream_host, upstream_path_prefix, scheme,
		        auth_method, auth_header, allowed_hosts, env_var, credential_type,
		        placeholder, icon_letter, icon_bg, validation,
		        exec_secret_id, runtime_provider_id, plugin_id,
		        autodetect_order, sort_order, enabled, created_at
		 FROM provider_catalog
		 WHERE enabled = true AND category = $1
		 ORDER BY sort_order`, category)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []ProviderCatalogEntry
	for rows.Next() {
		var e ProviderCatalogEntry
		if err := rows.Scan(&e.ID, &e.Label, &e.Category, &e.UpstreamHost, &e.UpstreamPathPrefix, &e.Scheme,
			&e.AuthMethod, &e.AuthHeader, &e.AllowedHosts, &e.EnvVar, &e.CredentialType,
			&e.Placeholder, &e.IconLetter, &e.IconBg, &e.Validation,
			&e.ExecSecretID, &e.RuntimeProviderID, &e.PluginID,
			&e.AutodetectOrder, &e.SortOrder, &e.Enabled, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *PostgresStore) GetProviderCatalogEntry(ctx context.Context, id string) (*ProviderCatalogEntry, error) {
	var e ProviderCatalogEntry
	err := s.pool.QueryRow(ctx,
		`SELECT id, label, category, upstream_host, upstream_path_prefix, scheme,
		        auth_method, auth_header, allowed_hosts, env_var, credential_type,
		        placeholder, icon_letter, icon_bg, validation,
		        exec_secret_id, runtime_provider_id, plugin_id,
		        autodetect_order, sort_order, enabled, created_at
		 FROM provider_catalog
		 WHERE id = $1`, id).Scan(&e.ID, &e.Label, &e.Category, &e.UpstreamHost, &e.UpstreamPathPrefix, &e.Scheme,
		&e.AuthMethod, &e.AuthHeader, &e.AllowedHosts, &e.EnvVar, &e.CredentialType,
		&e.Placeholder, &e.IconLetter, &e.IconBg, &e.Validation,
		&e.ExecSecretID, &e.RuntimeProviderID, &e.PluginID,
		&e.AutodetectOrder, &e.SortOrder, &e.Enabled, &e.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}
