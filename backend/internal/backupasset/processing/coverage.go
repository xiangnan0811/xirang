package processing

import (
	"context"
	"fmt"
	"sort"
	"time"

	"xirang/backend/internal/backupasset/processing/capabilityspec"

	"gorm.io/gorm"
)

type CoverageBucket struct {
	Capability  string `json:"capability"`
	Profile     string `json:"profile"`
	Eligible    int64  `json:"eligible"`
	Completed   int64  `json:"completed"`
	Partial     int64  `json:"partial"`
	Queued      int64  `json:"queued"`
	Failed      int64  `json:"failed"`
	Unsupported int64  `json:"unsupported"`
	NotDeployed int64  `json:"not_deployed"`
	Stale       int64  `json:"stale"`
}

type CoverageSummary struct {
	SchemaVersion    int              `json:"schema_version"`
	GeneratedAt      time.Time        `json:"generated_at"`
	Eligible         int64            `json:"eligible"`
	Completed        int64            `json:"completed"`
	Partial          int64            `json:"partial"`
	Queued           int64            `json:"queued"`
	Failed           int64            `json:"failed"`
	Unsupported      int64            `json:"unsupported"`
	NotDeployed      int64            `json:"not_deployed"`
	Stale            int64            `json:"stale"`
	BacklogAgeBucket string           `json:"backlog_age_bucket"`
	EstimatedSeconds *int64           `json:"estimated_seconds"`
	ByCapability     []CoverageBucket `json:"by_capability"`
}

type CapabilityInventoryItem struct {
	Capability        string                `json:"capability"`
	Schema            string                `json:"schema"`
	Profile           string                `json:"profile"`
	InputMIMEs        []string              `json:"input_mimes"`
	Limits            capabilityspec.Limits `json:"limits"`
	RequiresWorkspace bool                  `json:"requires_secure_workspace"`
	EnabledByDefault  bool                  `json:"enabled_by_default"`
	Deployed          bool                  `json:"deployed"`
	ReadyWorkers      int64                 `json:"ready_workers"`
}

type CoverageService struct {
	db  *gorm.DB
	now func() time.Time
}

func NewCoverageService(db *gorm.DB, now func() time.Time) (*CoverageService, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: coverage database unavailable", ErrInvalidContract)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &CoverageService{db: db, now: now}, nil
}

func (service *CoverageService) Summary(ctx context.Context) (CoverageSummary, error) {
	if service == nil || service.db == nil {
		return CoverageSummary{}, fmt.Errorf("%w: coverage service unavailable", ErrInvalidContract)
	}
	ctx = nonNilProcessingContext(ctx)
	type aggregateRow struct {
		Capability string
		Profile    string
		State      string
		Count      int64
	}
	var rows []aggregateRow
	if err := service.db.WithContext(ctx).Table("backup_asset_processing_jobs").
		Select("capability, output_profile AS profile, state, count(*) AS count").
		Where("is_current = ?", true).
		Group("capability, output_profile, state").Scan(&rows).Error; err != nil {
		return CoverageSummary{}, fmt.Errorf("aggregate processing coverage: %w", err)
	}
	now := service.now().UTC()
	result := CoverageSummary{SchemaVersion: 1, GeneratedAt: now, ByCapability: []CoverageBucket{}}
	buckets := make(map[string]*CoverageBucket)
	for _, row := range rows {
		key := row.Capability + "\x00" + row.Profile
		bucket := buckets[key]
		if bucket == nil {
			bucket = &CoverageBucket{Capability: row.Capability, Profile: row.Profile}
			buckets[key] = bucket
		}
		bucket.Eligible += row.Count
		result.Eligible += row.Count
		switch ProcessingState(row.State) {
		case ProcessingSucceeded:
			bucket.Completed += row.Count
			result.Completed += row.Count
		case ProcessingFailed, ProcessingCanceled, ProcessingExpired, ProcessingSuperseded:
			bucket.Failed += row.Count
			result.Failed += row.Count
		default:
			bucket.Queued += row.Count
			result.Queued += row.Count
		}
	}
	type setRow struct {
		Capability   string
		Profile      string
		State        string
		Completeness string
		Count        int64
	}
	var setRows []setRow
	if err := service.db.WithContext(ctx).Table("backup_asset_derived_artifact_sets AS sets").
		Select("jobs.capability, jobs.output_profile AS profile, sets.state, sets.completeness, count(*) AS count").
		Joins("JOIN backup_asset_processing_jobs AS jobs ON jobs.id = sets.job_id").
		Group("jobs.capability, jobs.output_profile, sets.state, sets.completeness").Scan(&setRows).Error; err != nil {
		return CoverageSummary{}, fmt.Errorf("aggregate Derived coverage: %w", err)
	}
	for _, row := range setRows {
		bucket := buckets[row.Capability+"\x00"+row.Profile]
		if bucket == nil {
			bucket = &CoverageBucket{Capability: row.Capability, Profile: row.Profile}
			buckets[row.Capability+"\x00"+row.Profile] = bucket
		}
		if row.State == "stale" {
			bucket.Stale += row.Count
			result.Stale += row.Count
		}
		if row.State == "active" && row.Completeness == string(ArtifactPartial) {
			bucket.Partial += row.Count
			result.Partial += row.Count
		}
	}
	for _, bucket := range buckets {
		result.ByCapability = append(result.ByCapability, *bucket)
	}
	sort.Slice(result.ByCapability, func(left, right int) bool {
		if result.ByCapability[left].Capability == result.ByCapability[right].Capability {
			return result.ByCapability[left].Profile < result.ByCapability[right].Profile
		}
		return result.ByCapability[left].Capability < result.ByCapability[right].Capability
	})
	var oldestQueued struct {
		QueuedAt time.Time
	}
	oldestResult := service.db.WithContext(ctx).Table("backup_asset_processing_jobs").Select("queued_at").
		Where("is_current = ? AND state NOT IN ?", true, []string{
			string(ProcessingSucceeded), string(ProcessingFailed), string(ProcessingCanceled),
			string(ProcessingExpired), string(ProcessingSuperseded),
		}).Order("queued_at ASC").Limit(1).Scan(&oldestQueued)
	if oldestResult.Error != nil {
		return CoverageSummary{}, fmt.Errorf("load oldest processing backlog: %w", oldestResult.Error)
	}
	var oldest *time.Time
	if oldestResult.RowsAffected == 1 {
		value := oldestQueued.QueuedAt.UTC()
		oldest = &value
	}
	result.BacklogAgeBucket = coverageAgeBucket(now, oldest)
	if result.Queued > 0 {
		estimate := min(result.Queued*30, int64((24*time.Hour)/time.Second))
		result.EstimatedSeconds = &estimate
	}
	return result, nil
}

