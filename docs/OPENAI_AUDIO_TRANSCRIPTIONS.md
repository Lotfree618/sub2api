# OpenAI Audio Transcriptions

Sub2API exposes an OpenAI-compatible speech-to-text endpoint:

```text
POST /v1/audio/transcriptions
```

The endpoint is available only for groups whose platform is `openai`. It routes to active schedulable OpenAI OAuth accounts that support the `audio_transcriptions` capability.

## Request

Supported multipart fields:

- `file`: one audio file, required. Supported extensions are `flac`, `mp3`, `mp4`, `mpeg`, `mpga`, `m4a`, `ogg`, `wav`, and `webm`.
- `model`: required. `gpt-4o-transcribe` is the built-in default transcription model.
- `prompt`: optional.
- `response_format`: optional, must be `json` when present.

Unsupported OpenAI transcription parameters are rejected before upstream forwarding so billing and audit behavior stays explicit.

Example:

```bash
curl "$SUB2API_BASE_URL/v1/audio/transcriptions" \
  -H "Authorization: Bearer $SUB2API_KEY" \
  -F "model=gpt-4o-transcribe" \
  -F "file=@voice.wav"
```

## Model Mapping

For imported or newly created OpenAI OAuth accounts, include:

```text
gpt-4o-transcribe -> gpt-4o-transcribe
```

The account-level model mapping is applied before the upstream request, so groups can expose a public alias as long as it resolves to a supported transcription model.

## Billing

Audio transcription uses duration billing, not token billing. Sub2API extracts the billable audio duration from the uploaded file, rounds up to whole seconds, and writes the duration into `usage_logs.billable_duration_seconds`.

Pricing resolution order:

1. Channel model pricing with `billing_mode = duration`.
2. Built-in fallback for `gpt-4o-transcribe`: `$0.0001` per second.

Channel duration pricing uses the existing `per_request_price` field as the per-second unit price. When no duration price can be resolved for an audio transcription model, the request fails before settlement instead of silently falling back to token pricing.

Usage audit fields:

- `billing_mode = duration`
- `media_type = audio_transcription`
- `billable_duration_seconds = <rounded seconds>`
