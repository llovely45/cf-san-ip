-- Parallel workers need independent state per source-prefix scan block.
-- Keep scan_checkpoints as the legacy compact cursor for existing installations.
CREATE TABLE scan_block_progress (
  asn INTEGER NOT NULL CHECK (asn > 0),
  source_prefix TEXT NOT NULL,
  block_cidr TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('active', 'completed')),
  latest_ip TEXT,
  last_reported_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (asn, source_prefix, block_cidr)
);

CREATE INDEX idx_scan_block_progress_asn ON scan_block_progress(asn);
