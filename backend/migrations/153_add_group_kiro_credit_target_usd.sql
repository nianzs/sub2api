-- Add Kiro reverse-token-scaling anchor price to groups.
-- USD value billed per Kiro credit. 0 = disabled (default).
ALTER TABLE groups
  ADD COLUMN IF NOT EXISTS kiro_credit_target_usd DECIMAL(8,4) NOT NULL DEFAULT 0;

COMMENT ON COLUMN groups.kiro_credit_target_usd IS
  'Kiro 反向 token 缩放锚定单价：每个 credit 对应的 sub2api USD 余额。0=禁用，仅 platform=kiro 生效。';

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'groups_kiro_credit_target_usd_range'
  ) THEN
    ALTER TABLE groups
      ADD CONSTRAINT groups_kiro_credit_target_usd_range
      CHECK (kiro_credit_target_usd >= 0 AND kiro_credit_target_usd <= 1);
  END IF;
END $$;
