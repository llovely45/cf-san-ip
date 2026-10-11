-- Keep claim retries recoverable and bound each agent to four active blocks.
ALTER TABLE scan_block_progress ADD COLUMN lease_request_id TEXT;

CREATE INDEX idx_scan_block_progress_agent_lease
  ON scan_block_progress(lease_agent_id, lease_expires_at, lease_request_id);
