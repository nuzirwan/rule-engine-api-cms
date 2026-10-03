-- 0002_add_flow_version_note.up.sql — example additive (expand-only) change.
--
-- Demonstrates the migration discipline from [[data-and-migrations]]: add a new
-- NULLABLE column (expand) in its own migration; never rename/drop in the same
-- release that stops using a column. `note` carries the author's "why" for a
-- version; it is optional, so old rows and old code keep working unchanged.
ALTER TABLE flow_versions        ADD COLUMN IF NOT EXISTS note text;
ALTER TABLE jdm_versions         ADD COLUMN IF NOT EXISTS note text;
ALTER TABLE connection_versions  ADD COLUMN IF NOT EXISTS note text;
