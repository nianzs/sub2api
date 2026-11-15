-- Codex 门票（x-codex-turn-state 打票/注入）功能已下线，清理其残留数据。
-- settings：总开关、缺票拦截、打票代理（可能含代理密码）、默认长度、订阅档位规则。
-- accounts.extra：codex_turn_ticket:<model> 门票 state，以及更早版本的账号级
-- 打票代理 codex_harvest_proxy_url（可能含代理密码）。
-- 只改命中的行，不碰其他账号的 updated_at；可重复执行。

DELETE FROM settings
WHERE key IN (
    'openai_codex_ticket_enabled',
    'openai_codex_ticket_fail_closed',
    'openai_codex_ticket_harvest_proxy_url',
    'openai_codex_ticket_default_length',
    'openai_codex_ticket_plan_lengths'
);

UPDATE accounts
SET extra = extra - ARRAY(
        SELECT k
        FROM jsonb_object_keys(extra) AS k
        WHERE k LIKE 'codex\_turn\_ticket:%' OR k = 'codex_harvest_proxy_url'
    ),
    updated_at = NOW()
WHERE jsonb_typeof(extra) = 'object'
  AND EXISTS (
        SELECT 1
        FROM jsonb_object_keys(extra) AS k
        WHERE k LIKE 'codex\_turn\_ticket:%' OR k = 'codex_harvest_proxy_url'
    );
