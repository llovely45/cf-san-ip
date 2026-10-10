CREATE TABLE scan_blocks (
  asn INTEGER NOT NULL CHECK (asn > 0),
  block_cidr TEXT NOT NULL,
  source_prefix TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'completed')),
  latest_ip TEXT,
  last_reported_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (asn, block_cidr)
);

CREATE INDEX idx_scan_blocks_asn_status ON scan_blocks(asn, status, block_cidr);
