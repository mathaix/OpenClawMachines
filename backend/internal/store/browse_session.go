package store

import "time"

// BrowseSession tracks an active `ocm machines browse` CLI connection.
type BrowseSession struct {
	ID            string    `json:"id"`
	MachineID     string    `json:"machine_id"`
	AccountID     int       `json:"account_id"`
	CDPTarget     string    `json:"cdp_target"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
}
