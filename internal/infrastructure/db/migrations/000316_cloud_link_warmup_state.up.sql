-- The warmup Warmbly Cloud last reported for a mailbox it warms, so readers need no live call to know it.
ALTER TABLE public.cloud_link_mailboxes
    ADD COLUMN cloud_warmup_state text,
    ADD COLUMN cloud_warmup_started_at timestamptz,
    ADD COLUMN cloud_warmup_volume integer,
    ADD COLUMN cloud_warmup_target integer,
    ADD COLUMN cloud_warmup_observed_at timestamptz,
    ADD CONSTRAINT cloud_link_mailboxes_cloud_warmup_state_check
        CHECK (cloud_warmup_state IS NULL OR cloud_warmup_state IN ('active', 'paused', 'off'));
