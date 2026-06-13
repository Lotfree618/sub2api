package service

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type audioTranscriptionHTTPUpstream struct {
	resp     *http.Response
	err      error
	lastReq  *http.Request
	lastBody []byte
}

func (u *audioTranscriptionHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.lastReq = req
	if req != nil && req.Body != nil {
		u.lastBody, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(u.lastBody))
	}
	if u.err != nil {
		return nil, u.err
	}
	if u.resp != nil {
		return u.resp, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"text":"hello"}`)),
	}, nil
}

func (u *audioTranscriptionHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func TestParseOpenAIAudioTranscriptionRequestValidatesStrictFields(t *testing.T) {
	body, contentType := buildAudioTranscriptionTestMultipart(t, map[string]string{
		"model":  OpenAIAudioTranscriptionModel,
		"prompt": "domain words",
	}, []byte("RIFF....WAVEfmt "))

	parsed, err := (&OpenAIGatewayService{}).ParseOpenAIAudioTranscriptionRequest(body, contentType)

	require.NoError(t, err)
	require.Equal(t, OpenAIAudioTranscriptionModel, parsed.Model)
	require.Equal(t, "domain words", parsed.Prompt)
	require.Equal(t, "voice.wav", parsed.FileName)
	require.Equal(t, "audio/wav", parsed.ContentType)
	require.Equal(t, []byte("RIFF....WAVEfmt "), parsed.FileBytes)
}

func TestParseOpenAIAudioTranscriptionRequestRejectsUnsupportedModelAndStream(t *testing.T) {
	tests := []struct {
		name      string
		fields    map[string]string
		wantParam string
	}{
		{
			name:      "unsupported model",
			fields:    map[string]string{"model": "gpt-4o-mini-transcribe"},
			wantParam: "model",
		},
		{
			name:      "stream field",
			fields:    map[string]string{"model": OpenAIAudioTranscriptionModel, "stream": "true"},
			wantParam: "stream",
		},
		{
			name:      "language field",
			fields:    map[string]string{"model": OpenAIAudioTranscriptionModel, "language": "zh"},
			wantParam: "language",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, contentType := buildAudioTranscriptionTestMultipart(t, tt.fields, []byte("audio bytes"))

			_, err := (&OpenAIGatewayService{}).ParseOpenAIAudioTranscriptionRequest(body, contentType)

			var reqErr *OpenAIAudioTranscriptionRequestError
			require.ErrorAs(t, err, &reqErr)
			require.Equal(t, http.StatusBadRequest, reqErr.Status)
			require.Equal(t, "invalid_request_error", reqErr.Type)
			require.Equal(t, tt.wantParam, reqErr.Param)
		})
	}
}

func TestParseOpenAIAudioTranscriptionRequestRejectsMissingFileOrModel(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		require.NoError(t, writer.WriteField("model", OpenAIAudioTranscriptionModel))
		require.NoError(t, writer.Close())

		_, err := (&OpenAIGatewayService{}).ParseOpenAIAudioTranscriptionRequest(buf.Bytes(), writer.FormDataContentType())

		var reqErr *OpenAIAudioTranscriptionRequestError
		require.ErrorAs(t, err, &reqErr)
		require.Equal(t, "file", reqErr.Param)
	})

	t.Run("missing model", func(t *testing.T) {
		body, contentType := buildAudioTranscriptionTestMultipart(t, nil, []byte("audio bytes"))

		_, err := (&OpenAIGatewayService{}).ParseOpenAIAudioTranscriptionRequest(body, contentType)

		var reqErr *OpenAIAudioTranscriptionRequestError
		require.ErrorAs(t, err, &reqErr)
		require.Equal(t, "model", reqErr.Param)
	})
}

func TestForwardAudioTranscriptionAPIKeyIncludesModelPromptAndUsesOfficialEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := newAudioTranscriptionGinContext(t)
	upstream := &audioTranscriptionHTTPUpstream{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"up_req_1"}},
		Body:       io.NopCloser(strings.NewReader(`{"text":"hello","usage":{"input_tokens":3,"output_tokens":1}}`)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID:       11,
		Type:     AccountTypeAPIKey,
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"api_key":    "sk-upstream",
			"base_url":   "https://api.openai.test/v1",
			"user_agent": "AudioTest/1.0",
		},
	}

	output, err := svc.ForwardAudioTranscription(context.Background(), c, account, &OpenAIAudioTranscriptionRequest{
		FileName:    "voice.wav",
		ContentType: "audio/wav",
		FileBytes:   []byte("audio bytes"),
		Model:       OpenAIAudioTranscriptionModel,
		Prompt:      "hint",
	})

	require.NoError(t, err)
	require.Equal(t, "hello", output.Text)
	require.Equal(t, "https://api.openai.test/v1/audio/transcriptions", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer sk-upstream", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "AudioTest/1.0", upstream.lastReq.Header.Get("User-Agent"))
	parts := readAudioTranscriptionMultipartParts(t, upstream.lastReq.Header.Get("Content-Type"), upstream.lastBody)
	require.Equal(t, OpenAIAudioTranscriptionModel, parts["model"])
	require.Equal(t, "hint", parts["prompt"])
	require.Equal(t, "audio bytes", parts["file"])
	require.True(t, OpenAIUsageHasBillableTokens(output.Result.Usage))
	require.Zero(t, output.Result.BillableDurationSeconds)
}

func TestForwardAudioTranscriptionOAuthOmitsModelAndClientHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := newAudioTranscriptionGinContext(t)
	c.Request.Header.Set("Authorization", "Bearer client-key")
	c.Request.Header.Set("x-codex-turn-state", "client-controlled")
	upstream := &audioTranscriptionHTTPUpstream{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"text":"hello"}`)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID:       12,
		Type:     AccountTypeOAuth,
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
			"user_agent":         "Codex-Test-UA/1.0",
		},
	}

	output, err := svc.ForwardAudioTranscription(context.Background(), c, account, &OpenAIAudioTranscriptionRequest{
		FileName:        "voice.wav",
		ContentType:     "audio/wav",
		FileBytes:       []byte("audio bytes"),
		Model:           OpenAIAudioTranscriptionModel,
		Prompt:          "hint",
		DurationParsed:  true,
		DurationSeconds: 7,
	})

	require.NoError(t, err)
	require.Equal(t, "hello", output.Text)
	require.Equal(t, chatgptAudioTranscriptionUpstream, upstream.lastReq.URL.String())
	require.Equal(t, "chatgpt.com", upstream.lastReq.Host)
	require.Equal(t, "Bearer oauth-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "chatgpt-acc", upstream.lastReq.Header.Get("chatgpt-account-id"))
	require.Equal(t, "Codex-Test-UA/1.0", upstream.lastReq.Header.Get("User-Agent"))
	require.Empty(t, upstream.lastReq.Header.Get("x-codex-turn-state"))
	parts := readAudioTranscriptionMultipartParts(t, upstream.lastReq.Header.Get("Content-Type"), upstream.lastBody)
	require.NotContains(t, parts, "model")
	require.Equal(t, "hint", parts["prompt"])
	require.Equal(t, "audio bytes", parts["file"])
	require.Equal(t, 7, output.Result.BillableDurationSeconds)
}

