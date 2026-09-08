SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- 0029 originally reserved resumable uploads for objects above 256 MiB.
-- The public ingress rejects single requests above roughly 60 MiB, so the
-- application now begins multipart uploads just above its 32 MiB part size.
ALTER TABLE data_space_uploads DROP CONSTRAINT IF EXISTS data_space_uploads_size_bytes_check;
ALTER TABLE data_space_uploads
  ADD CONSTRAINT data_space_uploads_size_bytes_check CHECK (size_bytes > 33554432);
