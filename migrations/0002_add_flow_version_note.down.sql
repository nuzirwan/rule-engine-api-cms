-- 0002_add_flow_version_note.down.sql — reverse the additive note columns.
ALTER TABLE connection_versions  DROP COLUMN IF EXISTS note;
ALTER TABLE jdm_versions         DROP COLUMN IF EXISTS note;
ALTER TABLE flow_versions        DROP COLUMN IF EXISTS note;
