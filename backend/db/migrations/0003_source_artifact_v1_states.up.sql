DO $source_artifact_v1_states$
DECLARE
  invalid_state_count BIGINT;
BEGIN
  SELECT COUNT(*)
    INTO invalid_state_count
    FROM source_artifacts
   WHERE state NOT IN ('PENDING', 'READY');

  IF invalid_state_count > 0 THEN
    RAISE EXCEPTION 'source_artifacts V1 state migration refused: % row(s) are not PENDING or READY', invalid_state_count
      USING ERRCODE = '23514';
  END IF;
END
$source_artifact_v1_states$;

ALTER TABLE source_artifacts
  DROP CONSTRAINT IF EXISTS source_artifacts_state_check;

ALTER TABLE source_artifacts
  ADD CONSTRAINT source_artifacts_state_check
  CHECK (state IN ('PENDING', 'READY'));
