package ga

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type InventoryService struct {
	db        *gorm.DB
	source    InventorySource
	store     InventoryStore
	mutations ProviderMutationSurface
	now       func() time.Time
}

func NewInventoryService(deps InventoryDependencies) *InventoryService {
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	service := &InventoryService{
		db: deps.DB, source: deps.Source, store: deps.Store,
		mutations: deps.Mutations, now: now,
	}
	if deps.DB != nil {
		database := newDatabaseInventory(deps.DB, now)
		if service.source == nil {
			service.source = database
		}
		if service.store == nil {
			service.store = database
		}
	}
	return service
}

func (service *InventoryService) DryRun(ctx context.Context) (InventoryDocument, error) {
	if service == nil || service.source == nil {
		return InventoryDocument{}, fmt.Errorf("inventory source unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	facts, err := service.source.LoadFacts(ctx)
	if err != nil {
		return InventoryDocument{}, service.failClosed(ctx, InventoryErrorFailed, err)
	}
	persistedClass := InstallationClass("")
	if service.store != nil {
		persistedClass, err = service.store.CurrentClass(ctx)
		if err != nil {
			return InventoryDocument{}, service.failClosed(ctx, InventoryErrorFailed, err)
		}
	}
	document, err := classifyInventory(facts, persistedClass)
	if err != nil {
		return InventoryDocument{}, service.failClosed(ctx, InventoryErrorFailed, err)
	}
	if service.store != nil {
		if err := service.store.PersistRun(ctx, document); err != nil {
			return InventoryDocument{}, err
		}
	}
	_ = service.mutations
	return document, nil
}

func (service *InventoryService) Latest(ctx context.Context) (InventoryDocument, error) {
	if service == nil || service.db == nil {
		return InventoryDocument{}, fmt.Errorf("inventory source unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var installation model.BackupAssetInstallation
	installationResult := service.db.WithContext(ctx).Where("slot = ?", 1).Limit(1).Find(&installation)
	if installationResult.Error != nil {
		return InventoryDocument{}, installationResult.Error
	}
	var run model.BackupAssetInventoryRun
	runResult := service.db.WithContext(ctx).Where("status = ?", InventoryRunComplete).
		Order("created_at DESC, id DESC").Limit(1).Find(&run)
	if runResult.Error != nil {
		return InventoryDocument{}, runResult.Error
	}
	document := InventoryDocument{Class: InstallationClass(installation.Class)}
	if installationResult.RowsAffected > 0 {
		document.Digest = strings.TrimSpace(installation.InventoryDigest)
	}
	if runResult.RowsAffected == 0 {
		return document, nil
	}
	document.Digest = strings.TrimSpace(run.Digest)
	if err := json.Unmarshal([]byte(run.CountsJSON), &document.Counts); err != nil {
		return InventoryDocument{}, err
	}
	var rows []model.BackupAssetRepositoryConflict
	if err := service.db.WithContext(ctx).Where("run_id = ?", run.ID).Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return InventoryDocument{}, err
	}
	for _, row := range rows {
		var taskIDs []uint
		if strings.TrimSpace(row.TaskIDsJSON) != "" {
			if err := json.Unmarshal([]byte(row.TaskIDsJSON), &taskIDs); err != nil {
				return InventoryDocument{}, err
			}
		}
		document.Conflicts = append(document.Conflicts, InventoryConflict{
			Kind:             ConflictKind(row.Kind),
			TaskIDs:          taskIDs,
			RepositoryID:     row.RepositoryID,
			StableReasonCode: row.StableReasonCode,
		})
	}
	return document, nil
}

func (service *InventoryService) MaterializeReadiness(ctx context.Context, snapshot ReadinessSnapshot) error {
	if service == nil || service.db == nil {
		return nil
	}
	if snapshot.Status != ReadinessReady {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := service.now().UTC()
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		installation, ok, err := (&databaseInventory{db: service.db}).loadInstallation(ctx, tx)
		if err != nil {
			return err
		}
		if !ok || installation.Readiness == string(ReadinessReady) || installation.Readiness == string(ReadinessAcknowledged) {
			return nil
		}
		return tx.Model(&model.BackupAssetInstallation{}).Where("id = ?", installation.ID).Updates(map[string]any{
			"readiness":  string(ReadinessReady),
			"updated_at": now,
		}).Error
	})
}

func (service *InventoryService) RecordEnablementSucceeded(ctx context.Context) error {
	if service == nil || service.db == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := service.now().UTC()
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		installation, ok, err := (&databaseInventory{db: service.db}).loadInstallation(ctx, tx)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: installation missing", ErrEnablementBlocked)
		}
		if installation.EnablementSucceededAt != nil && !installation.EnablementSucceededAt.IsZero() {
			return nil
		}
		return tx.Model(&model.BackupAssetInstallation{}).Where("id = ?", installation.ID).Updates(map[string]any{
			"enablement_succeeded_at": now,
			"updated_at":              now,
		}).Error
	})
}