func TestCanBillOpenAIAudioDurationRequiresDurationPricing(t *testing.T) {
	groupID := int64(42)
	price := 0.003
	cache := newEmptyChannelCache()
	cache.pricingByGroupModel[channelModelKey{groupID: groupID, platform: PlatformOpenAI, model: OpenAIAudioTranscriptionModel}] = &ChannelModelPricing{
		BillingMode:     BillingModeDuration,
		PerRequestPrice: &price,
	}
	cache.channelByGroupID[groupID] = &Channel{ID: 7, Status: StatusActive}
	cache.groupPlatform[groupID] = PlatformOpenAI
	cache.loadedAt = time.Now()
	channelService := &ChannelService{}
	channelService.cache.Store(cache)
	svc := &OpenAIGatewayService{
		resolver:       NewModelPricingResolver(channelService, NewBillingService(&config.Config{}, nil)),
		channelService: channelService,
	}
	apiKey := &APIKey{GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformOpenAI}}

	require.True(t, svc.CanBillOpenAIAudioDuration(context.Background(), apiKey, OpenAIAudioTranscriptionModel, 3))
	require.False(t, svc.CanBillOpenAIAudioDuration(context.Background(), apiKey, OpenAIAudioTranscriptionModel, 0))
	require.False(t, (&OpenAIGatewayService{}).CanBillOpenAIAudioDuration(context.Background(), apiKey, OpenAIAudioTranscriptionModel, 3))
}

