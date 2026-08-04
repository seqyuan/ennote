-- Activate the format-2 speaker-ledger writer for Direct Role invocations after
-- qualification. Host Runs keep format 1 so the Conversation Surface tool
-- timeline remains intact; format-aware readers support both.

UPDATE settings
SET value='2'
WHERE key='hosted_commit_format_version' AND value='1';

CREATE TRIGGER hosted_commit_format_setting_validate_insert
BEFORE INSERT ON settings
WHEN NEW.key='hosted_commit_format_version' AND NEW.value NOT IN ('1','2')
BEGIN
    SELECT RAISE(ABORT, 'hosted_commit_format_setting_invalid');
END;

CREATE TRIGGER hosted_commit_format_setting_validate_update
BEFORE UPDATE OF value ON settings
WHEN NEW.key='hosted_commit_format_version' AND NEW.value NOT IN ('1','2')
BEGIN
    SELECT RAISE(ABORT, 'hosted_commit_format_setting_invalid');
END;
