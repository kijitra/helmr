BEGIN;
-- Never erase native identity or coerce it into Firecracker on downgrade.
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM vm_platforms WHERE backend <> 'firecracker') THEN
  RAISE EXCEPTION 'cannot downgrade while native VM platform identities exist';
 END IF;
END $$;
ALTER TABLE vm_platforms DROP CONSTRAINT vm_platform_backend_payload;
ALTER TABLE vm_platforms
 ALTER COLUMN firecracker_digest SET NOT NULL,
 ALTER COLUMN firecracker_version SET NOT NULL,
 ALTER COLUMN snapshot_format_version SET NOT NULL,
 DROP COLUMN apple_vz,
 DROP COLUMN contract,
 DROP COLUMN arch,
 DROP COLUMN backend;
COMMIT;
