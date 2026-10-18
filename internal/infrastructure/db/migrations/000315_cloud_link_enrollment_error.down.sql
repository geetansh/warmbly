ALTER TABLE public.cloud_link_mailboxes
    DROP COLUMN IF EXISTS enrollment_error,
    DROP COLUMN IF EXISTS enrollment_error_code;
