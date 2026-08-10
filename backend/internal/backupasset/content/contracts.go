package content

import (
	"errors"
	"fmt"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
)

var (
	ErrInvalidDeliveryResource = errors.New("invalid delivery resource")
	ErrInvalidDeliveryProduct  = errors.New("invalid delivery product")
	ErrInvalidDeliveryState    = errors.New("invalid delivery state")
	ErrInvalidDeliveryCookie   = errors.New("invalid delivery cookie")
)

type DeliveryResourceKind string

const (
	DeliveryResourceBackupAsset    DeliveryResourceKind = "backup_asset"
	DeliveryResourceRecoveryResult DeliveryResourceKind = "recovery_result"
)

type DeliveryAction string

const (
	DeliveryPreview  DeliveryAction = "preview"
	DeliveryDownload DeliveryAction = "download"
)

type Renderer string

const (
	RendererEscapedText   Renderer = "escaped_text"
	RendererSafeRaster    Renderer = "safe_raster"
	RendererSameOriginPDF Renderer = "same_origin_pdf"
	RendererNativeAudio   Renderer = "native_audio"
	RendererNativeVideo   Renderer = "native_video"
	RendererMetadataHex   Renderer = "metadata_hex"
	RendererAttachment    Renderer = "attachment"
)

type RendererProfile string

const (
	ProfileTextV1     RendererProfile = "text_v1"
	ProfileRasterV1   RendererProfile = "raster_v1"
	ProfilePDFV1      RendererProfile = "pdf_v1"
	ProfileAudioV1    RendererProfile = "audio_v1"
	ProfileVideoV1    RendererProfile = "video_v1"
	ProfileHexV1      RendererProfile = "hex_v1"
	ProfileOriginalV1 RendererProfile = "original_v1"
)

type RangePolicy string

const (
	RangeNone   RangePolicy = "none"
	RangeSingle RangePolicy = "single"
)

type MethodPolicy string

const MethodGetHead MethodPolicy = "get_head"

type Classification string

const (
	ClassificationNonSecret Classification = "non_secret"
	ClassificationSecret    Classification = "secret"
	ClassificationUnknown   Classification = "unknown"
)

type DeliveryState string

const (
	DeliveryIssued   DeliveryState = "issued"
	DeliveryActive   DeliveryState = "active"
	DeliveryDraining DeliveryState = "draining"
	DeliveryRevoked  DeliveryState = "revoked"
	DeliveryExpired  DeliveryState = "expired"
	DeliveryClosed   DeliveryState = "closed"
)

type RecoveryResultRef struct {
	RecoveryJobID string
	ResultID      string
}

type DeliveryResource struct {
	Kind           DeliveryResourceKind
	Asset          *backupasset.AssetRef
	RecoveryResult *RecoveryResultRef
}

type StepUpProof struct {
	Action    auth.StepUpAction
	ID        string
	ExpiresAt time.Time
}

type DeliveryProduct struct {
	Action            DeliveryAction
	Method            MethodPolicy
	Range             RangePolicy
	Renderer          Renderer
	Profile           RendererProfile
	Classification    Classification
	Proof             *StepUpProof
	AbsoluteExpiresAt time.Time
}

func ValidateDeliveryResource(resource DeliveryResource) error {
	if resource.Kind == DeliveryResourceRecoveryResult && resource.Asset == nil && resource.RecoveryResult != nil {
		if backupasset.ValidateOpaqueID(resource.RecoveryResult.RecoveryJobID) != nil ||
			backupasset.ValidateOpaqueID(resource.RecoveryResult.ResultID) != nil {
			return ErrInvalidDeliveryResource
		}
		return nil
	}
	if resource.Kind != DeliveryResourceBackupAsset || resource.Asset == nil || resource.RecoveryResult != nil {
		return ErrInvalidDeliveryResource
	}
	if err := backupasset.ValidateAssetRef(*resource.Asset); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDeliveryResource, err)
	}
	return nil
}

