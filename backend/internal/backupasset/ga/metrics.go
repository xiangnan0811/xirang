package ga

import (
	"errors"
	"fmt"

	"xirang/backend/internal/backupasset"

	"github.com/prometheus/client_golang/prometheus"
)

type InventoryResult string

const (
	InventoryResultComplete InventoryResult = InventoryRunComplete
	InventoryResultFailed   InventoryResult = InventoryRunFailed
)

type EnablementRejectReason string

const (
	RejectInventoryIncomplete  EnablementRejectReason = "inventory_incomplete"
	RejectExportRootInvalid    EnablementRejectReason = "export_root_invalid"
	RejectKeysUnready          EnablementRejectReason = "keys_unready"
	RejectAckRequired          EnablementRejectReason = "ack_required"
	RejectClassUnknown         EnablementRejectReason = "class_unknown"
	RejectReadinessUnavailable EnablementRejectReason = "readiness_unavailable"
)

type Metrics interface {
	SetInstallationClass(InstallationClass)
	SetReadinessState(ReadinessStatus)
	SetLastInventoryResult(InventoryResult)
	SetConflictCount(ConflictKind, int)
	ObserveEnablementReject(EnablementRejectReason)
	SetExportRootProbe(ok bool)
	SetFeatureGates(requested, live bool)
}

type NoopMetrics struct{}

func (NoopMetrics) SetInstallationClass(InstallationClass)         {}
func (NoopMetrics) SetReadinessState(ReadinessStatus)              {}
func (NoopMetrics) SetLastInventoryResult(InventoryResult)         {}
func (NoopMetrics) SetConflictCount(ConflictKind, int)             {}
func (NoopMetrics) ObserveEnablementReject(EnablementRejectReason) {}
func (NoopMetrics) SetExportRootProbe(bool)                        {}
func (NoopMetrics) SetFeatureGates(bool, bool)                     {}

type PrometheusMetrics struct {
	installationClass *prometheus.GaugeVec
	readinessState    *prometheus.GaugeVec
	inventoryResult   *prometheus.GaugeVec
	conflicts         *prometheus.GaugeVec
	enablementRejects *prometheus.CounterVec
	exportRootProbe   *prometheus.GaugeVec
	featureRequested  prometheus.Gauge
	featureLive       prometheus.Gauge
}

func NewPrometheusMetrics(registerer prometheus.Registerer) (*PrometheusMetrics, error) {
	if registerer == nil {
		return nil, fmt.Errorf("%w: backup asset GA Prometheus registerer unavailable", backupasset.ErrInvalidState)
	}
	metrics := &PrometheusMetrics{
		installationClass: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_ga_installation_class",
			Help: "Current backup asset GA installation class.",
		}, []string{"class"}),
		readinessState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_ga_readiness_state",
			Help: "Current backup asset GA readiness state.",
		}, []string{"state"}),
		inventoryResult: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_ga_inventory_result",
			Help: "Last backup asset GA inventory result.",
		}, []string{"result"}),
		conflicts: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_ga_conflicts",
			Help: "Current backup asset GA inventory conflict counts by closed kind.",
		}, []string{"kind"}),
		enablementRejects: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_ga_enablement_rejects_total",
			Help: "Total backup asset enablement rejects by closed reason category.",
		}, []string{"reason"}),
		exportRootProbe: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_ga_export_root_probe",
			Help: "Last backup asset export-root probe result.",
		}, []string{"result"}),
		featureRequested: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: FeatureRequestedMetric,
			Help: "Whether backup_assets.enabled is requested.",
		}),
		featureLive: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: FeatureLiveMetric,
			Help: "Whether backup asset FeatureLive is currently true.",
		}),
	}
	for _, collector := range []prometheus.Collector{
		metrics.installationClass, metrics.readinessState, metrics.inventoryResult,
		metrics.conflicts, metrics.enablementRejects, metrics.exportRootProbe,
		metrics.featureRequested, metrics.featureLive,
	} {
		if err := registerer.Register(collector); err != nil {
			return nil, fmt.Errorf("register backup asset GA metric: %w", err)
		}
	}
	return metrics, nil
}

func (metrics *PrometheusMetrics) SetInstallationClass(class InstallationClass) {
	if metrics != nil {
		setExclusiveGauge(metrics.installationClass, metricInstallationClass(class), installationClassLabels)
	}
}

func (metrics *PrometheusMetrics) SetReadinessState(state ReadinessStatus) {
	if metrics != nil {
		setExclusiveGauge(metrics.readinessState, metricReadinessState(state), readinessStateLabels)
	}
}

func (metrics *PrometheusMetrics) SetLastInventoryResult(result InventoryResult) {
	if metrics != nil {
		setExclusiveGauge(metrics.inventoryResult, metricInventoryResult(result), inventoryResultLabels)
	}
}

