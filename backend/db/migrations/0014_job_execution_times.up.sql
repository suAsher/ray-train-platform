-- Job timing used to be recorded from the control plane's own clock:
-- finished_at was set to time.Now() at the moment the reconciler happened to
-- observe a terminal RayJob, and there was no started_at at all. That made the
-- reported end time later than reality by a poll interval, and made "duration"
-- silently include queue wait.
--
-- KubeRay already publishes the authoritative status.startTime and
-- status.endTime. These columns hold those values so the Portal can separate
-- queue wait from training time.
ALTER TABLE training_jobs
  ADD COLUMN IF NOT EXISTS started_at TIMESTAMPTZ;

-- Existing rows keep their approximate finished_at; it is the only timing
-- evidence retained for them. Backfilling started_at is deliberately not
-- attempted: a guessed start time would be indistinguishable from a measured
-- one, and the Portal renders an unknown start as such.
