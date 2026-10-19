ALTER TABLE public.cloud_link_mailboxes
    DROP CONSTRAINT IF EXISTS cloud_link_mailboxes_cloud_warmup_state_check,
    DROP COLUMN IF EXISTS cloud_warmup_observed_at,
    DROP COLUMN IF EXISTS cloud_warmup_target,
    DROP COLUMN IF EXISTS cloud_warmup_volume,
    DROP COLUMN IF EXISTS cloud_warmup_started_at,
    DROP COLUMN IF EXISTS cloud_warmup_state;
