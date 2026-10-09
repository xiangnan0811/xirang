package model

// No imports needed — this file only holds the package declaration and
// indexing comments. All types and their dependencies live in the
// domain-grouped files listed below.

// This file exists only to declare the package and its imports.
// All type definitions have been moved to domain-grouped files:
//
//	user.go        — User, SSHKey, LoginFailure
//	node.go        — Node, NodeOwner
//	task.go        — Task, TaskRun, TaskLog, TaskTrafficSample
//	task_occurrence.go — TaskCronOccurrence
//	task_resource.go — TaskRunResourceIdentity and resource identity helpers
//	policy.go      — Policy, PolicyNode
//	integration.go — Integration, AppCredential
//	report.go      — ReportConfig, Report
//	audit.go       — AuditLog, CredentialAuditEvent, CredentialAccessGrant
//	monitor.go     — AnomalyEvent, SLODefinition
//	backup.go      — RestoreDrillEvidence, SnapshotDiffHistory, SnapshotFileIndex, AutomationRule, AutomationRuleLog
//	system.go      — SystemSetting
//	cron_backup_health.go — CronBackupHealth and CronBackupHealthUsage