func TestOpenAIEndpointCapabilityAudioTranscriptionsSupportsAPIKeyAndOAuth(t *testing.T) {
	require.True(t, (&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}).SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityAudioTranscribe))
	require.True(t, (&Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}).SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityAudioTranscribe))
	require.False(t, (&Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}).SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityAudioTranscribe))
	require.False(t, (&Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			openAIEndpointCapabilitiesCredentialKey: []any{string(OpenAIEndpointCapabilityChatCompletions)},
		},
	}).SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityAudioTranscribe))
}

func TestCalculateCostUnifiedDurationMode(t *testing.T) {
	bs := NewBillingService(&config.Config{}, nil)
	resolver := NewModelPricingResolver(nil, bs)

	cost, err := bs.CalculateCostUnified(CostInput{
		Ctx:             context.Background(),
		Model:           OpenAIAudioTranscriptionModel,
		DurationSeconds: 9,
		RateMultiplier:  2,
		Resolver:        resolver,
		Resolved: &ResolvedPricing{
			Mode:                   BillingModeDuration,
			DefaultPerRequestPrice: 0.01,
		},
	})

	require.NoError(t, err)
	require.Equal(t, string(BillingModeDuration), cost.BillingMode)
	require.InDelta(t, 0.09, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.18, cost.ActualCost, 1e-12)
}

func TestUsageBillingFingerprintIncludesBillableDurationSeconds(t *testing.T) {
	base := &UsageBillingCommand{
		UserID:                  1,
		AccountID:               2,
		APIKeyID:                3,
		AccountType:             string(AccountTypeOAuth),
		Model:                   OpenAIAudioTranscriptionModel,
		BillingType:             BillingTypeBalance,
		BillableDurationSeconds: 7,
		BalanceCost:             0.07,
		RequestPayloadHash:      "payload",
	}
	other := *base
	other.BillableDurationSeconds = 8

	require.NotEqual(t, buildUsageBillingFingerprint(base), buildUsageBillingFingerprint(&other))
}

func buildAudioTranscriptionTestMultipart(t *testing.T, fields map[string]string, fileBytes []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="file"; filename="voice.wav"`)
	header.Set("Content-Type", "audio/wav")
	part, err := writer.CreatePart(header)
	require.NoError(t, err)
	_, err = part.Write(fileBytes)
	require.NoError(t, err)
	for key, value := range fields {
		require.NoError(t, writer.WriteField(key, value))
	}
	require.NoError(t, writer.Close())
	return buf.Bytes(), writer.FormDataContentType()
}

func newAudioTranscriptionGinContext(t *testing.T) *gin.Context {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	c.Request.Header.Set("User-Agent", "OpenAI-Go-Test/1.0")
	return c
}

func readAudioTranscriptionMultipartParts(t *testing.T, contentType string, body []byte) map[string]string {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(contentType)
	require.NoError(t, err)
	require.Equal(t, "multipart/form-data", mediaType)
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	parts := map[string]string{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		data, err := io.ReadAll(part)
		require.NoError(t, err)
		parts[part.FormName()] = string(data)
		require.NoError(t, part.Close())
	}
	return parts
}
