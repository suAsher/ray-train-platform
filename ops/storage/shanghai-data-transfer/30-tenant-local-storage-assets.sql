-- Register only PVC-backed, smoke-tested roots.  This script deliberately
-- contains neither a bucket URI nor credentials; those stay in Kubernetes
-- Secrets consumed by the FSX CSI driver and platform backend.
--
-- Re-running is safe: existing records are left untouched so an operator's
-- later catalogue edits are not silently overwritten.
INSERT INTO storage_assets (
  id, tenant_id, owner_user_id, name, description, kind, provider,
  claim_name, root_prefix, read_only, browse_enabled, created_by
)
VALUES
  (
    'tos-local-datasets',
    'local',
    NULL,
    '训练数据（TOS）',
    '本租户已验证的只读训练数据目录。可在目录内选择子目录。',
    'dataset',
    'tos',
    'tos-local-datasets',
    'ray-train/tenants/local/datasets/',
    TRUE,
    TRUE,
    'platform-bootstrap'
  ),
  (
    'tos-local-checkpoints',
    'local',
    NULL,
    '初始 Checkpoint（TOS）',
    '本租户已验证的只读 Checkpoint 目录。可在目录内选择子目录。',
    'checkpoint',
    'tos',
    'tos-local-checkpoints',
    'ray-train/tenants/local/checkpoints/',
    TRUE,
    TRUE,
    'platform-bootstrap'
  ),
  (
    'tos-local-outputs',
    'local',
    NULL,
    '训练产物（TOS）',
    '本租户已验证的可写训练产物目录；平台会为每个任务创建独立 runs/<job-id> 子目录。',
    'output',
    'tos',
    'tos-local-outputs',
    'ray-train/tenants/local/outputs/',
    FALSE,
    TRUE,
    'platform-bootstrap'
  )
ON CONFLICT (id) DO NOTHING;

SELECT id, tenant_id, kind, provider, claim_name, read_only, browse_enabled
FROM storage_assets
WHERE id IN ('tos-local-datasets', 'tos-local-checkpoints', 'tos-local-outputs')
ORDER BY id;
