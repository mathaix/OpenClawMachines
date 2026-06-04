-- Add proxy_token to machines for WebSocket proxy authentication
ALTER TABLE machines ADD COLUMN IF NOT EXISTS proxy_token TEXT;
