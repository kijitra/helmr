BEGIN;
ALTER TABLE vm_platforms DROP CONSTRAINT vm_platform_backend_payload;
-- Explicit null tests and COALESCE prevent CHECK's UNKNOWN result from
-- admitting incomplete backend payloads. Legacy columns keep their old checks.
ALTER TABLE vm_platforms ADD CONSTRAINT vm_platform_backend_payload CHECK (
 (backend = 'firecracker' AND arch IN ('x86_64','aarch64') AND contract = 'helmr.vm-runtime.v0'
  AND apple_vz IS NULL AND firecracker_digest IS NOT NULL
  AND firecracker_version IS NOT NULL AND snapshot_format_version IS NOT NULL)
 OR
 (backend = 'apple-vz' AND arch = 'aarch64' AND contract = 'helmr.vm-runtime.v1'
  AND firecracker_digest IS NULL AND firecracker_version IS NULL
  AND snapshot_format_version IS NULL AND cpu_template_digest IS NULL
  AND apple_vz IS NOT NULL AND COALESCE(
    jsonb_typeof(apple_vz) = 'object'
    AND apple_vz ?& ARRAY['helper_digest','os_build','hardware_model','installation_digest','key_domain_digest','boot_session_digest','state_format']
    AND apple_vz - ARRAY['helper_digest','os_build','hardware_model','installation_digest','key_domain_digest','boot_session_digest','state_format'] = '{}'::jsonb
    AND jsonb_typeof(apple_vz->'helper_digest') = 'string'
    AND apple_vz->>'helper_digest' ~ '^sha256:[0-9a-f]{64}$'
    AND jsonb_typeof(apple_vz->'installation_digest') = 'string'
    AND apple_vz->>'installation_digest' ~ '^sha256:[0-9a-f]{64}$'
    AND jsonb_typeof(apple_vz->'key_domain_digest') = 'string'
    AND apple_vz->>'key_domain_digest' ~ '^sha256:[0-9a-f]{64}$'
    AND jsonb_typeof(apple_vz->'boot_session_digest') = 'string'
    AND apple_vz->>'boot_session_digest' ~ '^sha256:[0-9a-f]{64}$'
    AND jsonb_typeof(apple_vz->'os_build') = 'string'
    AND octet_length(apple_vz->>'os_build') BETWEEN 1 AND 255
    AND apple_vz->>'os_build' !~ '(^[[:space:]])|([[:space:]]$)|[[:cntrl:]]'
    AND jsonb_typeof(apple_vz->'hardware_model') = 'string'
    AND octet_length(apple_vz->>'hardware_model') BETWEEN 1 AND 255
    AND apple_vz->>'hardware_model' !~ '(^[[:space:]])|([[:space:]]$)|[[:cntrl:]]'
    AND apple_vz->>'state_format' = 'helmr.apple-vz-state.v1', false))
);
COMMIT;
