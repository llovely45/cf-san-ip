-- Give each agent an exclusive, expiring lease for an ASN scan block.
ALTER TABLE scan_block_progress ADD COLUMN lease_agent_id TEXT;
ALTER TABLE scan_block_progress ADD COLUMN lease_id TEXT;
ALTER TABLE scan_block_progress ADD COLUMN lease_expires_at TEXT;

CREATE INDEX idx_scan_block_progress_lease_expiry
  ON scan_block_progress(asn, lease_expires_at);
