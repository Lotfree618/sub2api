package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIAudioTranscriptionFileAllowedUsesSharedRules(t *testing.T) {
	require.True(t, OpenAIAudioTranscriptionFileAllowed("recording.webm", "audio/webm; codecs=opus"))
	require.True(t, OpenAIAudioTranscriptionFileAllowed("voice.m4a", "application/octet-stream"))
	require.False(t, OpenAIAudioTranscriptionFileAllowed("notes.txt", "text/plain"))
	require.False(t, OpenAIAudioTranscriptionFileAllowed("voice.wav", "image/png"))
}

func TestOpenAIGatewayServiceIsAudioTranscriptionModelUsesDefaultFallback(t *testing.T) {
	svc := &OpenAIGatewayService{}

	require.True(t, svc.IsAudioTranscriptionModel(context.Background(), nil, OpenAIAudioTranscriptionModel))
	require.False(t, svc.IsAudioTranscriptionModel(context.Background(), nil, "gpt-5.4"))
}

func TestOpenAIGatewayServiceIsAudioTranscriptionModelUsesPricingMetadata(t *testing.T) {
	pricing := NewPricingService(&config.Config{}, nil)
	pricing.pricingData = map[string]*LiteLLMModelPricing{
		"custom-transcribe": {
			Mode:              "audio_transcription",
			LiteLLMProvider:   "openai",
			InputCostPerToken: 1e-6,
		},
		"gpt-5.4": {
			Mode:               "chat",
			LiteLLMProvider:    "openai",
			InputCostPerToken:  1e-6,
			OutputCostPerToken: 2e-6,
		},
	}
	pricing.lastUpdated = time.Now()
	billing := NewBillingService(&config.Config{}, pricing)
	svc := &OpenAIGatewayService{billingService: billing}

	require.True(t, svc.IsAudioTranscriptionModel(context.Background(), nil, "custom-transcribe"))
	require.False(t, svc.IsAudioTranscriptionModel(context.Background(), nil, "gpt-5.4"))
	require.False(t, svc.IsAudioTranscriptionModel(context.Background(), nil, "unknown-model"))
}

func TestOpenAIGatewayServiceAudioTranscriptionDurationPricingUsesDefaultAndChannelOverride(t *testing.T) {
	groupID := int64(91)
	price := 0.0002
	cache := newEmptyChannelCache()
	cache.pricingByGroupModel[channelModelKey{groupID: groupID, model: OpenAIAudioTranscriptionModel}] = &ChannelModelPricing{
		BillingMode:     BillingModeDuration,
		PerRequestPrice: &price,
	}
	cache.channelByGroupID[groupID] = &Channel{ID: groupID, Status: StatusActive}
	cache.loadedAt = time.Now()
	channelService := &ChannelService{}
	channelService.cache.Store(cache)
	svc := &OpenAIGatewayService{
		resolver: NewModelPricingResolver(channelService, NewBillingService(&config.Config{}, nil)),
	}

	require.True(t, svc.HasAudioTranscriptionDurationPricing(context.Background(), nil, OpenAIAudioTranscriptionModel))
	resolvedDefault := svc.resolveOpenAIAudioDurationPricing(context.Background(), nil, OpenAIAudioTranscriptionModel)
	require.NotNil(t, resolvedDefault)
	require.Equal(t, BillingModeDuration, resolvedDefault.Mode)
	require.InDelta(t, OpenAIAudioTranscriptionDefaultUSDPerSec, resolvedDefault.DefaultPerRequestPrice, 1e-12)

	resolvedChannel := svc.resolveOpenAIAudioDurationPricing(context.Background(), &groupID, OpenAIAudioTranscriptionModel)
	require.NotNil(t, resolvedChannel)
	require.Equal(t, PricingSourceChannel, resolvedChannel.Source)
	require.InDelta(t, price, resolvedChannel.DefaultPerRequestPrice, 1e-12)
	require.False(t, svc.HasAudioTranscriptionDurationPricing(context.Background(), &groupID, "gpt-5.4"))
}

func TestResolveOpenAIAudioTranscriptionForwardModelUsesAccountMappingOverChannelDefault(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"transcribe-public": OpenAIAudioTranscriptionModel,
			},
		},
	}

	require.Equal(t, OpenAIAudioTranscriptionModel, ResolveOpenAIAudioTranscriptionForwardModel(account, "transcribe-public", "gpt-4o-mini-transcribe"))
	require.Equal(t, "gpt-4o-mini-transcribe", ResolveOpenAIAudioTranscriptionForwardModel(&Account{}, "transcribe-public", "gpt-4o-mini-transcribe"))
}
