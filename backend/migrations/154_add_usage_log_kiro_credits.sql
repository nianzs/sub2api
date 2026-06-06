-- Add Kiro real credits column to usage_logs.
-- 来源：上游 meteringEvent.usage 字段（浮点 credit 数）累加。
-- 仅 Kiro 平台请求会写入，其他平台保持 NULL。
-- 用于事后对账（sub2api total_cost ≡ kiro_credits × group.kiro_credit_target_usd）。
ALTER TABLE usage_logs
  ADD COLUMN IF NOT EXISTS kiro_credits NUMERIC(10,4);

COMMENT ON COLUMN usage_logs.kiro_credits IS
  'Kiro 上游 meteringEvent.usage 累加值（真实 credits 消耗，浮点）。仅 platform=kiro 写入，其他 NULL。';

-- 配套索引见 155_add_usage_log_kiro_credits_index_notx.sql（非事务建索引，避免锁大表）。
