-- Adds duration billing facts for OpenAI-compatible audio transcription usage.
-- Rollback: stop audio transcription writes first, then run:
-- ALTER TABLE usage_logs DROP COLUMN IF EXISTS billable_duration_seconds;

ALTER TABLE usage_logs
  ADD COLUMN IF NOT EXISTS billable_duration_seconds INTEGER NOT NULL DEFAULT 0;