func ValidateDeliveryProductForResource(
	kind DeliveryResourceKind,
	product DeliveryProduct,
	now time.Time,
) error {
	if kind == DeliveryResourceBackupAsset {
		return ValidateDeliveryProduct(product, now)
	}
	if kind != DeliveryResourceRecoveryResult || product.Method != MethodGetHead ||
		!validClassifications[product.Classification] || !product.AbsoluteExpiresAt.After(now.UTC()) ||
		!validRendererProduct(product) || product.Action != DeliveryDownload ||
		product.Renderer != RendererAttachment || product.Profile != ProfileOriginalV1 ||
		!validProof(product.Proof, auth.StepUpActionRecoveryResultDownload, product.AbsoluteExpiresAt, now) {
		return ErrInvalidDeliveryProduct
	}
	return nil
}

func ValidateDeliveryProduct(product DeliveryProduct, now time.Time) error {
	if product.Method != MethodGetHead || !validClassifications[product.Classification] ||
		!product.AbsoluteExpiresAt.After(now.UTC()) || !validRendererProduct(product) {
		return ErrInvalidDeliveryProduct
	}

	switch {
	case product.Action == DeliveryDownload:
		if product.Renderer != RendererAttachment || product.Profile != ProfileOriginalV1 ||
			!validProof(product.Proof, auth.StepUpActionAssetDownload, product.AbsoluteExpiresAt, now) {
			return ErrInvalidDeliveryProduct
		}
	case product.Action == DeliveryPreview && product.Renderer != RendererAttachment:
		if product.Classification == ClassificationNonSecret {
			if product.Proof != nil {
				return ErrInvalidDeliveryProduct
			}
		} else if !validProof(product.Proof, auth.StepUpActionAssetSecretReveal, product.AbsoluteExpiresAt, now) {
			return ErrInvalidDeliveryProduct
		}
	default:
		return ErrInvalidDeliveryProduct
	}
	return nil
}

func ValidateDeliveryTransition(from, to DeliveryState) error {
	if !validDeliveryStates[from] || !validDeliveryStates[to] {
		return ErrInvalidDeliveryState
	}
	if from == to || validDeliveryTransitions[[2]DeliveryState{from, to}] {
		return nil
	}
	return ErrInvalidDeliveryState
}

func validRendererProduct(product DeliveryProduct) bool {
	if product.Range != RangeNone && product.Range != RangeSingle {
		return false
	}
	switch product.Renderer {
	case RendererEscapedText:
		return product.Profile == ProfileTextV1 && product.Range == RangeNone
	case RendererSafeRaster:
		return product.Profile == ProfileRasterV1
	case RendererSameOriginPDF:
		return product.Profile == ProfilePDFV1
	case RendererNativeAudio:
		return product.Profile == ProfileAudioV1
	case RendererNativeVideo:
		return product.Profile == ProfileVideoV1
	case RendererMetadataHex:
		return product.Profile == ProfileHexV1 && product.Range == RangeNone
	case RendererAttachment:
		return product.Profile == ProfileOriginalV1
	default:
		return false
	}
}

func validProof(proof *StepUpProof, action auth.StepUpAction, absoluteExpiresAt, now time.Time) bool {
	return proof != nil && proof.Action == action && backupasset.ValidateOpaqueID(proof.ID) == nil &&
		proof.ExpiresAt.After(now.UTC()) && !proof.ExpiresAt.Before(absoluteExpiresAt)
}

var validClassifications = map[Classification]bool{
	ClassificationNonSecret: true,
	ClassificationSecret:    true,
	ClassificationUnknown:   true,
}

var validDeliveryStates = map[DeliveryState]bool{
	DeliveryIssued: true, DeliveryActive: true, DeliveryDraining: true,
	DeliveryRevoked: true, DeliveryExpired: true, DeliveryClosed: true,
}

var validDeliveryTransitions = map[[2]DeliveryState]bool{
	{DeliveryIssued, DeliveryActive}:    true,
	{DeliveryIssued, DeliveryRevoked}:   true,
	{DeliveryActive, DeliveryDraining}:  true,
	{DeliveryActive, DeliveryClosed}:    true,
	{DeliveryActive, DeliveryRevoked}:   true,
	{DeliveryActive, DeliveryExpired}:   true,
	{DeliveryDraining, DeliveryRevoked}: true,
	{DeliveryDraining, DeliveryExpired}: true,
}
