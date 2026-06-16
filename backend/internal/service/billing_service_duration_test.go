package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCalculateCostUnified_DurationModeChargesPerSecond(t *testing.T) {
	svc := &BillingService{}

	cost, err := svc.CalculateCostUnified(CostInput{
		Ctx:             context.Background(),
		Model:           OpenAIAudioTranscriptionModel,
		DurationSeconds: 12,
		RateMultiplier:  1.5,
		Resolved: &ResolvedPricing{
			Mode:                   BillingModeDuration,
			DefaultPerRequestPrice: 0.003,
			Source:                 PricingSourceChannel,
		},
	})

	require.NoError(t, err)
	require.Equal(t, string(BillingModeDuration), cost.BillingMode)
	require.InDelta(t, 0.036, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.054, cost.ActualCost, 1e-12)
}

func TestCalculateCostUnified_DurationModeRequiresDurationAndPrice(t *testing.T) {
	svc := &BillingService{}
	resolved := &ResolvedPricing{
		Mode:   BillingModeDuration,
		Source: PricingSourceChannel,
	}

	_, err := svc.CalculateCostUnified(CostInput{
		Ctx:      context.Background(),
		Model:    OpenAIAudioTranscriptionModel,
		Resolved: resolved,
	})
	require.ErrorIs(t, err, ErrModelPricingUnavailable)

	resolved.DefaultPerRequestPrice = 0.003
	_, err = svc.CalculateCostUnified(CostInput{
		Ctx:      context.Background(),
		Model:    OpenAIAudioTranscriptionModel,
		Resolved: resolved,
	})
	require.ErrorIs(t, err, ErrModelPricingUnavailable)
}
