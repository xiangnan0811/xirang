-- Widen integrations.endpoint from VARCHAR(1024) to TEXT.
-- Reason: the column stores the AES-GCM encrypted ciphertext (base64 + IV + tag,
-- ~33% inflation over plaintext). For long webhook URLs (Slack/钉钉/飞书 with
-- query tokens) the ciphertext can exceed 1024 bytes and trigger PG's hard length
-- check. Secret/proxy_url were widened to TEXT in earlier migrations (000013/000029);
-- this aligns endpoint with the same convention.
-- ALTER COLUMN TYPE in the same family is metadata-only on PG (no table rewrite).
ALTER TABLE integrations ALTER COLUMN endpoint TYPE TEXT;
