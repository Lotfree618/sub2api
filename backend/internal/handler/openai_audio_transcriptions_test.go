package handler

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIGatewayHandlerAudioTranscriptionsRejectsMissingModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	c, rec := newOpenAIAudioTranscriptionContext(t, &service.APIKey{ID: 12}, nil, "voice.wav", []byte("RIFF....WAVEfmt "))
	h := newOpenAIAudioTranscriptionValidationHandler()

	h.AudioTranscriptions(c)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Contains(t, rec.Body.String(), "model is required")
}

func TestOpenAIGatewayHandlerAudioTranscriptionsRejectsUnsupportedMultipartField(t *testing.T) {
	gin.SetMode(gin.TestMode)

	c, rec := newOpenAIAudioTranscriptionContext(t, &service.APIKey{ID: 13}, map[string]string{
		"model":    service.OpenAIAudioTranscriptionModel,
		"language": "en",
	}, "", nil)
	h := newOpenAIAudioTranscriptionValidationHandler()

	h.AudioTranscriptions(c)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Contains(t, rec.Body.String(), "language is not supported")
}

func TestOpenAIGatewayHandlerAudioTranscriptionsRejectsOversizedMultipartBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	fileBytes := bytes.Repeat([]byte("x"), service.OpenAIAudioTranscriptionMaxRequestBytes+1)
	c, rec := newOpenAIAudioTranscriptionContext(t, &service.APIKey{ID: 14}, map[string]string{
		"model": service.OpenAIAudioTranscriptionModel,
	}, "voice.wav", fileBytes)
	h := newOpenAIAudioTranscriptionValidationHandler()

	h.AudioTranscriptions(c)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Contains(t, rec.Body.String(), "failed to parse multipart form")
}

func TestRejectUnsupportedOpenAIAudioFields(t *testing.T) {
	t.Run("allows_public_gateway_fields", func(t *testing.T) {
		form := &multipart.Form{
			Value: map[string][]string{
				"model":           {service.OpenAIAudioTranscriptionModel},
				"prompt":          {"hint"},
				"response_format": {"json"},
			},
			File: map[string][]*multipart.FileHeader{
				"file": {},
			},
		}

		require.NoError(t, rejectUnsupportedOpenAIAudioFields(form))
	})

	t.Run("rejects_unsupported_values", func(t *testing.T) {
		form := &multipart.Form{
			Value: map[string][]string{"temperature": {"0"}},
			File:  map[string][]*multipart.FileHeader{},
		}

		require.ErrorContains(t, rejectUnsupportedOpenAIAudioFields(form), "temperature is not supported")
	})

	t.Run("rejects_unsupported_file_fields", func(t *testing.T) {
		form := &multipart.Form{
			Value: map[string][]string{},
			File:  map[string][]*multipart.FileHeader{"audio": {}},
		}

		require.ErrorContains(t, rejectUnsupportedOpenAIAudioFields(form), "audio is not supported")
	})
}

func newOpenAIAudioTranscriptionValidationHandler() *OpenAIGatewayHandler {
	return &OpenAIGatewayHandler{
		gatewayService:      &service.OpenAIGatewayService{},
		billingCacheService: &service.BillingCacheService{},
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   &ConcurrencyHelper{concurrencyService: &service.ConcurrencyService{}},
	}
}

func newOpenAIAudioTranscriptionContext(t *testing.T, apiKey *service.APIKey, fields map[string]string, fileName string, fileBytes []byte) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for k, v := range fields {
		require.NoError(t, writer.WriteField(k, v))
	}
	if fileName != "" || len(fileBytes) > 0 {
		part, err := writer.CreateFormFile("file", fileName)
		require.NoError(t, err)
		_, err = part.Write(fileBytes)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())

	groupID := int64(7)
	if apiKey == nil {
		apiKey = &service.APIKey{ID: 11}
	}
	apiKey.GroupID = &groupID
	apiKey.Group = &service.Group{ID: groupID, Platform: service.PlatformOpenAI}
	apiKey.User = &service.User{ID: 13}
	c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 13, Concurrency: 1})

	return c, rec
}
