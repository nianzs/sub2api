-- 网关设置：Kiro 缓存模拟分桶方式。
-- false = 按 Kiro 账号分桶（默认，行为与历史版本一致）；
-- true = 按「API key + 会话」分桶，切换上游账号不丢缓存命中；没有会话标识时按
--        系统提示 + 第一条 user 消息派生会话（同 kiro.rs）。仅影响计费口径。

INSERT INTO settings (key, value, updated_at)
VALUES ('gateway_kiro_cache_scope_client', 'false', NOW())
ON CONFLICT (key) DO NOTHING;
