package backupasset

import "errors"

var (
	ErrNotFound                    = errors.New("backup asset not found")
	ErrForbidden                   = errors.New("backup asset operation forbidden")
	ErrConflict                    = errors.New("backup asset conflict")
	ErrInvalidState                = errors.New("invalid backup asset state")
	ErrInvalidAssetRef             = errors.New("invalid backup asset reference")
	ErrProviderUnavailable         = errors.New("backup asset provider unavailable")
	ErrCapabilityUnavailable       = errors.New("backup asset capability unavailable")
	ErrKeyUnavailable              = errors.New("backup asset key unavailable")
	ErrKeyLost                     = errors.New("backup asset key lost")
	ErrKeyRotationProhibited       = errors.New("backup asset key rotation prohibited")
	ErrLeaseHeld                   = errors.New("backup asset lease held")
	ErrLeaseFenceLost              = errors.New("backup asset lease fence lost")
	ErrLeaseDeadlineExceeded       = errors.New("backup asset lease deadline exceeded")
	ErrPublicationInProgress       = errors.New("backup asset publication in progress")
	ErrPublicationUnconfirmed      = errors.New("backup asset publication unconfirmed")
	ErrPublicationSessionAbandoned = errors.New("backup asset publication session abandoned")
)
