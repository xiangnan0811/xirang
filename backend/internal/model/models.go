package model

// No imports needed — this file only holds the package declaration and
// indexing comments. All types and their dependencies live in the
// domain-grouped files listed below.

// This file exists only to declare the package and its imports.
// All type definitions have been moved to domain-grouped files:
//
//	user.go        — User, SSHKey, LoginFailure
//	node.go        — Node, NodeOwner, NodeMetricSample, NodeMetricSampleHourly, NodeMetricSampleDaily, NodeLog, NodeLogCursor
//	task.go        — Task, TaskRun, TaskLog, TaskTrafficSample
//	policy.go      — Policy, PolicyNode
//	alert.go       — Alert, AlertDelivery, Silence, EscalationPolicy, EscalationLevel, AlertEscalationEvent
//	integration.go — Integration, AppCredential
//	report.go      — ReportConfig, Report
//	audit.go       — AuditLog, CredentialAuditEvent, CredentialAccessGrant
//	monitor.go     — Dashboard, DashboardPanel, PanelFilters, ServiceMonitor, ServiceUptimeSample, AnomalyEvent, SLODefinition
//	backup.go      — RestoreDrillEvidence, SnapshotDiffHistory, SnapshotFileIndex, AutomationRule, AutomationRuleLog
//	system.go      — SystemSetting
