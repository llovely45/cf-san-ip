CREATE TABLE ip_records (
  ip TEXT PRIMARY KEY,
  port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
  asn INTEGER NOT NULL CHECK (asn > 0),
  region TEXT NOT NULL,
  asn_alias TEXT
);

CREATE INDEX idx_ip_records_asn_region ON ip_records(asn, region);

CREATE TABLE app_settings (
  singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
  admin_password TEXT,
  api_token TEXT
);

INSERT INTO app_settings(singleton_id, admin_password, api_token) VALUES (1, NULL, NULL);
