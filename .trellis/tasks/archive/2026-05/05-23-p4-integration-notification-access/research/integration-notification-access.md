# Integration notification access research

## Current materialization points

- `model.Integration` encrypts `Endpoint`, `Secret`, and `ProxyURL` before save and decrypts them after normal GORM finds.
- Integration handlers currently load decrypted model values and return model instances after only Telegram-specific endpoint masking.
- Alert dispatch and retry workers intentionally need plaintext endpoint/proxy/secret values to send notifications.

## Exposure candidates

- API responses for List/Get/Create/Update/Patch/Test can serialize decrypted endpoint and proxy values.
- Non-Telegram webhook endpoints often place credentials in URL paths or query strings.
- Proxy URLs can contain credentials, paths, and query strings.
- Sender/test errors can include transport errors derived from endpoints or proxy configuration; these must be sanitized at response/log/persistence boundaries.

## Chosen P4 slice

Add a local response/error boundary around Integration notification access:

- Introduce sanitized response DTOs for integration API outputs.
- Mask endpoint and proxy URL values broadly, not just Telegram bot tokens.
- Keep sender/dispatcher inputs unchanged so delivery behavior stays intact.
- Sanitize sender errors before API responses, logs, or persisted LastError values.

## Deferred work

- External notification secret providers.
- Schema/API/frontend redesign for structured provider references.
- Changing delivery payloads or endpoint validation behavior.
- Retrofitting historical delivery records.
