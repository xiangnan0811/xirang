CREATE TABLE IF NOT EXISTS dashboards (
  id BIGSERIAL PRIMARY KEY,
  owner_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name VARCHAR(100) NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  time_range VARCHAR(16) NOT NULL DEFAULT '1h',
  custom_start TIMESTAMPTZ,
  custom_end TIMESTAMPTZ,
  auto_refresh_seconds INTEGER NOT NULL DEFAULT 30,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_dashboards_owner_name ON dashboards(owner_id, name);

CREATE TABLE IF NOT EXISTS dashboard_panels (
  id BIGSERIAL PRIMARY KEY,
  dashboard_id BIGINT NOT NULL REFERENCES dashboards(id) ON DELETE CASCADE,
  title VARCHAR(100) NOT NULL,
  chart_type VARCHAR(16) NOT NULL,
  metric VARCHAR(32) NOT NULL,
  filters TEXT NOT NULL DEFAULT '{}',
  aggregation VARCHAR(16) NOT NULL,
  layout_x INTEGER NOT NULL DEFAULT 0,
  layout_y INTEGER NOT NULL DEFAULT 0,
  layout_w INTEGER NOT NULL DEFAULT 6,
  layout_h INTEGER NOT NULL DEFAULT 4,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_dashboard_panels_dashboard ON dashboard_panels(dashboard_id);