func (metrics *PrometheusMetrics) SetConflictCount(kind ConflictKind, count int) {
	if metrics == nil {
		return
	}
	if count < 0 {
		count = 0
	}
	metrics.conflicts.WithLabelValues(metricConflictKind(kind)).Set(float64(count))
}

func (metrics *PrometheusMetrics) ObserveEnablementReject(reason EnablementRejectReason) {
	if metrics != nil {
		metrics.enablementRejects.WithLabelValues(metricEnablementReject(reason)).Inc()
	}
}

func (metrics *PrometheusMetrics) SetExportRootProbe(ok bool) {
	if metrics == nil {
		return
	}
	result := "fail"
	if ok {
		result = "ok"
	}
	setExclusiveGauge(metrics.exportRootProbe, result, exportRootProbeLabels)
}

func (metrics *PrometheusMetrics) SetFeatureGates(requested, live bool) {
	if metrics == nil {
		return
	}
	metrics.featureRequested.Set(boolGauge(requested))
	metrics.featureLive.Set(boolGauge(live))
}

func boolGauge(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func ObserveReadiness(metrics Metrics, snapshot ReadinessSnapshot) {
	if metrics == nil {
		return
	}
	metrics.SetInstallationClass(snapshot.Class)
	metrics.SetReadinessState(snapshot.Status)
	metrics.SetExportRootProbe(snapshot.ExportRootValid)
}

func ObserveInventory(metrics Metrics, document InventoryDocument, result InventoryResult) {
	if metrics == nil {
		return
	}
	metrics.SetLastInventoryResult(result)
	counts := map[ConflictKind]int{}
	for _, conflict := range document.Conflicts {
		counts[conflict.Kind]++
	}
	for _, kind := range []ConflictKind{
		ConflictSharedResticIdentity, ConflictTaskRepositoryMismatch, ConflictCapabilityGap, ConflictCommandUnsupported,
	} {
		metrics.SetConflictCount(kind, counts[kind])
	}
}

func ClassifyEnablementReject(snapshot ReadinessSnapshot, err error) EnablementRejectReason {
	if errors.Is(err, ErrEnablementAckRequired) {
		return RejectAckRequired
	}
	if snapshot.Class != "" && snapshot.Class != InstallationFresh && snapshot.Class != InstallationExisting {
		return RejectClassUnknown
	}
	if !snapshot.InventoryComplete {
		return RejectInventoryIncomplete
	}
	if !snapshot.ExportRootValid {
		return RejectExportRootInvalid
	}
	if !snapshot.KeyDomainsReady {
		return RejectKeysUnready
	}
	return RejectReadinessUnavailable
}

func setExclusiveGauge(vec *prometheus.GaugeVec, selected string, known []string) {
	if vec == nil {
		return
	}
	for _, label := range known {
		value := 0.0
		if label == selected {
			value = 1
		}
		vec.WithLabelValues(label).Set(value)
	}
}

func metricInstallationClass(class InstallationClass) string {
	switch class {
	case InstallationFresh, InstallationExisting:
		return string(class)
	default:
		return "unknown"
	}
}

func metricReadinessState(state ReadinessStatus) string {
	switch state {
	case ReadinessUnknown, ReadinessBlocked, ReadinessReady, ReadinessAcknowledged:
		return string(state)
	default:
		return "unknown"
	}
}

func metricInventoryResult(result InventoryResult) string {
	switch result {
	case InventoryResultComplete, InventoryResultFailed:
		return string(result)
	default:
		return "unknown"
	}
}

func metricConflictKind(kind ConflictKind) string {
	switch kind {
	case ConflictSharedResticIdentity, ConflictTaskRepositoryMismatch, ConflictCapabilityGap, ConflictCommandUnsupported:
		return string(kind)
	default:
		return "unknown"
	}
}

func metricEnablementReject(reason EnablementRejectReason) string {
	switch reason {
	case RejectInventoryIncomplete, RejectExportRootInvalid, RejectKeysUnready,
		RejectAckRequired, RejectClassUnknown, RejectReadinessUnavailable:
		return string(reason)
	default:
		return "unknown"
	}
}

var (
	installationClassLabels = []string{string(InstallationFresh), string(InstallationExisting), "unknown"}
	readinessStateLabels    = []string{string(ReadinessUnknown), string(ReadinessBlocked), string(ReadinessReady), string(ReadinessAcknowledged), "unknown"}
	inventoryResultLabels   = []string{string(InventoryResultComplete), string(InventoryResultFailed), "unknown"}
	exportRootProbeLabels   = []string{"ok", "fail"}
)

const (
	FeatureRequestedMetric = "xirang_backup_asset_feature_requested"
	FeatureLiveMetric      = "xirang_backup_asset_feature_live"
)

var _ Metrics = (*PrometheusMetrics)(nil)
var _ Metrics = NoopMetrics{}
