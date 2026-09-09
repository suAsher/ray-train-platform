SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE mlflow_dashboard_tickets
  ADD COLUMN IF NOT EXISTS redirect_fragment TEXT NOT NULL DEFAULT '';

ALTER TABLE mlflow_dashboard_tickets
  ADD CONSTRAINT mlflow_dashboard_tickets_redirect_fragment_check
  CHECK (redirect_fragment = '' OR redirect_fragment ~ '^#/experiments/[0-9]+/runs/[0-9a-f]{32}$');