func (service *InventoryService) Acknowledge(ctx context.Context, actorID uint, digest string) error {
	if service == nil || service.db == nil {
		return fmt.Errorf("inventory source unavailable")
	}
	digest = strings.ToLower(strings.TrimSpace(digest))
	if actorID == 0 || !validInventoryDigest(digest) {
		return ErrInvalidInventoryDigest
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := service.now().UTC()
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		installation, ok, err := (&databaseInventory{db: service.db}).loadInstallation(ctx, tx)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: installation missing", ErrEnablementBlocked)
		}
		if installation.Class == string(InstallationFresh) {
			return ErrAcknowledgeNotRequired
		}
		if strings.TrimSpace(installation.InventoryDigest) != digest {
			return ErrInventoryDigestMismatch
		}
		var run model.BackupAssetInventoryRun
		runResult := tx.Where("status = ? AND digest = ?", InventoryRunComplete, digest).
			Order("created_at DESC, id DESC").Limit(1).Find(&run)
		if runResult.Error != nil {
			return runResult.Error
		}
		if runResult.RowsAffected == 0 {
			return fmt.Errorf("%w: inventory incomplete", ErrEnablementBlocked)
		}
		logger.Module("backup_asset_ga").Info().
			Uint("actor_id", actorID).
			Str("class", installation.Class).
			Int("conflicts", inventoryConflictCount(run.CountsJSON)).
			Msg("备份资产清单已确认")
		return tx.Model(&model.BackupAssetInstallation{}).Where("id = ?", installation.ID).Updates(map[string]any{
			"readiness":    string(ReadinessAcknowledged),
			"ack_actor_id": actorID,
			"ack_at":       now,
			"updated_at":   now,
		}).Error
	})
}

func inventoryConflictCount(countsJSON string) int {
	var counts InventoryCounts
	if json.Unmarshal([]byte(countsJSON), &counts) != nil || counts.Conflicts < 0 {
		return 0
	}
	return counts.Conflicts
}

func validInventoryDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func (service *InventoryService) failClosed(ctx context.Context, category string, cause error) error {
	if service == nil || service.store == nil {
		return cause
	}
	if persistErr := service.store.PersistFailedRun(ctx, category); persistErr != nil {
		return errors.Join(cause, persistErr)
	}
	return cause
}

