package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"xirang/backend/internal/backupasset"
)

const (
	recoveryPointIDDomain = "xirang.recovery-point.task-run.v1"
	resticLinkTagPrefix   = "xirang.link.v1."
	resticPointTagPrefix  = "xirang.point.v1."
)

func deriveRecoveryPointID(linkID string, taskRunID uint) (string, error) {
	if backupasset.ValidateOpaqueID(linkID) != nil || taskRunID == 0 {
		return "", fmt.Errorf("%w: invalid deterministic recovery point identity", backupasset.ErrInvalidState)
	}
	sum := sha256.Sum256([]byte(recoveryPointIDDomain + "\x00" + linkID + "\x00" + strconv.FormatUint(uint64(taskRunID), 10)))
	return hex.EncodeToString(sum[:16]), nil
}

func deriveResticPublicationTags(linkID, pointID string) ([2]string, error) {
	if backupasset.ValidateOpaqueID(linkID) != nil || backupasset.ValidateOpaqueID(pointID) != nil {
		return [2]string{}, fmt.Errorf("%w: invalid Restic publication tag identity", backupasset.ErrInvalidState)
	}
	return [2]string{resticLinkTagPrefix + linkID, resticPointTagPrefix + pointID}, nil
}
