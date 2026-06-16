package handler

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type openAIAudioTranscriptionRequest struct {
	Model                string
	UpstreamModel        string
	Prompt               string
	ResponseFormat       string
	FileName             string
	ContentType          string
	FileBytes            []byte
	FileSHA256           string
	BillableDurationSecs int
}

// AudioTranscriptions handles OpenAI-compatible audio transcription requests.
// POST /v1/audio/transcriptions
func (h *OpenAIGatewayHandler) AudioTranscriptions(c *gin.Context) {
	streamStarted := false
	requestStart := time.Now()

	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := requestLogger(
		c,
		"handler.openai_gateway.audio_transcriptions",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)
	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, service.OpenAIAudioTranscriptionMaxRequestBytes)
	if err := c.Request.ParseMultipartForm(service.OpenAIAudioTranscriptionMaxBytes); err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "failed to parse multipart form")
		return
	}

	parsed, err := h.parseOpenAIAudioTranscriptionRequest(c, apiKey)
	if err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	reqLog = reqLog.With(
		zap.String("model", parsed.Model),
		zap.String("upstream_model", parsed.UpstreamModel),
		zap.Int("billable_duration_seconds", parsed.BillableDurationSecs),
	)

	setOpsRequestContext(c, parsed.Model, false)
	setOpsEndpointContext(c, "", int16(service.RequestTypeSync))

	channelMapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, parsed.Model)

	if h.errorPassthroughService != nil {
		service.BindErrorPassthroughService(c, h.errorPassthroughService)
	}

	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	service.SetOpsLatencyMs(c, service.OpsAuthLatencyMsKey, time.Since(requestStart).Milliseconds())

	userReleaseFunc, acquired := h.acquireResponsesUserSlot(c, subject.UserID, subject.Concurrency, false, &streamStarted, reqLog)
	if !acquired {
		return
	}
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		reqLog.Info("openai_audio_transcriptions.billing_eligibility_check_failed", zap.Error(err))
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", fmt.Sprintf("%d", retryAfter))
		}
		h.errorResponse(c, status, code, message)
		return
	}

	requestPayloadHash := service.HashOpenAIAudioTranscriptionPayload(parsed.FileSHA256, parsed.Prompt, parsed.ResponseFormat, parsed.Model, parsed.UpstreamModel)
	sessionHash := h.gatewayService.GenerateSessionHashWithFallback(c, []byte(requestPayloadHash), requestPayloadHash)
	failedAccountIDs := make(map[int64]struct{})
	sameAccountRetryCount := make(map[int64]int)
	var lastFailoverErr *service.UpstreamFailoverError
	maxAccountSwitches := h.maxAccountSwitches
	if maxAccountSwitches <= 0 {
		maxAccountSwitches = 3
	}
	switchCount := 0
	routingStart := time.Now()

	for {
		selection, scheduleDecision, err := h.gatewayService.SelectAccountWithSchedulerForAudioTranscriptions(
			c.Request.Context(),
			apiKey.GroupID,
			sessionHash,
			parsed.Model,
			failedAccountIDs,
		)
		if err != nil {
			reqLog.Warn("openai_audio_transcriptions.account_select_failed",
				zap.Error(err),
				zap.Int("excluded_account_count", len(failedAccountIDs)),
			)
			if len(failedAccountIDs) == 0 {
				markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
				h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "No available compatible accounts")
				return
			}
			if lastFailoverErr != nil {
				h.handleFailoverExhausted(c, lastFailoverErr, false)
			} else {
				h.errorResponse(c, http.StatusBadGateway, "api_error", "Upstream request failed")
			}
			return
		}
		if selection == nil || selection.Account == nil {
			markOpsRoutingCapacityLimited(c)
			h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "No available compatible accounts")
			return
		}

		account := selection.Account
		sessionHash = ensureOpenAIPoolModeSessionHash(sessionHash, account)
		setOpsSelectedAccount(c, account.ID, account.Platform)
		reqLog.Debug("openai_audio_transcriptions.account_schedule_decision",
			zap.String("layer", scheduleDecision.Layer),
			zap.Bool("sticky_session_hit", scheduleDecision.StickySessionHit),
			zap.Int("candidate_count", scheduleDecision.CandidateCount),
			zap.Int("top_k", scheduleDecision.TopK),
			zap.Int64("latency_ms", scheduleDecision.LatencyMs),
			zap.Float64("load_skew", scheduleDecision.LoadSkew),
		)

		accountReleaseFunc, accountAcquired := h.acquireResponsesAccountSlot(c, apiKey.GroupID, sessionHash, selection, false, &streamStarted, reqLog)
		if !accountAcquired {
			return
		}
		forwardModel := service.ResolveOpenAIAudioTranscriptionForwardModel(account, parsed.Model, parsed.UpstreamModel)
		if strings.TrimSpace(forwardModel) == "" {
			forwardModel = parsed.UpstreamModel
		}

		service.SetOpsLatencyMs(c, service.OpsRoutingLatencyMsKey, time.Since(routingStart).Milliseconds())
		forwardStart := time.Now()
		output, forwardErr := func() (*service.OpenAIAudioTranscriptionOutput, error) {
			defer func() {
				if accountReleaseFunc != nil {
					accountReleaseFunc()
				}
			}()
			return h.gatewayService.ForwardAudioTranscription(c.Request.Context(), c, account, service.OpenAIAudioTranscriptionInput{
				RequestID:   openAIAudioRequestID(c),
				FileName:    parsed.FileName,
				ContentType: parsed.ContentType,
				FileBytes:   parsed.FileBytes,
				Prompt:      parsed.Prompt,
				Model:       forwardModel,
			})
		}()
		forwardDurationMs := time.Since(forwardStart).Milliseconds()
		upstreamLatencyMs, _ := getContextInt64(c, service.OpsUpstreamLatencyMsKey)
		responseLatencyMs := forwardDurationMs
		if upstreamLatencyMs > 0 && forwardDurationMs > upstreamLatencyMs {
			responseLatencyMs = forwardDurationMs - upstreamLatencyMs
		}
		service.SetOpsLatencyMs(c, service.OpsResponseLatencyMsKey, responseLatencyMs)

		if forwardErr != nil {
			var failoverErr *service.UpstreamFailoverError
			if errors.As(forwardErr, &failoverErr) {
				h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil)
				if failoverErr.RetryableOnSameAccount {
					retryLimit := account.GetPoolModeRetryCount()
					if sameAccountRetryCount[account.ID] < retryLimit {
						sameAccountRetryCount[account.ID]++
						reqLog.Warn("openai_audio_transcriptions.pool_mode_same_account_retry",
							zap.Int64("account_id", account.ID),
							zap.Int("upstream_status", failoverErr.StatusCode),
							zap.Int("retry_limit", retryLimit),
							zap.Int("retry_count", sameAccountRetryCount[account.ID]),
						)
						select {
						case <-c.Request.Context().Done():
							return
						case <-time.After(sameAccountRetryDelay):
						}
						continue
					}
				}
				h.gatewayService.RecordOpenAIAccountSwitch()
				failedAccountIDs[account.ID] = struct{}{}
				lastFailoverErr = failoverErr
				if switchCount >= maxAccountSwitches {
					h.handleFailoverExhausted(c, failoverErr, false)
					return
				}
				switchCount++
				if h.gatewayService.ShouldStopOpenAIOAuth429Failover(account, failoverErr.StatusCode, switchCount) {
					h.handleFailoverExhausted(c, failoverErr, false)
					return
				}
				reqLog.Warn("openai_audio_transcriptions.upstream_failover_switching",
					zap.Int64("account_id", account.ID),
					zap.Int("upstream_status", failoverErr.StatusCode),
					zap.Int("switch_count", switchCount),
					zap.Int("max_switches", maxAccountSwitches),
				)
				continue
			}
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil)
			reqLog.Warn("openai_audio_transcriptions.forward_failed", zap.Int64("account_id", account.ID), zap.Error(forwardErr))
			h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
			return
		}

		if output == nil || output.Result == nil {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil)
			h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
			return
		}
		h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, true, nil)
		if account.Type == service.AccountTypeOAuth {
			h.gatewayService.UpdateCodexUsageSnapshotFromHeaders(c.Request.Context(), account.ID, output.Result.ResponseHeaders)
		}
		if !openAIAudioResultHasUsage(output.Result) {
			output.Result.BillableDurationSeconds = parsed.BillableDurationSecs
		}

		userAgent := c.GetHeader("User-Agent")
		clientIP := ip.GetClientIP(c)
		inboundEndpoint := GetInboundEndpoint(c)
		upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)
		if err := h.gatewayService.RecordUsage(c.Request.Context(), &service.OpenAIRecordUsageInput{
			Result:             output.Result,
			APIKey:             apiKey,
			User:               apiKey.User,
			Account:            account,
			Subscription:       subscription,
			InboundEndpoint:    inboundEndpoint,
			UpstreamEndpoint:   upstreamEndpoint,
			UserAgent:          userAgent,
			IPAddress:          clientIP,
			RequestPayloadHash: requestPayloadHash,
			APIKeyService:      h.apiKeyService,
			ChannelUsageFields: channelMapping.ToUsageFields(parsed.Model, output.Result.UpstreamModel),
		}); err != nil {
			logger.L().With(
				zap.String("component", "handler.openai_gateway.audio_transcriptions"),
				zap.Int64("user_id", subject.UserID),
				zap.Int64("api_key_id", apiKey.ID),
				zap.Any("group_id", apiKey.GroupID),
				zap.String("model", parsed.Model),
				zap.Int64("account_id", account.ID),
			).Error("openai_audio_transcriptions.record_usage_failed", zap.Error(err))
			h.errorResponse(c, http.StatusInternalServerError, "api_error", "Failed to record usage")
			return
		}

		c.JSON(http.StatusOK, gin.H{"text": output.Text})
		reqLog.Debug("openai_audio_transcriptions.request_completed",
			zap.Int64("account_id", account.ID),
			zap.Int("switch_count", switchCount),
		)
		return
	}
}

