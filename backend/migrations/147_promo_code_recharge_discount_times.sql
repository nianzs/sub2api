ALTER TABLE promo_codes
    ADD COLUMN IF NOT EXISTS first_recharge_discount_times INTEGER NOT NULL DEFAULT 1;

COMMENT ON COLUMN promo_codes.first_recharge_discount_times IS 'Number of balance recharge orders eligible for the promo payment discount; 0 means unlimited';
