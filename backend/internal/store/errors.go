package store

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

var ErrAlreadyMember = errors.New("account member already exists")
var ErrWorkflowLockHeld = errors.New("workflow lock already held")
var ErrWorkflowLockNotFound = errors.New("workflow lock not found")
var ErrPluginNotFound = errors.New("plugin catalog entry not found")
var ErrPluginStillEnabled = errors.New("plugin still enabled on machines")
var ErrMachinePluginNotFound = errors.New("machine plugin not found")
var ErrOpikRecordNotFound = errors.New("opik record not found")
var ErrOpikScopeConflict = errors.New("opik record belongs to a different scope")

// IsConflict returns true if the error is a PostgreSQL unique violation (code 23505).
func IsConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