func (h *OpenAIGatewayHandler) parseOpenAIAudioTranscriptionRequest(c *gin.Context, apiKey *service.APIKey) (*openAIAudioTranscriptionRequest, error) {
	if c == nil || c.Request == nil {
		return nil, fmt.Errorf("request is required")
	}
	form := c.Request.MultipartForm
	if form == nil {
		return nil, fmt.Errorf("multipart/form-data request is required")
	}
	if err := rejectUnsupportedOpenAIAudioFields(form); err != nil {
		return nil, err
	}
	model := strings.TrimSpace(c.PostForm("model"))
	if model == "" {
		return nil, fmt.Errorf("model is required")
	}
	responseFormat := strings.TrimSpace(c.PostForm("response_format"))
	if responseFormat == "" {
		responseFormat = "json"
	}
	if responseFormat != "json" {
		return nil, fmt.Errorf("response_format must be json")
	}

	channelMapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, model)
	upstreamModel := strings.TrimSpace(channelMapping.MappedModel)
	if upstreamModel == "" {
		upstreamModel = model
	}
	if !h.gatewayService.IsAudioTranscriptionModel(c.Request.Context(), apiKey.GroupID, upstreamModel) {
		return nil, fmt.Errorf("model must be an audio_transcription model")
	}

	fileHeader, err := firstMultipartFile(form, "file")
	if err != nil {
		return nil, err
	}
	contentType := strings.TrimSpace(fileHeader.Header.Get("Content-Type"))
	if !service.OpenAIAudioTranscriptionFileAllowed(fileHeader.Filename, contentType) {
		return nil, fmt.Errorf("unsupported audio file format")
	}
	file, err := fileHeader.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to open audio file")
	}
	defer file.Close()
	fileBytes, err := io.ReadAll(io.LimitReader(file, service.OpenAIAudioTranscriptionMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read audio file")
	}
	if len(fileBytes) == 0 {
		return nil, fmt.Errorf("audio file is required")
	}
	if len(fileBytes) > service.OpenAIAudioTranscriptionMaxBytes {
		return nil, fmt.Errorf("audio file exceeds limit")
	}
	durationSeconds, durationOK := service.ExtractBillableAudioDurationSeconds(fileBytes)
	if !durationOK || durationSeconds <= 0 {
		return nil, fmt.Errorf("unable to determine billable audio duration")
	}
	if !h.gatewayService.HasAudioTranscriptionDurationPricing(c.Request.Context(), apiKey.GroupID, upstreamModel) {
		return nil, fmt.Errorf("duration pricing is required for model")
	}

	fileHash := service.HashUsageRequestPayload(fileBytes)
	return &openAIAudioTranscriptionRequest{
		Model:                model,
		UpstreamModel:        upstreamModel,
		Prompt:               strings.TrimSpace(c.PostForm("prompt")),
		ResponseFormat:       responseFormat,
		FileName:             fileHeader.Filename,
		ContentType:          contentType,
		FileBytes:            fileBytes,
		FileSHA256:           fileHash,
		BillableDurationSecs: durationSeconds,
	}, nil
}

