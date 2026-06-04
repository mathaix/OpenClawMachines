-- Add status_message column to hosts for surfacing provisioning errors
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS status_message TEXT;