func (service *CoverageService) Capabilities(ctx context.Context, secretEnabled bool) ([]CapabilityInventoryItem, error) {
	if service == nil || service.db == nil {
		return nil, fmt.Errorf("%w: coverage service unavailable", ErrInvalidContract)
	}
	type readyRow struct {
		Capability string
		Profile    string
		Count      int64
	}
	var ready []readyRow
	if err := service.db.WithContext(nonNilProcessingContext(ctx)).Table("backup_asset_worker_capabilities AS capabilities").
		Select("capabilities.capability, capabilities.output_profile AS profile, count(distinct capabilities.worker_id) AS count").
		Joins("JOIN backup_asset_worker_identities AS workers ON workers.id = capabilities.worker_id").
		Where("workers.trust_state = ? AND workers.health_state = ? AND capabilities.health_state = ?", "active", "ready", "ready").
		Group("capabilities.capability, capabilities.output_profile").Scan(&ready).Error; err != nil {
		return nil, fmt.Errorf("aggregate ready processing capabilities: %w", err)
	}
	counts := make(map[string]int64, len(ready))
	for _, row := range ready {
		counts[row.Capability+"\x00"+row.Profile] = row.Count
	}
	profiles := capabilityspec.AllProfiles(secretEnabled)
	result := make([]CapabilityInventoryItem, 0, len(profiles))
	for _, profile := range profiles {
		count := counts[profile.Capability+"\x00"+profile.OutputProfile]
		result = append(result, CapabilityInventoryItem{
			Capability: profile.Capability, Schema: profile.CapabilitySchema, Profile: profile.OutputProfile,
			InputMIMEs: append([]string(nil), profile.InputMIMEs...), Limits: profile.Limits,
			RequiresWorkspace: profile.RequiresMaterialization, EnabledByDefault: profile.EnabledByDefault,
			Deployed: count > 0, ReadyWorkers: count,
		})
	}
	return result, nil
}

func coverageAgeBucket(now time.Time, oldest *time.Time) string {
	if oldest == nil {
		return "none"
	}
	age := now.Sub(oldest.UTC())
	switch {
	case age < time.Hour:
		return "under_1h"
	case age < 24*time.Hour:
		return "1h_24h"
	case age < 7*24*time.Hour:
		return "1d_7d"
	default:
		return "over_7d"
	}
}