func rejectUnsupportedOpenAIAudioFields(form *multipart.Form) error {
	allowed := map[string]struct{}{
		"model":           {},
		"prompt":          {},
		"response_format": {},
	}
	for key := range form.Value {
		trimmed := strings.TrimSpace(key)
		if _, ok := allowed[trimmed]; !ok {
			return fmt.Errorf("%s is not supported for this audio transcription gateway", trimmed)
		}
	}
	for key := range form.File {
		if strings.TrimSpace(key) != "file" {
			return fmt.Errorf("%s is not supported for this audio transcription gateway", strings.TrimSpace(key))
		}
	}
	return nil
}

func firstMultipartFile(form *multipart.Form, name string) (*multipart.FileHeader, error) {
	files := form.File[name]
	if len(files) == 0 || files[0] == nil {
		return nil, fmt.Errorf("audio file is required")
	}
	if len(files) > 1 {
		return nil, fmt.Errorf("only one audio file is supported")
	}
	return files[0], nil
}

func openAIAudioResultHasUsage(result *service.OpenAIForwardResult) bool {
	if result == nil {
		return false
	}
	return result.Usage.InputTokens > 0 ||
		result.Usage.OutputTokens > 0 ||
		result.Usage.CacheCreationInputTokens > 0 ||
		result.Usage.CacheReadInputTokens > 0 ||
		result.Usage.ImageOutputTokens > 0 ||
		result.ImageCount > 0
}

func openAIAudioRequestID(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	if clientRequestID, ok := c.Request.Context().Value(ctxkey.ClientRequestID).(string); ok && strings.TrimSpace(clientRequestID) != "" {
		return strings.TrimSpace(clientRequestID)
	}
	if requestID, ok := c.Request.Context().Value(ctxkey.RequestID).(string); ok && strings.TrimSpace(requestID) != "" {
		return strings.TrimSpace(requestID)
	}
	return ""
}