func classifyInventory(facts InventoryFacts, persisted InstallationClass) (InventoryDocument, error) {
	reposByID := make(map[string]RepositoryFact, len(facts.Repositories))
	for _, repository := range facts.Repositories {
		if strings.TrimSpace(repository.ID) == "" {
			continue
		}
		reposByID[repository.ID] = repository
	}
	linksByTask := make(map[uint][]string, len(facts.Links))
	for _, link := range facts.Links {
		if link.TaskID == 0 || strings.TrimSpace(link.RepositoryID) == "" {
			continue
		}
		linksByTask[link.TaskID] = append(linksByTask[link.TaskID], link.RepositoryID)
	}

	type groupKey struct {
		provider backupasset.ProviderKind
		identity string
	}
	type group struct {
		provider backupasset.ProviderKind
		identity string
		mode     backupasset.VersionMode
		tasks    []uint
		repos    map[string]struct{}
	}
	groups := make(map[groupKey]*group)
	commandTasks := make([]uint, 0)
	gapTasks := make([]uint, 0)
	mismatchTasks := make([]uint, 0)
	mismatchRepos := make(map[string]struct{})
	for _, task := range facts.Tasks {
		kind := providerKindForExecutor(task.ExecutorType)
		switch kind {
		case backupasset.ProviderCommand:
			if task.TaskID != 0 {
				commandTasks = append(commandTasks, task.TaskID)
			}
		case backupasset.ProviderRestic, backupasset.ProviderRsync, backupasset.ProviderRclone:
			if task.TaskID == 0 {
				continue
			}
			linked := uniqueSortedString(linksByTask[task.TaskID])
			identity := strings.TrimSpace(task.IdentityKey)
			mode := task.VersionMode
			mismatched := false
			for _, repositoryID := range linked {
				repository, ok := reposByID[repositoryID]
				if !ok {
					continue
				}
				if identity == "" {
					identity = strings.TrimSpace(repository.IdentityKey)
				}
				if mode == "" {
					mode = repository.VersionMode
				}
				if repository.ProviderKind != "" && repository.ProviderKind != kind {
					mismatched = true
					mismatchRepos[repositoryID] = struct{}{}
				}
				if strings.TrimSpace(repository.IdentityKey) != "" && identity != strings.TrimSpace(repository.IdentityKey) {
					mismatched = true
					mismatchRepos[repositoryID] = struct{}{}
				}
			}
			if mismatched {
				mismatchTasks = append(mismatchTasks, task.TaskID)
				continue
			}
			if identity == "" {
				gapTasks = append(gapTasks, task.TaskID)
				continue
			}
			if mode == "" {
				mode = defaultVersionMode(kind)
			}
			key := groupKey{provider: kind, identity: identity}
			current := groups[key]
			if current == nil {
				current = &group{
					provider: kind,
					identity: identity,
					mode:     mode,
					repos:    map[string]struct{}{},
				}
				groups[key] = current
			}
			current.tasks = append(current.tasks, task.TaskID)
			for _, repositoryID := range linked {
				current.repos[repositoryID] = struct{}{}
			}
		}
	}

	candidates := make([]InventoryCandidate, 0, len(groups))
	conflicts := make([]InventoryConflict, 0)
	for _, current := range groups {
		tasks := uniqueSortedUint(current.tasks)
		repos := uniqueSortedString(mapKeys(current.repos))
		candidates = append(candidates, InventoryCandidate{
			Provider:         current.provider,
			VersionMode:      current.mode,
			IdentityKey:      current.identity,
			RepositoryIDs:    repos,
			ProducingTaskIDs: tasks,
		})
		if current.provider == backupasset.ProviderRestic && len(tasks) > 1 {
			conflicts = append(conflicts, InventoryConflict{
				Kind:             ConflictSharedResticIdentity,
				TaskIDs:          append([]uint(nil), tasks...),
				StableReasonCode: ReasonSharedResticIdentity,
			})
		}
	}
	if len(commandTasks) > 0 {
		conflicts = append(conflicts, InventoryConflict{
			Kind:             ConflictCommandUnsupported,
			TaskIDs:          uniqueSortedUint(commandTasks),
			StableReasonCode: ReasonCommandUnsupported,
		})
	}
	if len(gapTasks) > 0 {
		conflicts = append(conflicts, InventoryConflict{
			Kind:             ConflictCapabilityGap,
			TaskIDs:          uniqueSortedUint(gapTasks),
			StableReasonCode: ReasonCapabilityGap,
		})
	}
	if len(mismatchTasks) > 0 {
		repos := uniqueSortedString(mapKeys(mismatchRepos))
		repositoryID := ""
		if len(repos) == 1 {
			repositoryID = repos[0]
		}
		conflicts = append(conflicts, InventoryConflict{
			Kind:             ConflictTaskRepositoryMismatch,
			TaskIDs:          uniqueSortedUint(mismatchTasks),
			RepositoryID:     repositoryID,
			StableReasonCode: ReasonTaskRepositoryMismatch,
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Provider != candidates[j].Provider {
			return candidates[i].Provider < candidates[j].Provider
		}
		return candidates[i].IdentityKey < candidates[j].IdentityKey
	})
	sort.Slice(conflicts, func(i, j int) bool {
		if conflicts[i].Kind != conflicts[j].Kind {
			return conflicts[i].Kind < conflicts[j].Kind
		}
		return conflicts[i].StableReasonCode < conflicts[j].StableReasonCode
	})

	document := InventoryDocument{
		Class:      lockInstallationClass(computeInstallationClass(facts), persisted),
		Candidates: candidates,
		Conflicts:  conflicts,
		Counts: InventoryCounts{
			Candidates:     len(candidates),
			Conflicts:      len(conflicts),
			Unsupported:    len(uniqueSortedUint(commandTasks)),
			CapabilityGaps: len(uniqueSortedUint(gapTasks)),
		},
	}
	digest, err := inventoryDigest(document)
	if err != nil {
		return InventoryDocument{}, err
	}
	document.Digest = digest
	return document, nil
}

func computeInstallationClass(facts InventoryFacts) InstallationClass {
	if facts.HasManagedHistory {
		return InstallationExisting
	}
	if len(facts.Repositories) > 0 {
		return InstallationExisting
	}
	for _, task := range facts.Tasks {
		switch providerKindForExecutor(task.ExecutorType) {
		case backupasset.ProviderRestic, backupasset.ProviderRsync, backupasset.ProviderRclone:
			return InstallationExisting
		}
	}
	return InstallationFresh
}

func lockInstallationClass(computed, persisted InstallationClass) InstallationClass {
	if persisted == InstallationExisting {
		return InstallationExisting
	}
	return computed
}

func defaultVersionMode(kind backupasset.ProviderKind) backupasset.VersionMode {
	switch kind {
	case backupasset.ProviderRestic:
		return backupasset.VersionNativeSnapshot
	default:
		return backupasset.VersionMutableHead
	}
}

func providerKindForExecutor(executorType string) backupasset.ProviderKind {
	switch strings.ToLower(strings.TrimSpace(executorType)) {
	case string(backupasset.ProviderRestic):
		return backupasset.ProviderRestic
	case string(backupasset.ProviderRsync):
		return backupasset.ProviderRsync
	case string(backupasset.ProviderRclone):
		return backupasset.ProviderRclone
	case string(backupasset.ProviderCommand):
		return backupasset.ProviderCommand
	default:
		return ""
	}
}

func inventoryDigest(document InventoryDocument) (string, error) {
	payload, err := json.Marshal(struct {
		Candidates []InventoryCandidate `json:"candidates"`
		Conflicts  []InventoryConflict  `json:"conflicts"`
		Counts     InventoryCounts      `json:"counts"`
	}{Candidates: document.Candidates, Conflicts: document.Conflicts, Counts: document.Counts})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func uniqueSortedUint(values []uint) []uint {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[uint]struct{}, len(values))
	out := make([]uint, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func uniqueSortedString(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func mapKeys(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	return out
}

type databaseInventory struct {
	db  *gorm.DB
	now func() time.Time
}

func newDatabaseInventory(db *gorm.DB, now func() time.Time) *databaseInventory {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &databaseInventory{db: db, now: now}
}

func (inventory *databaseInventory) LoadFacts(ctx context.Context) (InventoryFacts, error) {
	if inventory == nil || inventory.db == nil {
		return InventoryFacts{}, fmt.Errorf("inventory database unavailable")
	}
	query := inventory.db.WithContext(ctx)
	var tasks []model.Task
	if err := query.Where("executor_type IN ?", []string{
		string(backupasset.ProviderRestic), string(backupasset.ProviderRsync),
		string(backupasset.ProviderRclone), string(backupasset.ProviderCommand),
	}).Find(&tasks).Error; err != nil {
		return InventoryFacts{}, err
	}
	var repositories []model.BackupRepository
	if err := query.Find(&repositories).Error; err != nil {
		return InventoryFacts{}, err
	}
	var links []model.TaskRepositoryLink
	if err := query.Where("unlinked_at IS NULL").Find(&links).Error; err != nil {
		return InventoryFacts{}, err
	}
	var latchCount int64
	if err := query.Model(&model.BackupAssetManagedHistoryLatch{}).Count(&latchCount).Error; err != nil {
		return InventoryFacts{}, err
	}
	var tombstoneCount int64
	if err := query.Model(&model.RecoveryPointLifecycleTombstone{}).Where("managed_history = ?", true).Count(&tombstoneCount).Error; err != nil {
		return InventoryFacts{}, err
	}

	reposByID := make(map[string]model.BackupRepository, len(repositories))
	repoFacts := make([]RepositoryFact, 0, len(repositories))
	for _, repository := range repositories {
		reposByID[repository.ID] = repository
		identity := ""
		if repository.RepositoryIdentity != nil {
			identity = strings.TrimSpace(*repository.RepositoryIdentity)
		}
		repoFacts = append(repoFacts, RepositoryFact{
			ID:           repository.ID,
			ProviderKind: backupasset.ProviderKind(repository.ProviderKind),
			IdentityKey:  identity,
			VersionMode:  backupasset.VersionMode(repository.VersionMode),
		})
	}
	linksByTask := make(map[uint][]string)
	linkFacts := make([]LinkFact, 0, len(links))
	for _, link := range links {
		if link.TaskID == nil || *link.TaskID == 0 {
			continue
		}
		linksByTask[*link.TaskID] = append(linksByTask[*link.TaskID], link.RepositoryID)
		linkFacts = append(linkFacts, LinkFact{TaskID: *link.TaskID, RepositoryID: link.RepositoryID})
	}
	taskFacts := make([]TaskFact, 0, len(tasks))
	for _, task := range tasks {
		kind := providerKindForExecutor(task.ExecutorType)
		identity := ""
		mode := backupasset.VersionMode("")
		for _, repositoryID := range linksByTask[task.ID] {
			repository, ok := reposByID[repositoryID]
			if !ok || repository.RepositoryIdentity == nil {
				continue
			}
			repoIdentity := strings.TrimSpace(*repository.RepositoryIdentity)
			if repoIdentity == "" {
				continue
			}
			if identity == "" {
				identity = repoIdentity
				mode = backupasset.VersionMode(repository.VersionMode)
			}
		}
		if mode == "" {
			mode = defaultVersionMode(kind)
		}
		if kind == backupasset.ProviderCommand {
			identity = ""
			mode = ""
		}
		taskFacts = append(taskFacts, TaskFact{
			TaskID:       task.ID,
			ExecutorType: task.ExecutorType,
			IdentityKey:  identity,
			VersionMode:  mode,
		})
	}
	return InventoryFacts{
		Tasks:             taskFacts,
		Repositories:      repoFacts,
		Links:             linkFacts,
		HasManagedHistory: latchCount > 0 || tombstoneCount > 0,
	}, nil
}

func (inventory *databaseInventory) CurrentClass(ctx context.Context) (InstallationClass, error) {
	installation, ok, err := inventory.loadInstallation(ctx, inventory.db)
	if err != nil || !ok {
		return "", err
	}
	return InstallationClass(installation.Class), nil
}

func (inventory *databaseInventory) PersistRun(ctx context.Context, document InventoryDocument) error {
	if inventory == nil || inventory.db == nil {
		return fmt.Errorf("inventory database unavailable")
	}
	now := inventory.now().UTC()
	return inventory.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		installation, ok, err := inventory.loadInstallation(ctx, tx)
		if err != nil {
			return err
		}
		class := lockInstallationClass(document.Class, InstallationClass(installation.Class))
		if class == "" {
			class = document.Class
		}
		if !ok {
			id, err := backupasset.NewOpaqueID()
			if err != nil {
				return err
			}
			installation = model.BackupAssetInstallation{
				ID: id, Slot: 1, Class: string(class), Readiness: string(ReadinessUnknown),
				InventoryDigest: document.Digest, CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Create(&installation).Error; err != nil {
				return err
			}
		} else {
			updates := map[string]any{
				"class":            string(class),
				"inventory_digest": document.Digest,
				"updated_at":       now,
			}
			if installation.Readiness == string(ReadinessAcknowledged) && installation.InventoryDigest != document.Digest {
				updates["readiness"] = string(ReadinessBlocked)
				updates["ack_actor_id"] = nil
				updates["ack_at"] = nil
			}
			if err := tx.Model(&model.BackupAssetInstallation{}).Where("id = ?", installation.ID).Updates(updates).Error; err != nil {
				return err
			}
		}
		runID, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		counts, err := json.Marshal(document.Counts)
		if err != nil {
			return err
		}
		run := model.BackupAssetInventoryRun{
			ID: runID, Digest: document.Digest, Status: InventoryRunComplete,
			CountsJSON: string(counts), CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		for _, conflict := range document.Conflicts {
			conflictID, err := backupasset.NewOpaqueID()
			if err != nil {
				return err
			}
			taskIDs, err := json.Marshal(conflict.TaskIDs)
			if err != nil {
				return err
			}
			row := model.BackupAssetRepositoryConflict{
				ID: conflictID, RunID: runID, Kind: string(conflict.Kind),
				TaskIDsJSON: string(taskIDs), RepositoryID: conflict.RepositoryID,
				StableReasonCode: conflict.StableReasonCode, CreatedAt: now,
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		logger.Module("backup_asset_ga").Info().
			Str("status", InventoryRunComplete).
			Str("class", string(class)).
			Int("candidates", document.Counts.Candidates).
			Int("conflicts", document.Counts.Conflicts).
			Msg("备份资产清单试运行完成")
		return nil
	})
}

func (inventory *databaseInventory) PersistFailedRun(ctx context.Context, category string) error {
	if inventory == nil || inventory.db == nil {
		return fmt.Errorf("inventory database unavailable")
	}
	category = strings.TrimSpace(category)
	if category == "" {
		category = InventoryErrorFailed
	}
	now := inventory.now().UTC()
	digest := failedInventoryDigest(category)
	counts, err := json.Marshal(InventoryCounts{})
	if err != nil {
		return err
	}
	runID, err := backupasset.NewOpaqueID()
	if err != nil {
		return err
	}
	return inventory.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		installation, ok, err := inventory.loadInstallation(ctx, tx)
		if err != nil {
			return err
		}
		if ok && (installation.Readiness == string(ReadinessReady) || installation.Readiness == string(ReadinessAcknowledged)) {
			if err := tx.Model(&model.BackupAssetInstallation{}).Where("id = ?", installation.ID).Updates(map[string]any{
				"readiness":  string(ReadinessBlocked),
				"updated_at": now,
			}).Error; err != nil {
				return err
			}
		}
		return tx.Create(&model.BackupAssetInventoryRun{
			ID: runID, Digest: digest, Status: InventoryRunFailed,
			CountsJSON: string(counts), ErrorCategory: category, CreatedAt: now, UpdatedAt: now,
		}).Error
	})
}

func (inventory *databaseInventory) loadInstallation(ctx context.Context, tx *gorm.DB) (model.BackupAssetInstallation, bool, error) {
	if tx == nil {
		return model.BackupAssetInstallation{}, false, fmt.Errorf("inventory database unavailable")
	}
	var installation model.BackupAssetInstallation
	result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("slot = ?", 1).Limit(1).Find(&installation)
	if result.Error != nil {
		return model.BackupAssetInstallation{}, false, result.Error
	}
	return installation, result.RowsAffected > 0, nil
}

func failedInventoryDigest(category string) string {
	sum := sha256.Sum256([]byte("inventory:" + category))
	return hex.EncodeToString(sum[:])
}
