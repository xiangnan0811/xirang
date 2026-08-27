package content

import (
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
)

func TestDeliveryResourceAcceptsOneClosedResourceArm(t *testing.T) {
	asset := backupasset.AssetRef{RecoveryPointID: strings.Repeat("a", 32), EntryID: strings.Repeat("b", 64)}
	recovery := RecoveryResultRef{RecoveryJobID: strings.Repeat("c", 32), ResultID: strings.Repeat("d", 32)}
	tests := []struct {
		name    string
		value   DeliveryResource
		wantErr error
	}{
		{name: "backup asset", value: DeliveryResource{Kind: DeliveryResourceBackupAsset, Asset: &asset}},
		{name: "empty", value: DeliveryResource{}, wantErr: ErrInvalidDeliveryResource},
		{name: "missing asset", value: DeliveryResource{Kind: DeliveryResourceBackupAsset}, wantErr: ErrInvalidDeliveryResource},
		{name: "dual", value: DeliveryResource{Kind: DeliveryResourceBackupAsset, Asset: &asset, RecoveryResult: &recovery}, wantErr: ErrInvalidDeliveryResource},
		{name: "recovery result", value: DeliveryResource{Kind: DeliveryResourceRecoveryResult, RecoveryResult: &recovery}},
		{name: "recovery result missing ref", value: DeliveryResource{Kind: DeliveryResourceRecoveryResult}, wantErr: ErrInvalidDeliveryResource},
		{name: "recovery result path id", value: DeliveryResource{Kind: DeliveryResourceRecoveryResult, RecoveryResult: &RecoveryResultRef{
			RecoveryJobID: recovery.RecoveryJobID, ResultID: "../result",
		}}, wantErr: ErrInvalidDeliveryResource},
		{name: "unknown", value: DeliveryResource{Kind: "future", Asset: &asset}, wantErr: ErrInvalidDeliveryResource},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateDeliveryResource(test.value)
			if test.wantErr == nil && err != nil {
				t.Fatalf("ValidateDeliveryResource() error = %v", err)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("ValidateDeliveryResource() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestRecoveryResultDeliveryProductRequiresExactDownloadPurpose(t *testing.T) {
	now := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	expires := now.Add(2 * time.Minute)
	recoveryProof := &StepUpProof{
		Action: auth.StepUpActionRecoveryResultDownload, ID: strings.Repeat("a", 32), ExpiresAt: expires,
	}
	assetProof := &StepUpProof{
		Action: auth.StepUpActionAssetDownload, ID: strings.Repeat("b", 32), ExpiresAt: expires,
	}
	base := DeliveryProduct{
		Action: DeliveryDownload, Method: MethodGetHead, Range: RangeSingle,
		Renderer: RendererAttachment, Profile: ProfileOriginalV1,
		Classification: ClassificationUnknown, Proof: recoveryProof, AbsoluteExpiresAt: now.Add(time.Minute),
	}
	tests := []struct {
		name  string
		edit  func(DeliveryProduct) DeliveryProduct
		valid bool
	}{
		{name: "exact recovery proof", edit: func(product DeliveryProduct) DeliveryProduct { return product }, valid: true},
		{name: "non secret still requires proof", edit: func(product DeliveryProduct) DeliveryProduct {
			product.Classification = ClassificationNonSecret
			product.Proof = nil
			return product
		}},
		{name: "asset download proof is insufficient", edit: func(product DeliveryProduct) DeliveryProduct {
			product.Proof = assetProof
			return product
		}},
		{name: "preview is forbidden", edit: func(product DeliveryProduct) DeliveryProduct {
			product.Action = DeliveryPreview
			return product
		}},
		{name: "inline renderer is forbidden", edit: func(product DeliveryProduct) DeliveryProduct {
			product.Renderer = RendererEscapedText
			product.Profile = ProfileTextV1
			product.Range = RangeNone
			return product
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateDeliveryProductForResource(
				DeliveryResourceRecoveryResult, test.edit(base), now,
			)
			if test.valid && err != nil {
				t.Fatalf("ValidateDeliveryProductForResource() error = %v", err)
			}
			if !test.valid && !errors.Is(err, ErrInvalidDeliveryProduct) {
				t.Fatalf("ValidateDeliveryProductForResource() error = %v, want invalid product", err)
			}
		})
	}
}

func TestDeliveryProductClosesRendererAndProofPurposes(t *testing.T) {
	now := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	expires := now.Add(2 * time.Minute)
	secretProof := &StepUpProof{Action: auth.StepUpActionAssetSecretReveal, ID: strings.Repeat("c", 32), ExpiresAt: expires}
	downloadProof := &StepUpProof{Action: auth.StepUpActionAssetDownload, ID: strings.Repeat("d", 32), ExpiresAt: expires}
	base := DeliveryProduct{
		Action: DeliveryPreview, Method: MethodGetHead, Range: RangeNone,
		Renderer: RendererEscapedText, Profile: ProfileTextV1,
		Classification: ClassificationNonSecret, AbsoluteExpiresAt: now.Add(time.Minute),
	}
	tests := []struct {
		name  string
		edit  func(DeliveryProduct) DeliveryProduct
		valid bool
	}{
		{name: "non secret preview", edit: func(v DeliveryProduct) DeliveryProduct { return v }, valid: true},
		{name: "faithful plain text preview", edit: func(v DeliveryProduct) DeliveryProduct {
			v.Renderer = RendererPlainText
			v.Profile = ProfileTextV2
			return v
		}, valid: true},
		{name: "secret exact proof", edit: func(v DeliveryProduct) DeliveryProduct {
			v.Classification = ClassificationSecret
			v.Proof = secretProof
			return v
		}, valid: true},
		{name: "unknown exact proof", edit: func(v DeliveryProduct) DeliveryProduct {
			v.Classification = ClassificationUnknown
			v.Proof = secretProof
			return v
		}, valid: true},
		{name: "download exact proof", edit: func(v DeliveryProduct) DeliveryProduct {
			v.Action = DeliveryDownload
			v.Renderer = RendererAttachment
			v.Profile = ProfileOriginalV1
			v.Range = RangeSingle
			v.Proof = downloadProof
			return v
		}, valid: true},
		{name: "non secret with proof", edit: func(v DeliveryProduct) DeliveryProduct { v.Proof = secretProof; return v }},
		{name: "secret without proof", edit: func(v DeliveryProduct) DeliveryProduct { v.Classification = ClassificationSecret; return v }},
		{name: "secret download proof", edit: func(v DeliveryProduct) DeliveryProduct {
			v.Classification = ClassificationSecret
			v.Proof = downloadProof
			return v
		}},
		{name: "download secret proof", edit: func(v DeliveryProduct) DeliveryProduct {
			v.Action = DeliveryDownload
			v.Renderer = RendererAttachment
			v.Profile = ProfileOriginalV1
			v.Proof = secretProof
			return v
		}},
		{name: "download no proof", edit: func(v DeliveryProduct) DeliveryProduct {
			v.Action = DeliveryDownload
			v.Renderer = RendererAttachment
			v.Profile = ProfileOriginalV1
			return v
		}},
		{name: "expired proof", edit: func(v DeliveryProduct) DeliveryProduct {
			v.Classification = ClassificationUnknown
			v.Proof = &StepUpProof{Action: auth.StepUpActionAssetSecretReveal, ID: strings.Repeat("e", 32), ExpiresAt: now}
			return v
		}},
		{name: "wrong profile", edit: func(v DeliveryProduct) DeliveryProduct { v.Profile = ProfileVideoV1; return v }},
		{name: "text range", edit: func(v DeliveryProduct) DeliveryProduct { v.Range = RangeSingle; return v }},
		{name: "attachment preview", edit: func(v DeliveryProduct) DeliveryProduct {
			v.Renderer = RendererAttachment
			v.Profile = ProfileOriginalV1
			return v
		}},
		{name: "unknown renderer", edit: func(v DeliveryProduct) DeliveryProduct { v.Renderer = "future"; return v }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateDeliveryProduct(test.edit(base), now)
			if test.valid && err != nil {
				t.Fatalf("ValidateDeliveryProduct() error = %v", err)
			}
			if !test.valid && !errors.Is(err, ErrInvalidDeliveryProduct) {
				t.Fatalf("ValidateDeliveryProduct() error = %v, want invalid product", err)
			}
		})
	}
}

func TestDeliveryStateTransitionsAreTerminal(t *testing.T) {
	valid := [][2]DeliveryState{
		{DeliveryIssued, DeliveryActive}, {DeliveryIssued, DeliveryRevoked},
		{DeliveryActive, DeliveryDraining}, {DeliveryActive, DeliveryClosed},
		{DeliveryActive, DeliveryRevoked}, {DeliveryActive, DeliveryExpired},
		{DeliveryDraining, DeliveryRevoked}, {DeliveryDraining, DeliveryExpired},
	}
	for _, pair := range valid {
		if err := ValidateDeliveryTransition(pair[0], pair[1]); err != nil {
			t.Fatalf("transition %s -> %s rejected: %v", pair[0], pair[1], err)
		}
	}
	for _, terminal := range []DeliveryState{DeliveryRevoked, DeliveryExpired, DeliveryClosed} {
		if err := ValidateDeliveryTransition(terminal, DeliveryActive); !errors.Is(err, ErrInvalidDeliveryState) {
			t.Fatalf("terminal transition %s -> active error = %v", terminal, err)
		}
	}
}
