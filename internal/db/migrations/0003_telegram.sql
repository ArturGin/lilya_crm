-- Telegram account linked to a user. Linked admins receive pipeline
-- notifications from the bot and can upload videos by sending them to it.
ALTER TABLE users ADD COLUMN telegram_id BIGINT UNIQUE;
