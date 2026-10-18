-- Why Warmbly Cloud last refused the mailbox's pending enrollment; cleared once Cloud accepts it.
ALTER TABLE public.cloud_link_mailboxes
    ADD COLUMN enrollment_error_code text,
    ADD COLUMN enrollment_error text;
