-- 155_add_usage_log_kiro_credits_index_notx.sql
-- kiro_credits 对账聚合用的部分索引（按时间范围筛 platform=kiro 的记录）。
-- 非事务迁移（_notx）：CREATE INDEX CONCURRENTLY 不可在事务内执行，
-- 用 CONCURRENTLY 避免在大体量 usage_logs 表上建索引时阻塞写入。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_kiro_credits_at
  ON usage_logs (created_at) WHERE kiro_credits IS NOT NULL;
