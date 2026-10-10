CREATE TABLE scan_plan_snapshots (
  asn INTEGER PRIMARY KEY CHECK (asn > 0),
  prefixes_json TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE scan_checkpoints (
  asn INTEGER NOT NULL CHECK (asn > 0),
  source_prefix TEXT NOT NULL,
  block_cidr TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('active', 'completed')),
  latest_ip TEXT,
  last_reported_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (asn, source_prefix)
);

CREATE INDEX idx_scan_checkpoints_asn ON scan_checkpoints(asn);

-- Preserve the old plan identity and one completed checkpoint per source
-- prefix. The Go agent combines these sparse rows into a single ascending
-- cursor without re-materializing all of the old /24 block rows.
INSERT INTO scan_plan_snapshots(asn, prefixes_json)
SELECT asn, json_group_array(source_prefix)
FROM (
  SELECT DISTINCT asn, source_prefix
  FROM scan_blocks
  ORDER BY asn, source_prefix
)
GROUP BY asn;

WITH addresses AS (
  SELECT
    asn,
    source_prefix,
    block_cidr,
    status,
    latest_ip,
    last_reported_at,
    updated_at,
    substr(block_cidr, 1, instr(block_cidr, '/') - 1) AS address
  FROM scan_blocks
  WHERE status = 'completed'
), octets_one_two AS (
  SELECT
    *,
    CAST(substr(address, 1, instr(address, '.') - 1) AS INTEGER) AS octet_one,
    substr(address, instr(address, '.') + 1) AS tail_one
  FROM addresses
), octets_one_two_three AS (
  SELECT
    *,
    CAST(substr(tail_one, 1, instr(tail_one, '.') - 1) AS INTEGER) AS octet_two,
    substr(tail_one, instr(tail_one, '.') + 1) AS tail_two
  FROM octets_one_two
), numeric_addresses AS (
  SELECT
    *,
    CAST(substr(tail_two, 1, instr(tail_two, '.') - 1) AS INTEGER) AS octet_three,
    CAST(substr(tail_two, instr(tail_two, '.') + 1) AS INTEGER) AS octet_four
  FROM octets_one_two_three
), ranked AS (
  SELECT
    asn,
    source_prefix,
    block_cidr,
    status,
    latest_ip,
    ROW_NUMBER() OVER (
      PARTITION BY asn, source_prefix
      ORDER BY
        last_reported_at DESC,
        updated_at DESC,
        (octet_one * 16777216 + octet_two * 65536 + octet_three * 256 + octet_four) DESC
    ) AS checkpoint_rank
  FROM numeric_addresses
)
INSERT INTO scan_checkpoints(asn, source_prefix, block_cidr, status, latest_ip)
SELECT asn, source_prefix, block_cidr, status, latest_ip
FROM ranked
WHERE checkpoint_rank = 1;
