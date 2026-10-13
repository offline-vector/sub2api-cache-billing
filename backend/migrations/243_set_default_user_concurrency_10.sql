-- Raise the user concurrency default and migrate legacy users that still have 5.
-- Account concurrency is intentionally unaffected.
UPDATE users
SET concurrency = 10,
    updated_at = NOW()
WHERE concurrency = 5;

UPDATE settings
SET value = '10'
WHERE key = 'default_concurrency'
  AND value = '5';

UPDATE settings
SET value = '10'
WHERE key IN (
    'auth_source_default_dingtalk_concurrency',
    'auth_source_default_email_concurrency',
    'auth_source_default_github_concurrency',
    'auth_source_default_google_concurrency',
    'auth_source_default_linuxdo_concurrency',
    'auth_source_default_oidc_concurrency',
    'auth_source_default_wechat_concurrency'
)
  AND value = '5';
