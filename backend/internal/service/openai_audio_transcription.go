package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

const (
	OpenAIAudioTranscriptionModel              = "gpt-4o-transcribe"
	OpenAIAudioTranscriptionDefaultUSDPerSec   = 0.0001
	OpenAIAudioTranscriptionMaxBytes           = 25 << 20
	OpenAIAudioTranscriptionMaxRequestBytes    = OpenAIAudioTranscriptionMaxBytes + (1 << 20)
	openAIAudioTranscriptionPricingSourceLocal = "fallback"
)

func OpenAIAudioTranscriptionFileAllowed(filename, contentType string) bool {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(strings.TrimSpace(filename))), ".")
	switch ext {
	case "flac", "mp3", "mp4", "mpeg", "mpga", "m4a", "ogg", "wav", "webm":
	default:
		return false
	}
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return contentType == "" ||
		strings.HasPrefix(contentType, "audio/") ||
		contentType == "video/mp4" ||
		contentType == "video/webm" ||
		contentType == "application/octet-stream"
}

func HashOpenAIAudioTranscriptionPayload(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		hash.Write([]byte(strings.TrimSpace(part)))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (s *OpenAIGatewayService) SelectAccountWithSchedulerForAudioTranscriptions(
	ctx context.Context,
	groupID *int64,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	return s.SelectAccountWithSchedulerForCapability(
		ctx,
		groupID,
		"",
		sessionHash,
		requestedModel,
		excludedIDs,
		OpenAIUpstreamTransportAny,
		OpenAIEndpointCapabilityAudioTranscribe,
		false,
	)
}

func (s *OpenAIGatewayService) IsAudioTranscriptionModel(ctx context.Context, groupID *int64, model string) bool {
	model = strings.TrimSpace(model)
	if s == nil || model == "" {
		return false
	}
	if resolved := s.resolveOpenAIAudioDurationPricing(ctx, groupID, model); resolved != nil && resolved.Mode == BillingModeDuration {
		return true
	}
	if s.billingService != nil && s.billingService.pricingService != nil {
		pricing := s.billingService.pricingService.GetModelPricing(model)
		return pricing != nil && strings.EqualFold(strings.TrimSpace(pricing.Mode), "audio_transcription")
	}
	return false
}

func (s *OpenAIGatewayService) HasAudioTranscriptionDurationPricing(ctx context.Context, groupID *int64, model string) bool {
	resolved := s.resolveOpenAIAudioDurationPricing(ctx, groupID, model)
	return resolved != nil && resolved.Mode == BillingModeDuration && resolved.DefaultPerRequestPrice > 0
}

func (s *OpenAIGatewayService) resolveOpenAIAudioDurationPricing(ctx context.Context, groupID *int64, model string) *ResolvedPricing {
	model = strings.TrimSpace(model)
	if s == nil || model == "" {
		return nil
	}
	if s.resolver != nil && groupID != nil {
		resolved := s.resolver.Resolve(ctx, PricingInput{
			Model:   model,
			GroupID: groupID,
		})
		if resolved != nil && resolved.Source == PricingSourceChannel && resolved.Mode == BillingModeDuration {
			return resolved
		}
	}
	if price, ok := DefaultOpenAIAudioTranscriptionDurationPrice(model); ok {
		return &ResolvedPricing{
			Mode:                   BillingModeDuration,
			DefaultPerRequestPrice: price,
			Source:                 openAIAudioTranscriptionPricingSourceLocal,
		}
	}
	return nil
}

func DefaultOpenAIAudioTranscriptionDurationPrice(model string) (float64, bool) {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case OpenAIAudioTranscriptionModel:
		return OpenAIAudioTranscriptionDefaultUSDPerSec, true
	default:
		return 0, false
	}
}

func ResolveOpenAIAudioTranscriptionForwardModel(account *Account, requestedModel, defaultMappedModel string) string {
	requestedModel = strings.TrimSpace(requestedModel)
	defaultMappedModel = strings.TrimSpace(defaultMappedModel)
	if account != nil {
		if mappedModel, matched := account.ResolveMappedModel(requestedModel); matched {
			return strings.TrimSpace(mappedModel)
		}
	}
	if defaultMappedModel != "" {
		return defaultMappedModel
	}
	return requestedModel
}

func groupIDFromAPIKey(apiKey *APIKey) *int64 {
	if apiKey == nil {
		return nil
	}
	if apiKey.Group != nil {
		groupID := apiKey.Group.ID
		return &groupID
	}
	return apiKey.GroupID
}
