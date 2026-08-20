package overlay

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	assetsearch "xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestLifecycleLateOutputRejectsOverlayResurrection(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	if err := harness.db.AutoMigrate(&model.RecoveryPointLifecycleAttempt{}); err != nil {
		t.Fatalf("migrate lifecycle attempt: %v", err)
	}
	actor := Actor{UserID: 700, Role: "operator"}
	pointID := strings.Repeat("b", 32)
	ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat("b", 64)}
	harness.points[pointID] = true
	harness.assets[ref] = true
	now := harness.clock.Now()
	if err := harness.db.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).Updates(map[string]any{
		"semantics": backupasset.PointMutableHead, "state": backupasset.RecoveryPointObserved,
		"observed_at": now, "immutability_level": backupasset.ImmutabilityMutable,
	}).Error; err != nil {
		t.Fatal(err)
	}

	saved, err := service.CreateSavedSearch(context.Background(), actor, CreateSavedSearchRequest{
		Query: savedQuery(pointID, "before retirement"), IdempotencyKey: "lifecycle-late-saved-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	favorite, err := service.AddFavorite(context.Background(), actor, AddFavoriteRequest{
		Ref: ref, Label: "before retirement", IdempotencyKey: "lifecycle-late-fav-0001",
	})
	if err != nil {
		t.Fatal(err)
	}
	tag, err := service.CreateTag(context.Background(), actor.UserID, "before-retirement", "lifecycle-late-tag-0001")
	if err != nil {
		t.Fatal(err)
	}
	assignment, err := service.AssignTag(context.Background(), actor, tag.ID, ref, "lifecycle-late-assign1")
	if err != nil {
		t.Fatal(err)
	}
	recent, err := service.RecordRecent(context.Background(), actor, ref)
	if err != nil {
		t.Fatal(err)
	}

	attemptID := strings.Repeat("d", 32)
	if err := harness.db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: attemptID, RecoveryPointID: pointID, Operation: string(backupasset.LifecycleMutableRetire),
		Phase: string(backupasset.LifecyclePhaseCleaning), TransitionRevision: 1,
		ClaimedAt: &now, HeartbeatAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	result, err := service.ReconcileSource(context.Background(), SourceLifecycle{
		RecoveryPointID: pointID, Reason: SourceRetired,
	}, 100)
	if err != nil {
		t.Fatalf("ReconcileSource: %v", err)
	}
	if result.SavedSearches != 1 || result.Favorites != 1 || result.TagAssignments != 1 || result.RecentsDeleted != 1 {
		t.Fatalf("lifecycle result=%+v", result)
	}

	lateWrites := []struct {
		name string
		run  func() error
	}{
		{"saved_search", func() error {
			_, err := service.CreateSavedSearch(context.Background(), actor, CreateSavedSearchRequest{
				Query: savedQuery(pointID, "late retirement"), IdempotencyKey: "lifecycle-late-saved-02",
			})
			return err
		}},
		{"favorite", func() error {
			_, err := service.AddFavorite(context.Background(), actor, AddFavoriteRequest{
				Ref: ref, Label: "late retirement", IdempotencyKey: "lifecycle-late-fav-0002",
			})
			return err
		}},
		{"tag_assignment", func() error {
			_, err := service.AssignTag(context.Background(), actor, tag.ID, ref, "lifecycle-late-assign2")
			return err
		}},
		{"recent", func() error {
			_, err := service.RecordRecent(context.Background(), actor, ref)
			return err
		}},
	}
	for _, write := range lateWrites {
		t.Run(write.name, func(t *testing.T) {
			if err := write.run(); !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("late write error=%v, want ErrConflict", err)
			}
		})
	}

	var savedRow model.BackupAssetSavedSearch
	if err := harness.db.Where("id = ?", saved.ID).Take(&savedRow).Error; err != nil {
		t.Fatal(err)
	}
	var favoriteRow model.BackupAssetFavorite
	if err := harness.db.Where("id = ?", favorite.ID).Take(&favoriteRow).Error; err != nil {
		t.Fatal(err)
	}
	var assignmentRow model.BackupAssetTagAssignment
	if err := harness.db.Where("id = ?", assignment.ID).Take(&assignmentRow).Error; err != nil {
		t.Fatal(err)
	}
	var recentCount int64
	if err := harness.db.Model(&model.BackupAssetRecentAccess{}).Where("id = ?", recent.ID).Count(&recentCount).Error; err != nil {
		t.Fatal(err)
	}
	if savedRow.State != string(SavedSearchBroken) || favoriteRow.State != string(OverlayTombstone) ||
		assignmentRow.State != string(OverlayTombstone) || recentCount != 0 {
		t.Fatalf("late output resurrected overlays: saved=%s favorite=%s assignment=%s recent=%d",
			savedRow.State, favoriteRow.State, assignmentRow.State, recentCount)
	}
}

func TestLifecycleDependentCleanupOverlayMaintenanceUsesExactAttemptWhileDisabled(t *testing.T) {
	enabled, harness := newOverlayTestHarness(t)
	actor := Actor{UserID: 799, Role: "operator"}
	pointID := strings.Repeat("a", 32)
	ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat("a", 64)}
	harness.assets[ref] = true
	favorite, err := enabled.AddFavorite(context.Background(), actor, AddFavoriteRequest{
		Ref: ref, Label: "lifecycle maintenance", IdempotencyKey: "lifecycle-maintenance-fav",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := harness.clock.Now()
	attemptID := strings.Repeat("c", 32)
	if err := harness.db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: attemptID, RecoveryPointID: pointID, Operation: string(backupasset.LifecycleRetentionExpire),
		Phase: string(backupasset.LifecyclePhaseCleaning), TransitionRevision: 1,
		ClaimedAt: &now, HeartbeatAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	disabled, err := NewService(ServiceDependencies{
		DB: harness.db, Keys: harness.ring, Assets: enabled.assets, Points: enabled.points,
		Now: harness.clock.Now, Config: DefaultConfig(), FeatureEnabled: func() (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := backupasset.SourceLifecycleRequest{
		RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
		Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecycleCleanup,
	}
	result, err := disabled.ReconcileSourceLifecycle(context.Background(), request, SourceLifecycle{
		RecoveryPointID: pointID, Reason: SourceExpiring,
	}, 1)
	if err != nil || result.Favorites != 1 {
		t.Fatalf("disabled lifecycle maintenance result=%+v err=%v", result, err)
	}
	var row model.BackupAssetFavorite
	if err := harness.db.Where("id = ?", favorite.ID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != string(OverlayTombstone) || row.TombstoneReason != string(TombstoneSourceExpiring) {
		t.Fatalf("disabled lifecycle maintenance favorite_id=%s state=%s tombstone_reason=%s version=%d created_at_present=%t updated_at_present=%t",
			row.ID, row.State, row.TombstoneReason, row.Version, !row.CreatedAt.IsZero(), !row.UpdatedAt.IsZero())
	}
	request.LifecycleAttemptID = strings.Repeat("d", 32)
	if _, err := disabled.ReconcileSourceLifecycle(context.Background(), request, SourceLifecycle{
		RecoveryPointID: pointID, Reason: SourceExpiring,
	}, 1); !errors.Is(err, backupasset.ErrConflict) && !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("mismatched lifecycle maintenance error=%v, want fail closed", err)
	}
}

func TestLifecycleDisabledMaintenanceDiagnosticsDoNotFormatDecryptedFavorite(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve lifecycle test source")
	}
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, filename, nil, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse lifecycle test source: %v", err)
	}
	live := analyzeLifecycleDiagnosticPrivacy(files, parsed, "TestLifecycleDisabledMaintenanceDiagnosticsDoNotFormatDecryptedFavorite")
	if live.lifecycleFunctions == 0 || live.savedSearchRoots == 0 || live.favoriteRoots == 0 ||
		live.tagAssignmentRoots == 0 || live.recentRoots == 0 || live.usageRoots == 0 || live.formattingCalls == 0 {
		t.Fatalf("lifecycle privacy coverage is vacuous: functions=%d saved_search_roots=%d favorite_roots=%d tag_assignment_roots=%d recent_roots=%d usage_roots=%d formatters=%d",
			live.lifecycleFunctions, live.savedSearchRoots, live.favoriteRoots, live.tagAssignmentRoots,
			live.recentRoots, live.usageRoots, live.formattingCalls)
	}

	const mutationSource = `package overlay
func lifecyclePrivacyOrdinaryHelper(t *testing.T, value any) {
	emit := t.Fatalf
	emit("helper=%q", value)
}
func lifecyclePrivacyForwardToOrdinaryHelper(t *testing.T, value any) {
	lifecyclePrivacyOrdinaryHelper(t, value)
}
func lifecyclePrivacyLoadFavorite() (value Favorite) { return }
func lifecyclePrivacyLoadFavoritePointer() (value *model.BackupAssetFavorite) { return }
func lifecyclePrivacyLoadSavedFirst() (value model.BackupAssetSavedSearch, _ error) { return }
func lifecyclePrivacyLoadAssignmentMiddle() (_ error, value model.BackupAssetTagAssignment, _ bool) {
	return
}
func lifecyclePrivacyLoadRecentLast() (_ error, _ string, value model.BackupAssetRecentAccess) { return }
func lifecyclePrivacyLoadUsageSecond() (_ error, value *model.BackupAssetOverlayUsage) { return }
func lifecyclePrivacyDeriveLabel(value Favorite) string { return value.Label + suffix }
func lifecyclePrivacyDeriveOwner(value model.BackupAssetFavorite) uint { return value.OwnerUserID + delta }
func lifecyclePrivacyDeriveUsage(value model.BackupAssetOverlayUsage) int64 {
	return value.RecentRateWindowWriteCount & mask
}
func lifecyclePrivacyGenericSink[T any](t *testing.T, value T) {
	t.Errorf("generic index sink=%s", value)
}
func lifecyclePrivacyGenericPairSink[A, B any](t *testing.T, _ A, value B) {
	t.Logf("generic index-list sink=%q", value)
}
func lifecyclePrivacyNonFormatter(_ string, _ ...any) {}
func TestLifecyclePrivacyMutationCanary(t *testing.T) {
	var saved SavedSearch
	var favorite Favorite
	var storedSaved model.BackupAssetSavedSearch
	var storedFavorite model.BackupAssetFavorite
	var storedAssignment model.BackupAssetTagAssignment
	var storedRecents []model.BackupAssetRecentAccess
	var storedUsage model.BackupAssetOverlayUsage
	loaded, _ := service.GetSavedSearch(ctx, ownerID, savedID)
	added, _ := service.AddFavorite(ctx, actor, request)
	assigned, _ := service.AssignTag(ctx, actor, tagID, ref, key)
	recorded, _ := service.RecordRecent(ctx, actor, ref)
	helperFavorite := lifecyclePrivacyLoadFavorite()
	helperFavoritePointer := lifecyclePrivacyLoadFavoritePointer()
	helperSaved, _ := lifecyclePrivacyLoadSavedFirst()
	_, helperAssignment, _ := lifecyclePrivacyLoadAssignmentMiddle()
	_, _, helperRecent := lifecyclePrivacyLoadRecentLast()
	_, helperUsage := lifecyclePrivacyLoadUsageSecond()
	savedAlias := saved
	favoritePointer := &favorite
	querySelector := saved.Query
	queryIndex := saved.Query.Scope.RecoveryPointIDs[0]
	genericIndex := storedFavorite[model.BackupAssetFavorite, string]
	converted := any(storedFavorite)
	called := cloneFavorite(storedFavorite)
	identity := func(value any) any { return value }
	container := []any{identity(added)}
	privateConcat := storedFavorite.EncryptedLabel + suffix
	privateArithmetic := storedFavorite.OwnerUserID + delta
	privateBitwise := storedUsage.RecentRateWindowWriteCount & mask
	privateRange := map[Favorite]SavedSearch{favorite: saved}
	formatter := t.Fatalf
	formatter("domain favorite=%v", added)
	formatter = fmt.Sprintf
	formatter(dynamicFormat, container)
	t.Errorf("saved=%v", savedAlias)
	t.Fatalf("favorite=%+v", favoritePointer)
	t.Logf("query=%[1]q", querySelector)
	fmt.Printf("index=%[1]+x", queryIndex)
	fmt.Sprintf("generic=%v", genericIndex)
	t.Errorf(dynamicFormat, converted)
	fmt.Sprintf(dynamicFormat, called)
	t.Errorf("stored saved=%v", storedSaved)
	t.Fatalf("stored assignment=%v", storedAssignment)
	t.Logf("stored recents=%v", storedRecents)
	fmt.Printf("stored usage=%v", storedUsage)
	fmt.Sprintf("loaded=%v", loaded)
	t.Errorf("assigned=%v", assigned)
	t.Logf("recorded=%v", recorded)
	t.Errorf("domain literal=%s", Favorite{})
	t.Fatalf("model saved literal=%q", model.BackupAssetSavedSearch{})
	t.Logf("model favorite literal=%x", model.BackupAssetFavorite{})
	fmt.Printf("model assignment literal=%[1]s", model.BackupAssetTagAssignment{})
	fmt.Sprintf("model recent literal=%[1]q", model.BackupAssetRecentAccess{})
	t.Errorf("model usage literal=%#x", model.BackupAssetOverlayUsage{})
	t.Errorf("typed helper favorite=%s", helperFavorite)
	t.Fatalf("typed helper favorite pointer=%q", helperFavoritePointer)
	t.Logf("typed helper saved=%x", helperSaved)
	fmt.Printf("typed helper assignment=%s", helperAssignment)
	fmt.Sprintf("typed helper recent=%q", helperRecent)
	t.Errorf("typed helper usage=%x", helperUsage)
	t.Errorf("private concat=%s", privateConcat)
	t.Fatalf("private arithmetic=%d", privateArithmetic)
	t.Logf("private bitwise=%x", privateBitwise)
	t.Errorf("helper private concat=%s", lifecyclePrivacyDeriveLabel(favorite))
	t.Fatalf("helper private arithmetic=%d", lifecyclePrivacyDeriveOwner(storedFavorite))
	t.Logf("helper private bitwise=%x", lifecyclePrivacyDeriveUsage(storedUsage))
	lifecyclePrivacyGenericSink[Favorite](t, favorite)
	lifecyclePrivacyGenericPairSink[string, model.BackupAssetFavorite](t, "", storedFavorite)
	shadowedFormatter := t.Fatalf
	{
		shadowedFormatter := lifecyclePrivacyNonFormatter
		shadowedFormatter("inner non-formatter")
	}
	shadowedFormatter("outer alias private=%v", storedFavorite)
	for rangeKey, rangeValue := range privateRange {
		t.Errorf("range private key=%s", rangeKey)
		t.Logf("range private value=%q", rangeValue)
	}
	ifFormatter := t.Fatalf
	ifFormat := "%v"
	if false {
		ifFormatter = lifecyclePrivacyNonFormatter
		ifFormat = "%t"
	}
	ifFormatter("if-joined formatter private=%v", favorite)
	fmt.Sprintf(ifFormat, favorite)
	loopFormatter := t.Fatalf
	loopFormat := "%v"
	for false {
		loopFormatter = lifecyclePrivacyNonFormatter
		loopFormat = "%t"
	}
	loopFormatter("loop-joined formatter private=%v", favorite)
	fmt.Sprintf(loopFormat, favorite)
	switchFormatter := t.Fatalf
	switchFormat := "%v"
	switch switchValue {
	case "safe":
		switchFormatter = lifecyclePrivacyNonFormatter
		switchFormat = "%t"
	}
	switchFormatter("switch-joined formatter private=%v", favorite)
	fmt.Sprintf(switchFormat, favorite)
	definiteFormatter := t.Fatalf
	definiteFormatter = lifecyclePrivacyNonFormatter
	definiteFormatter("definite non-formatter", favorite)
	definiteFormat := "%v"
	definiteFormat = "%t"
	fmt.Sprintf(definiteFormat, favorite.EncryptedLabel != "")
	lifecyclePrivacyForwardToOrdinaryHelper(t, helperFavorite)
}`
	mutationFiles := token.NewFileSet()
	mutation, mutationErr := parser.ParseFile(mutationFiles, "lifecycle_privacy_mutation.go", mutationSource, parser.AllErrors)
	if mutationErr != nil {
		t.Fatalf("parse lifecycle privacy mutation canary: %v", mutationErr)
	}
	mutationReport := analyzeLifecycleDiagnosticPrivacy(mutationFiles, mutation, "")
	wantMutationFormatters := map[string]int{"Errorf": 12, "Fatalf": 12, "Logf": 9, "Printf": 4, "Sprintf": 9}
	mutationExact := mutationReport.lifecycleFunctions == 15 && mutationReport.savedSearchRoots == 5 &&
		mutationReport.favoriteRoots == 9 && mutationReport.tagAssignmentRoots == 4 &&
		mutationReport.recentRoots == 4 && mutationReport.usageRoots == 4 &&
		mutationReport.formattingCalls == 47 && len(mutationReport.violations) == 46
	for formatter, want := range wantMutationFormatters {
		mutationExact = mutationExact && mutationReport.violationFormatters[formatter] == want
	}
	if !mutationExact {
		t.Fatalf("lifecycle privacy mutation canary mismatch: functions=%d saved_search_roots=%d favorite_roots=%d tag_assignment_roots=%d recent_roots=%d usage_roots=%d formatters=%d violations=%d formatter_counts=%v",
			mutationReport.lifecycleFunctions, mutationReport.savedSearchRoots, mutationReport.favoriteRoots,
			mutationReport.tagAssignmentRoots, mutationReport.recentRoots, mutationReport.usageRoots,
			mutationReport.formattingCalls, len(mutationReport.violations), mutationReport.violationFormatters)
	}
	const safeMutationSource = `package overlay
func lifecyclePrivacyFavoritePresent(value model.BackupAssetFavorite) bool {
	return value.EncryptedLabel != ""
}
func lifecyclePrivacySavedSearchEqual(value SavedSearch) bool { return value.Query == emptyQuery }
func lifecyclePrivacyFavoriteLess(value model.BackupAssetFavorite) bool {
	return value.RecoveryPointID < pointID
}
func TestLifecyclePrivacySafeScalarCanary(t *testing.T) {
	var saved SavedSearch
	var favorite model.BackupAssetFavorite
	equal := favorite.EncryptedLabel == label
	notEqual := favorite.RecoveryPointID != pointID
	less := favorite.EntryID < entryID
	greater := favorite.EncryptedLabel > label
	lessOrEqual := favorite.OwnerUserID <= ownerID
	greaterOrEqual := favorite.OwnerUserID >= ownerID
	allPresent := favorite.EncryptedLabel != "" && favorite.RecoveryPointID != ""
	anyPresent := favorite.EntryID != "" || saved.Query != emptyQuery
	fmt.Sprintf("safe id=%v state=%q reason=%s version=%d created=%t mode=%s comparisons=%t/%t/%t/%t/%t/%t/%t/%t helper=%t/%t/%t type=%T percent=%%",
		favorite.ID, favorite.State, favorite.TombstoneReason, favorite.Version,
		favorite.CreatedAt.IsZero(), saved.Query.Scope.Mode,
		equal, notEqual, less, greater, lessOrEqual, greaterOrEqual, allPresent, anyPresent,
		lifecyclePrivacyFavoritePresent(favorite), lifecyclePrivacySavedSearchEqual(saved),
		lifecyclePrivacyFavoriteLess(favorite), favorite)
}`
	safeMutationFiles := token.NewFileSet()
	safeMutation, safeMutationErr := parser.ParseFile(safeMutationFiles, "lifecycle_privacy_safe_mutation.go", safeMutationSource, parser.AllErrors)
	if safeMutationErr != nil {
		t.Fatalf("parse lifecycle privacy safe mutation canary: %v", safeMutationErr)
	}
	safeMutationReport := analyzeLifecycleDiagnosticPrivacy(safeMutationFiles, safeMutation, "")
	if safeMutationReport.lifecycleFunctions != 4 || safeMutationReport.savedSearchRoots != 2 ||
		safeMutationReport.favoriteRoots != 3 || safeMutationReport.formattingCalls != 1 ||
		len(safeMutationReport.violations) != 0 {
		t.Fatalf("lifecycle privacy safe mutation canary mismatch: functions=%d saved_search_roots=%d favorite_roots=%d formatters=%d violations=%d",
			safeMutationReport.lifecycleFunctions, safeMutationReport.savedSearchRoots,
			safeMutationReport.favoriteRoots, safeMutationReport.formattingCalls, len(safeMutationReport.violations))
	}
	if len(live.violations) > 0 {
		sites := make([]string, 0, len(live.violations))
		for _, violation := range live.violations {
			sites = append(sites, violation.function+":"+strconv.Itoa(violation.position.Line)+":"+
				violation.formatter+":"+violation.kinds.label()+":"+strconv.FormatBool(violation.dynamic))
		}
		t.Fatalf("lifecycle private diagnostic violations=%d sites=%s", len(live.violations), strings.Join(sites, ";"))
	}
}

type lifecyclePrivateKinds uint8

const (
	lifecyclePrivateSavedSearch lifecyclePrivateKinds = 1 << iota
	lifecyclePrivateFavorite
	lifecyclePrivateTagAssignment
	lifecyclePrivateRecent
	lifecyclePrivateUsage
)

type lifecyclePrivacyViolation struct {
	function  string
	formatter string
	position  token.Position
	kinds     lifecyclePrivateKinds
	dynamic   bool
}

type lifecyclePrivacyReport struct {
	lifecycleFunctions  int
	savedSearchRoots    int
	favoriteRoots       int
	tagAssignmentRoots  int
	recentRoots         int
	usageRoots          int
	formattingCalls     int
	violations          []lifecyclePrivacyViolation
	violationFormatters map[string]int
}

type lifecyclePrivacyFunctionState struct {
	declaration *ast.FuncDecl
	private     map[string]lifecyclePrivateKinds
	parameters  []string
	results     []lifecyclePrivateKinds
	resultNames []string
}

type lifecycleFormatterBinding struct {
	formatter string
}

type lifecycleFormatterScopes struct {
	bindings []map[string]lifecycleFormatterBinding
}

func analyzeLifecycleDiagnosticPrivacy(files *token.FileSet, parsed *ast.File, excludedFunction string) lifecyclePrivacyReport {
	report := lifecyclePrivacyReport{violationFormatters: make(map[string]int)}
	states := make([]*lifecyclePrivacyFunctionState, 0, len(parsed.Decls))
	statesByName := make(map[string]*lifecyclePrivacyFunctionState)
	resultKindsByFunction := make(map[string][]lifecyclePrivateKinds)
	countRoots := func(kinds lifecyclePrivateKinds) {
		if kinds&lifecyclePrivateSavedSearch != 0 {
			report.savedSearchRoots++
		}
		if kinds&lifecyclePrivateFavorite != 0 {
			report.favoriteRoots++
		}
		if kinds&lifecyclePrivateTagAssignment != 0 {
			report.tagAssignmentRoots++
		}
		if kinds&lifecyclePrivateRecent != 0 {
			report.recentRoots++
		}
		if kinds&lifecyclePrivateUsage != 0 {
			report.usageRoots++
		}
	}
	for _, declaration := range parsed.Decls {
		function, isFunction := declaration.(*ast.FuncDecl)
		if !isFunction || function.Body == nil || function.Name.Name == excludedFunction {
			continue
		}
		report.lifecycleFunctions++
		state := &lifecyclePrivacyFunctionState{
			declaration: function,
			private:     make(map[string]lifecyclePrivateKinds),
			parameters:  lifecycleFunctionParameterNames(function),
			results:     lifecycleFunctionResultKinds(function),
			resultNames: lifecycleFunctionResultNames(function),
		}
		states = append(states, state)
		statesByName[function.Name.Name] = state
		resultKindsByFunction[function.Name.Name] = state.results
	}
	for _, state := range states {
		function := state.declaration
		markRoot := func(name string, kinds lifecyclePrivateKinds) {
			if name == "" || name == "_" || kinds == 0 {
				return
			}
			added := kinds &^ state.private[name]
			state.private[name] |= kinds
			countRoots(added)
		}
		if function.Type.Params != nil {
			for _, field := range function.Type.Params.List {
				kinds := lifecyclePrivateTypeKinds(field.Type)
				for _, name := range field.Names {
					markRoot(name.Name, kinds)
				}
			}
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch statement := node.(type) {
			case *ast.CompositeLit:
				countRoots(lifecyclePrivateTypeKinds(statement.Type))
			case *ast.ValueSpec:
				kinds := lifecyclePrivateTypeKinds(statement.Type)
				for _, name := range statement.Names {
					markRoot(name.Name, kinds)
				}
				assigned := lifecycleAssignedCallResultKinds(len(statement.Names), statement.Values, resultKindsByFunction)
				for index, kinds := range assigned {
					markRoot(statement.Names[index].Name, kinds)
				}
			case *ast.AssignStmt:
				assigned := lifecycleAssignedCallResultKinds(len(statement.Lhs), statement.Rhs, resultKindsByFunction)
				for index, kinds := range assigned {
					left, isIdentifier := statement.Lhs[index].(*ast.Ident)
					if isIdentifier {
						markRoot(left.Name, kinds)
					}
				}
			}
			return true
		})
	}

	for changed := true; changed; {
		changed = false
		for _, state := range states {
			if lifecyclePropagatePrivateAssignments(state.declaration.Body, state.private, resultKindsByFunction) {
				changed = true
			}
		}
		for _, caller := range states {
			ast.Inspect(caller.declaration.Body, func(node ast.Node) bool {
				call, isCall := node.(*ast.CallExpr)
				if !isCall {
					return true
				}
				calleeName := lifecycleLocalCalleeName(call.Fun)
				if calleeName == "" {
					return true
				}
				callee := statesByName[calleeName]
				if callee == nil {
					return true
				}
				for index, argument := range call.Args {
					if index >= len(callee.parameters) || callee.parameters[index] == "" {
						continue
					}
					kinds := lifecyclePrivateExpressionKinds(argument, caller.private, resultKindsByFunction)
					parameter := callee.parameters[index]
					if added := kinds &^ callee.private[parameter]; added != 0 {
						callee.private[parameter] |= added
						changed = true
					}
				}
				return true
			})
		}
		for _, state := range states {
			if lifecyclePropagateFunctionResults(state, resultKindsByFunction) {
				changed = true
			}
		}
	}

	for _, state := range states {
		function := state.declaration
		private := state.private
		formatterAliases := &lifecycleFormatterScopes{}
		scopeNodes := make([]bool, 0)
		controlNodes := make([]bool, 0)
		conditionalDepth := 0
		ast.Inspect(function.Body, func(node ast.Node) bool {
			if node == nil {
				createsScope := scopeNodes[len(scopeNodes)-1]
				scopeNodes = scopeNodes[:len(scopeNodes)-1]
				if createsScope {
					formatterAliases.pop()
				}
				createsControl := controlNodes[len(controlNodes)-1]
				controlNodes = controlNodes[:len(controlNodes)-1]
				if createsControl {
					conditionalDepth--
				}
				return true
			}
			_, createsScope := node.(*ast.BlockStmt)
			scopeNodes = append(scopeNodes, createsScope)
			if createsScope {
				formatterAliases.push()
			}
			createsControl := lifecycleConditionalControlNode(node)
			controlNodes = append(controlNodes, createsControl)
			if createsControl {
				conditionalDepth++
			}
			switch statement := node.(type) {
			case *ast.AssignStmt:
				if len(statement.Lhs) == len(statement.Rhs) {
					for index, right := range statement.Rhs {
						left, isIdentifier := statement.Lhs[index].(*ast.Ident)
						if !isIdentifier || left.Name == "_" {
							continue
						}
						formatter := lifecycleFormatterName(right, formatterAliases)
						if statement.Tok == token.DEFINE {
							formatterAliases.declare(left.Name, formatter)
						} else {
							formatterAliases.assign(left.Name, formatter, conditionalDepth > 0)
						}
					}
				}
			case *ast.ValueSpec:
				if len(statement.Names) == len(statement.Values) {
					for index, right := range statement.Values {
						left := statement.Names[index]
						formatterAliases.declare(left.Name, lifecycleFormatterName(right, formatterAliases))
					}
				}
			}
			call, isCall := node.(*ast.CallExpr)
			if !isCall || len(call.Args) == 0 {
				return true
			}
			formatter := lifecycleFormatterName(call.Fun, formatterAliases)
			if formatter == "" {
				return true
			}
			report.formattingCalls++
			argumentKinds := make([]lifecyclePrivateKinds, 0, len(call.Args)-1)
			for _, argument := range call.Args[1:] {
				argumentKinds = append(argumentKinds, lifecyclePrivateExpressionKinds(argument, private, resultKindsByFunction))
			}
			kinds := lifecyclePrivateKindsForArguments(argumentKinds)
			if kinds == 0 {
				return true
			}
			literal, isLiteral := call.Args[0].(*ast.BasicLit)
			dynamic := !isLiteral || literal.Kind != token.STRING
			violatingKinds := kinds
			if !dynamic {
				format, unquoteErr := strconv.Unquote(literal.Value)
				if unquoteErr == nil {
					violatingKinds = lifecycleFormatViolatingKinds(format, argumentKinds)
				}
			}
			if violatingKinds != 0 {
				report.violations = append(report.violations, lifecyclePrivacyViolation{
					function: function.Name.Name, formatter: formatter, position: files.Position(call.Pos()), kinds: violatingKinds, dynamic: dynamic,
				})
				report.violationFormatters[formatter]++
			}
			return true
		})
	}
	return report
}

func lifecycleFunctionParameterNames(function *ast.FuncDecl) []string {
	if function.Type.Params == nil {
		return nil
	}
	parameters := make([]string, 0, function.Type.Params.NumFields())
	for _, field := range function.Type.Params.List {
		if len(field.Names) == 0 {
			parameters = append(parameters, "")
			continue
		}
		for _, name := range field.Names {
			parameters = append(parameters, name.Name)
		}
	}
	return parameters
}

func lifecycleFunctionResultKinds(function *ast.FuncDecl) []lifecyclePrivateKinds {
	if function.Type.Results == nil {
		return nil
	}
	results := make([]lifecyclePrivateKinds, 0, function.Type.Results.NumFields())
	for _, field := range function.Type.Results.List {
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		for range count {
			results = append(results, lifecyclePrivateTypeKinds(field.Type))
		}
	}
	return results
}

func lifecycleFunctionResultNames(function *ast.FuncDecl) []string {
	if function.Type.Results == nil {
		return nil
	}
	names := make([]string, 0, function.Type.Results.NumFields())
	for _, field := range function.Type.Results.List {
		if len(field.Names) == 0 {
			names = append(names, "")
			continue
		}
		for _, name := range field.Names {
			names = append(names, name.Name)
		}
	}
	return names
}

func lifecyclePropagateFunctionResults(
	state *lifecyclePrivacyFunctionState,
	resultKindsByFunction map[string][]lifecyclePrivateKinds,
) bool {
	if len(state.results) == 0 {
		return false
	}
	changed := false
	ast.Inspect(state.declaration.Body, func(node ast.Node) bool {
		statement, isReturn := node.(*ast.ReturnStmt)
		if !isReturn {
			return true
		}
		returned := make([]lifecyclePrivateKinds, len(state.results))
		if len(statement.Results) == 0 {
			for index, name := range state.resultNames {
				if name != "" {
					returned[index] = state.private[name]
				}
			}
		} else {
			returned = lifecycleAssignedExpressionKinds(
				len(state.results), statement.Results, state.private, resultKindsByFunction,
			)
		}
		for index, kinds := range returned {
			if added := kinds &^ state.results[index]; added != 0 {
				state.results[index] |= added
				changed = true
			}
		}
		return true
	})
	return changed
}

func lifecycleAssignedCallResultKinds(
	leftCount int,
	right []ast.Expr,
	resultKindsByFunction map[string][]lifecyclePrivateKinds,
) []lifecyclePrivateKinds {
	assigned := make([]lifecyclePrivateKinds, leftCount)
	if len(right) == 1 {
		results := lifecyclePrivateCallResultKinds(right[0], resultKindsByFunction)
		copy(assigned, results)
		return assigned
	}
	for index, expression := range right {
		if index >= len(assigned) {
			break
		}
		results := lifecyclePrivateCallResultKinds(expression, resultKindsByFunction)
		if len(results) > 0 {
			assigned[index] = results[0]
		}
	}
	return assigned
}

func lifecyclePropagatePrivateAssignments(
	body *ast.BlockStmt,
	private map[string]lifecyclePrivateKinds,
	resultKindsByFunction map[string][]lifecyclePrivateKinds,
) bool {
	changed := false
	ast.Inspect(body, func(node ast.Node) bool {
		switch statement := node.(type) {
		case *ast.AssignStmt:
			assigned := lifecycleAssignedExpressionKinds(len(statement.Lhs), statement.Rhs, private, resultKindsByFunction)
			for index, kinds := range assigned {
				left, isIdentifier := statement.Lhs[index].(*ast.Ident)
				if !isIdentifier || left.Name == "_" || left.Name == "err" {
					continue
				}
				if added := kinds &^ private[left.Name]; added != 0 {
					private[left.Name] |= added
					changed = true
				}
			}
		case *ast.ValueSpec:
			assigned := lifecycleAssignedExpressionKinds(len(statement.Names), statement.Values, private, resultKindsByFunction)
			for index, kinds := range assigned {
				left := statement.Names[index]
				if left.Name == "_" || left.Name == "err" {
					continue
				}
				if added := kinds &^ private[left.Name]; added != 0 {
					private[left.Name] |= added
					changed = true
				}
			}
		case *ast.RangeStmt:
			kinds := lifecyclePrivateExpressionKinds(statement.X, private, resultKindsByFunction)
			for _, expression := range []ast.Expr{statement.Key, statement.Value} {
				identifier, isIdentifier := expression.(*ast.Ident)
				if !isIdentifier || identifier.Name == "_" || identifier.Name == "err" {
					continue
				}
				if added := kinds &^ private[identifier.Name]; added != 0 {
					private[identifier.Name] |= added
					changed = true
				}
			}
		}
		return true
	})
	return changed
}

func lifecycleAssignedExpressionKinds(
	leftCount int,
	right []ast.Expr,
	private map[string]lifecyclePrivateKinds,
	resultKindsByFunction map[string][]lifecyclePrivateKinds,
) []lifecyclePrivateKinds {
	assigned := make([]lifecyclePrivateKinds, leftCount)
	if len(right) == 1 {
		if results := lifecyclePrivateCallResultKinds(right[0], resultKindsByFunction); len(results) > 0 {
			copy(assigned, results)
			return assigned
		}
		if leftCount == 1 {
			assigned[0] = lifecyclePrivateExpressionKinds(right[0], private, resultKindsByFunction)
		}
		return assigned
	}
	for index, expression := range right {
		if index >= len(assigned) {
			break
		}
		assigned[index] = lifecyclePrivateExpressionKinds(expression, private, resultKindsByFunction)
	}
	return assigned
}

func lifecyclePrivateTypeKinds(expression ast.Expr) lifecyclePrivateKinds {
	switch value := expression.(type) {
	case *ast.Ident:
		switch value.Name {
		case "SavedSearch":
			return lifecyclePrivateSavedSearch
		case "Favorite":
			return lifecyclePrivateFavorite
		case "TagAssignment":
			return lifecyclePrivateTagAssignment
		case "RecentAccess":
			return lifecyclePrivateRecent
		}
	case *ast.ArrayType:
		return lifecyclePrivateTypeKinds(value.Elt)
	case *ast.ParenExpr:
		return lifecyclePrivateTypeKinds(value.X)
	case *ast.StarExpr:
		return lifecyclePrivateTypeKinds(value.X)
	case *ast.SelectorExpr:
		packageName, isPackage := value.X.(*ast.Ident)
		if !isPackage || packageName.Name != "model" {
			return 0
		}
		switch value.Sel.Name {
		case "BackupAssetSavedSearch":
			return lifecyclePrivateSavedSearch
		case "BackupAssetFavorite":
			return lifecyclePrivateFavorite
		case "BackupAssetTagAssignment":
			return lifecyclePrivateTagAssignment
		case "BackupAssetRecentAccess":
			return lifecyclePrivateRecent
		case "BackupAssetOverlayUsage":
			return lifecyclePrivateUsage
		}
	}
	return 0
}

func lifecyclePrivateCallResultKinds(
	expression ast.Expr,
	resultKindsByFunction map[string][]lifecyclePrivateKinds,
) []lifecyclePrivateKinds {
	call, isCall := expression.(*ast.CallExpr)
	if !isCall {
		return nil
	}
	if calleeName := lifecycleLocalCalleeName(call.Fun); calleeName != "" {
		if results, ok := resultKindsByFunction[calleeName]; ok {
			return results
		}
	}
	switch lifecycleCalleeName(call.Fun) {
	case "CreateSavedSearch", "GetSavedSearch", "ListSavedSearches", "UpdateSavedSearch", "UseSavedSearch", "ValidateSavedSearchForExportTx":
		return []lifecyclePrivateKinds{lifecyclePrivateSavedSearch, 0}
	case "AddFavorite", "AddFavorites", "ListFavorites":
		return []lifecyclePrivateKinds{lifecyclePrivateFavorite, 0}
	case "AssignTag":
		return []lifecyclePrivateKinds{lifecyclePrivateTagAssignment, 0}
	case "RecordRecent", "ListRecent":
		return []lifecyclePrivateKinds{lifecyclePrivateRecent, 0}
	default:
		return nil
	}
}

func lifecyclePrivateExpressionKinds(
	expression ast.Expr,
	private map[string]lifecyclePrivateKinds,
	resultKindsByFunction map[string][]lifecyclePrivateKinds,
) lifecyclePrivateKinds {
	if expression == nil {
		return 0
	}
	switch value := expression.(type) {
	case *ast.Ident:
		return private[value.Name]
	case *ast.ParenExpr:
		return lifecyclePrivateExpressionKinds(value.X, private, resultKindsByFunction)
	case *ast.UnaryExpr:
		return lifecyclePrivateExpressionKinds(value.X, private, resultKindsByFunction)
	case *ast.StarExpr:
		return lifecyclePrivateExpressionKinds(value.X, private, resultKindsByFunction)
	case *ast.SelectorExpr:
		if value.Sel.Name == "Error" {
			return 0
		}
		kinds := lifecyclePrivateExpressionKinds(value.X, private, resultKindsByFunction)
		if kinds != 0 && lifecyclePrivateSafeSelector(value.Sel.Name) {
			return 0
		}
		return kinds
	case *ast.IndexExpr:
		return lifecyclePrivateExpressionKinds(value.X, private, resultKindsByFunction) |
			lifecyclePrivateExpressionKinds(value.Index, private, resultKindsByFunction)
	case *ast.IndexListExpr:
		kinds := lifecyclePrivateExpressionKinds(value.X, private, resultKindsByFunction)
		for _, index := range value.Indices {
			kinds |= lifecyclePrivateExpressionKinds(index, private, resultKindsByFunction)
		}
		return kinds
	case *ast.SliceExpr:
		return lifecyclePrivateExpressionKinds(value.X, private, resultKindsByFunction) |
			lifecyclePrivateExpressionKinds(value.Low, private, resultKindsByFunction) |
			lifecyclePrivateExpressionKinds(value.High, private, resultKindsByFunction) |
			lifecyclePrivateExpressionKinds(value.Max, private, resultKindsByFunction)
	case *ast.CallExpr:
		if identifier, isIdentifier := value.Fun.(*ast.Ident); isIdentifier &&
			(identifier.Name == "len" || identifier.Name == "cap") {
			return 0
		}
		if results := lifecyclePrivateCallResultKinds(value, resultKindsByFunction); len(results) > 0 {
			return results[0]
		}
		kinds := lifecyclePrivateExpressionKinds(value.Fun, private, resultKindsByFunction)
		for _, argument := range value.Args {
			kinds |= lifecyclePrivateExpressionKinds(argument, private, resultKindsByFunction)
		}
		return kinds
	case *ast.TypeAssertExpr:
		return lifecyclePrivateExpressionKinds(value.X, private, resultKindsByFunction)
	case *ast.KeyValueExpr:
		return lifecyclePrivateExpressionKinds(value.Key, private, resultKindsByFunction) |
			lifecyclePrivateExpressionKinds(value.Value, private, resultKindsByFunction)
	case *ast.CompositeLit:
		kinds := lifecyclePrivateTypeKinds(value.Type)
		for _, element := range value.Elts {
			kinds |= lifecyclePrivateExpressionKinds(element, private, resultKindsByFunction)
		}
		return kinds
	case *ast.BinaryExpr:
		switch value.Op {
		case token.EQL, token.NEQ, token.LSS, token.GTR, token.LEQ, token.GEQ, token.LAND, token.LOR:
			return 0
		}
		return lifecyclePrivateExpressionKinds(value.X, private, resultKindsByFunction) |
			lifecyclePrivateExpressionKinds(value.Y, private, resultKindsByFunction)
	case *ast.Ellipsis:
		return lifecyclePrivateExpressionKinds(value.Elt, private, resultKindsByFunction)
	default:
		return 0
	}
}

func lifecyclePrivateSafeSelector(name string) bool {
	switch name {
	case "ID", "State", "StateReason", "Reason", "TombstoneReason", "Version",
		"BrokenAt", "CreatedAt", "UpdatedAt", "AccessCount", "LastAccessedAt", "ExpiresAt",
		"SavedSearchCount", "FavoriteCount", "TagDefinitionCount", "TagAssignmentCount", "RecentCount", "Mode":
		return true
	default:
		return false
	}
}

func lifecycleCalleeName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return value.Sel.Name
	case *ast.IndexExpr:
		return lifecycleCalleeName(value.X)
	case *ast.IndexListExpr:
		return lifecycleCalleeName(value.X)
	default:
		return ""
	}
}

func lifecycleLocalCalleeName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.IndexExpr:
		return lifecycleLocalCalleeName(value.X)
	case *ast.IndexListExpr:
		return lifecycleLocalCalleeName(value.X)
	case *ast.ParenExpr:
		return lifecycleLocalCalleeName(value.X)
	default:
		return ""
	}
}

func (scopes *lifecycleFormatterScopes) push() {
	scopes.bindings = append(scopes.bindings, make(map[string]lifecycleFormatterBinding))
}

func (scopes *lifecycleFormatterScopes) pop() {
	scopes.bindings = scopes.bindings[:len(scopes.bindings)-1]
}

func (scopes *lifecycleFormatterScopes) declare(name, formatter string) {
	scopes.bindings[len(scopes.bindings)-1][name] = lifecycleFormatterBinding{formatter: formatter}
}

func (scopes *lifecycleFormatterScopes) assign(name, formatter string, conservative bool) {
	for index := len(scopes.bindings) - 1; index >= 0; index-- {
		if binding, exists := scopes.bindings[index][name]; exists {
			if conservative {
				if binding.formatter == "" && formatter != "" {
					scopes.bindings[index][name] = lifecycleFormatterBinding{formatter: formatter}
				}
				return
			}
			scopes.bindings[index][name] = lifecycleFormatterBinding{formatter: formatter}
			return
		}
	}
	scopes.declare(name, formatter)
}

func lifecycleConditionalControlNode(node ast.Node) bool {
	switch node.(type) {
	case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
		return true
	default:
		return false
	}
}

func (scopes *lifecycleFormatterScopes) lookup(name string) (string, bool) {
	for index := len(scopes.bindings) - 1; index >= 0; index-- {
		if binding, exists := scopes.bindings[index][name]; exists {
			return binding.formatter, true
		}
	}
	return "", false
}

func lifecycleFormatterName(expression ast.Expr, aliases *lifecycleFormatterScopes) string {
	if identifier, isIdentifier := expression.(*ast.Ident); isIdentifier {
		if formatter, bound := aliases.lookup(identifier.Name); bound {
			return formatter
		}
	}
	name := lifecycleCalleeName(expression)
	switch name {
	case "Errorf", "Fatalf", "Logf", "Printf", "Sprintf":
		return name
	default:
		return ""
	}
}

func lifecyclePrivateKindsForArguments(arguments []lifecyclePrivateKinds) lifecyclePrivateKinds {
	var kinds lifecyclePrivateKinds
	for _, argument := range arguments {
		kinds |= argument
	}
	return kinds
}

func lifecycleFormatViolatingKinds(format string, arguments []lifecyclePrivateKinds) lifecyclePrivateKinds {
	consumed := make([]bool, len(arguments))
	var violating lifecyclePrivateKinds
	nextArgument := 0
	consume := func(index int, typeOnly bool) bool {
		if index < 0 || index >= len(arguments) {
			return false
		}
		consumed[index] = true
		if !typeOnly {
			violating |= arguments[index]
		}
		return true
	}
	for offset := 0; offset < len(format); offset++ {
		if format[offset] != '%' {
			continue
		}
		cursor := offset + 1
		if cursor >= len(format) {
			return lifecyclePrivateKindsForArguments(arguments)
		}
		if format[cursor] == '%' {
			offset = cursor
			continue
		}
		for cursor < len(format) && strings.ContainsRune("+#- 0", rune(format[cursor])) {
			cursor++
		}
		valueArgument := -1
		if index, after, ok := lifecycleFormatArgumentIndex(format, cursor); ok {
			nextArgument = index + 1
			cursor = after
			if cursor < len(format) && format[cursor] == '*' {
				if !consume(index, false) {
					return lifecyclePrivateKindsForArguments(arguments)
				}
				cursor++
			} else {
				valueArgument = index
			}
		}
		for cursor < len(format) && strings.ContainsRune("+#- 0", rune(format[cursor])) {
			cursor++
		}
		if cursor < len(format) && format[cursor] == '*' {
			if !consume(nextArgument, false) {
				return lifecyclePrivateKindsForArguments(arguments)
			}
			nextArgument++
			cursor++
		} else {
			for cursor < len(format) && format[cursor] >= '0' && format[cursor] <= '9' {
				cursor++
			}
		}
		if cursor < len(format) && format[cursor] == '.' {
			cursor++
			if index, after, ok := lifecycleFormatArgumentIndex(format, cursor); ok &&
				after < len(format) && format[after] == '*' {
				nextArgument = index + 1
				if !consume(index, false) {
					return lifecyclePrivateKindsForArguments(arguments)
				}
				cursor = after + 1
			} else if cursor < len(format) && format[cursor] == '*' {
				if !consume(nextArgument, false) {
					return lifecyclePrivateKindsForArguments(arguments)
				}
				nextArgument++
				cursor++
			} else {
				for cursor < len(format) && format[cursor] >= '0' && format[cursor] <= '9' {
					cursor++
				}
			}
		}
		if index, after, ok := lifecycleFormatArgumentIndex(format, cursor); ok {
			valueArgument = index
			nextArgument = index + 1
			cursor = after
		}
		if cursor >= len(format) {
			return lifecyclePrivateKindsForArguments(arguments)
		}
		verb := format[cursor]
		if verb != '%' {
			if valueArgument < 0 {
				valueArgument = nextArgument
				nextArgument++
			}
			if !consume(valueArgument, verb == 'T') {
				return lifecyclePrivateKindsForArguments(arguments)
			}
		}
		offset = cursor
	}
	for index, kinds := range arguments {
		if kinds != 0 && !consumed[index] {
			violating |= kinds
		}
	}
	return violating
}

func lifecycleFormatArgumentIndex(format string, offset int) (int, int, bool) {
	if offset >= len(format) || format[offset] != '[' {
		return 0, offset, false
	}
	closing := strings.IndexByte(format[offset:], ']')
	if closing <= 1 {
		return 0, offset, false
	}
	closing += offset
	index, err := strconv.Atoi(format[offset+1 : closing])
	if err != nil || index <= 0 {
		return 0, offset, false
	}
	return index - 1, closing + 1, true
}

func (kinds lifecyclePrivateKinds) label() string {
	labels := make([]string, 0, 5)
	for _, candidate := range []struct {
		kind  lifecyclePrivateKinds
		label string
	}{
		{lifecyclePrivateSavedSearch, "saved_search"},
		{lifecyclePrivateFavorite, "favorite"},
		{lifecyclePrivateTagAssignment, "tag_assignment"},
		{lifecyclePrivateRecent, "recent"},
		{lifecyclePrivateUsage, "usage"},
	} {
		if kinds&candidate.kind != 0 {
			labels = append(labels, candidate.label)
		}
	}
	if len(labels) == 0 {
		return "unknown"
	}
	return strings.Join(labels, "+")
}

func TestLifecycleOverlayPostgresLockedRecentCannotFalseZero(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_OVERLAY_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_OVERLAY_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	service, harness := newOverlayPostgresTestHarness(t, dsn)
	actor := Actor{UserID: 798, Role: "operator"}
	pointID := strings.Repeat("6", 32)
	ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat("6", 64)}
	harness.assets[ref] = true
	recent, err := service.RecordRecent(context.Background(), actor, ref)
	if err != nil {
		t.Fatalf("seed recent access: %v", err)
	}
	now := harness.clock.Now()
	attemptID := strings.Repeat("5", 32)
	if err := harness.db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: attemptID, RecoveryPointID: pointID, Operation: string(backupasset.LifecycleRetentionExpire),
		Phase: string(backupasset.LifecyclePhaseCleaning), TransitionRevision: 1,
		ClaimedAt: &now, HeartbeatAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed lifecycle attempt: %v", err)
	}

	locker := harness.db.Begin()
	if locker.Error != nil {
		t.Fatalf("begin recent row locker: %v", locker.Error)
	}
	lockerOpen := true
	t.Cleanup(func() {
		if lockerOpen {
			_ = locker.Rollback().Error
		}
	})
	var locked model.BackupAssetRecentAccess
	if err := locker.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", recent.ID).Take(&locked).Error; err != nil {
		t.Fatalf("lock sole recent row: %v", err)
	}

	type lifecycleCall struct {
		result LifecycleResult
		err    error
	}
	callDone := make(chan lifecycleCall, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	go func() {
		result, callErr := service.ReconcileSourceLifecycle(ctx, backupasset.SourceLifecycleRequest{
			RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
			Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecycleCleanup,
		}, SourceLifecycle{RecoveryPointID: pointID, Reason: SourceExpiring}, 1)
		callDone <- lifecycleCall{result: result, err: callErr}
	}()

	var early *lifecycleCall
	select {
	case completed := <-callDone:
		early = &completed
	case <-time.After(200 * time.Millisecond):
	}
	if err := locker.Rollback().Error; err != nil {
		t.Fatalf("release sole recent row: %v", err)
	}
	lockerOpen = false
	if early != nil {
		t.Fatalf("locked sole recent completed before rollback: result=%+v err=%v", early.result, early.err)
	}

	select {
	case completed := <-callDone:
		if completed.err != nil || completed.result.RecentsDeleted != 1 {
			t.Fatalf("post-rollback lifecycle result=%+v err=%v, want sole recent processed", completed.result, completed.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lifecycle did not process the sole recent after row-lock rollback")
	}
	var survivors int64
	if err := harness.db.Model(&model.BackupAssetRecentAccess{}).Where("id = ?", recent.ID).Count(&survivors).Error; err != nil {
		t.Fatalf("count recent survivors: %v", err)
	}
	if survivors != 0 {
		t.Fatalf("lifecycle returned with %d locked recent survivor(s)", survivors)
	}
}

func TestLifecycleOverlayPostgresClearRecentUsesCanonicalLockOrderWithoutRetry(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_OVERLAY_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_OVERLAY_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	service, harness := newOverlayPostgresTestHarness(t, dsn)
	actor := Actor{UserID: 797, Role: "operator"}
	pointID := strings.Repeat("7", 32)
	ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat("7", 64)}
	harness.assets[ref] = true
	if _, err := service.RecordRecent(context.Background(), actor, ref); err != nil {
		t.Fatalf("seed recent access: %v", err)
	}
	now := harness.clock.Now()
	attemptID := strings.Repeat("8", 32)
	if err := harness.db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: attemptID, RecoveryPointID: pointID, Operation: string(backupasset.LifecycleRetentionExpire),
		Phase: string(backupasset.LifecyclePhaseCleaning), TransitionRevision: 1,
		ClaimedAt: &now, HeartbeatAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed lifecycle attempt: %v", err)
	}

	type operationMarker struct{}
	const (
		lifecycleOperation = "lifecycle"
		clearOperation     = "clear"
	)
	lifecycleRecentReady := make(chan struct{}, 1)
	clearDeleteReady := make(chan struct{}, 1)
	releaseBoth := make(chan struct{})
	var lifecycleReadyOnce sync.Once
	var clearReadyOnce sync.Once
	var lifecycleTransactions sync.Map
	var clearTransactions sync.Map
	if err := harness.db.Callback().Query().After("gorm:query").Register(
		"overlay_postgres_lifecycle_recent_lock_barrier",
		func(tx *gorm.DB) {
			if tx.Statement.Context.Value(operationMarker{}) != lifecycleOperation ||
				tx.Statement.Table != "backup_asset_recent_access" {
				return
			}
			lifecycleTransactions.Store(tx.Statement.ConnPool, struct{}{})
			lifecycleReadyOnce.Do(func() {
				lifecycleRecentReady <- struct{}{}
				select {
				case <-releaseBoth:
				case <-tx.Statement.Context.Done():
					_ = tx.AddError(tx.Statement.Context.Err())
				}
			})
		},
	); err != nil {
		t.Fatalf("register lifecycle recent barrier: %v", err)
	}
	if err := harness.db.Callback().Delete().Before("gorm:delete").Register(
		"overlay_postgres_clear_recent_delete_barrier",
		func(tx *gorm.DB) {
			if tx.Statement.Context.Value(operationMarker{}) != clearOperation ||
				tx.Statement.Table != "backup_asset_recent_access" {
				return
			}
			clearTransactions.Store(tx.Statement.ConnPool, struct{}{})
			clearReadyOnce.Do(func() {
				clearDeleteReady <- struct{}{}
				select {
				case <-releaseBoth:
				case <-tx.Statement.Context.Done():
					_ = tx.AddError(tx.Statement.Context.Err())
				}
			})
		},
	); err != nil {
		_ = harness.db.Callback().Query().Remove("overlay_postgres_lifecycle_recent_lock_barrier")
		t.Fatalf("register ClearRecent delete barrier: %v", err)
	}
	t.Cleanup(func() {
		_ = harness.db.Callback().Delete().Remove("overlay_postgres_clear_recent_delete_barrier")
		_ = harness.db.Callback().Query().Remove("overlay_postgres_lifecycle_recent_lock_barrier")
	})

	type lifecycleCall struct {
		result LifecycleResult
		err    error
	}
	type clearCall struct {
		cleared int64
		err     error
	}
	lifecycleDone := make(chan lifecycleCall, 1)
	clearDone := make(chan clearCall, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		result, err := service.ReconcileSourceLifecycle(
			context.WithValue(ctx, operationMarker{}, lifecycleOperation),
			backupasset.SourceLifecycleRequest{
				RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
				Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecycleCleanup,
			},
			SourceLifecycle{RecoveryPointID: pointID, Reason: SourceExpiring},
			1,
		)
		lifecycleDone <- lifecycleCall{result: result, err: err}
	}()
	select {
	case <-lifecycleRecentReady:
	case <-ctx.Done():
		close(releaseBoth)
		t.Fatalf("lifecycle did not reach the recent discovery barrier: %v", ctx.Err())
	}
	go func() {
		cleared, err := service.ClearRecent(
			context.WithValue(ctx, operationMarker{}, clearOperation),
			actor.UserID,
			"lifecycle-clear-lock-order",
		)
		clearDone <- clearCall{cleared: cleared, err: err}
	}()
	select {
	case <-clearDeleteReady:
	case <-ctx.Done():
		close(releaseBoth)
		t.Fatalf("ClearRecent did not reach the post-usage delete barrier: %v", ctx.Err())
	}
	close(releaseBoth)

	var lifecycleOutcome lifecycleCall
	select {
	case lifecycleOutcome = <-lifecycleDone:
	case <-ctx.Done():
		t.Fatalf("lifecycle did not finish after the barriers opened: %v", ctx.Err())
	}
	var clearOutcome clearCall
	select {
	case clearOutcome = <-clearDone:
	case <-ctx.Done():
		t.Fatalf("ClearRecent did not finish after the barriers opened: %v", ctx.Err())
	}
	countTransactions := func(transactions *sync.Map) int {
		count := 0
		transactions.Range(func(_, _ any) bool {
			count++
			return true
		})
		return count
	}
	lifecycleAttempts := countTransactions(&lifecycleTransactions)
	clearAttempts := countTransactions(&clearTransactions)
	deadlocks := 0
	for _, err := range []error{lifecycleOutcome.err, clearOutcome.err} {
		if err != nil && strings.Contains(strings.ToUpper(err.Error()), "SQLSTATE 40P01") {
			deadlocks++
		}
	}
	if lifecycleRetries, clearRetries := max(lifecycleAttempts-1, 0), max(clearAttempts-1, 0); deadlocks != 0 || lifecycleRetries != 0 || clearRetries != 0 {
		t.Fatalf("deadlocks=%d lifecycle_retries=%d clear_retries=%d lifecycle_err=%v clear_err=%v",
			deadlocks, lifecycleRetries, clearRetries, lifecycleOutcome.err, clearOutcome.err)
	}
	if lifecycleOutcome.err != nil || clearOutcome.err != nil {
		t.Fatalf("lifecycle result=%+v err=%v ClearRecent cleared=%d err=%v",
			lifecycleOutcome.result, lifecycleOutcome.err, clearOutcome.cleared, clearOutcome.err)
	}
	if lifecycleOutcome.result.RecentsDeleted != 0 || clearOutcome.cleared != 1 {
		t.Fatalf("post-lock requery lifecycle_deleted=%d ClearRecent_cleared=%d, want exact zero/one proof",
			lifecycleOutcome.result.RecentsDeleted, clearOutcome.cleared)
	}
	var recentCount int64
	if err := harness.db.Model(&model.BackupAssetRecentAccess{}).
		Where("owner_user_id = ? AND recovery_point_id = ?", actor.UserID, pointID).
		Count(&recentCount).Error; err != nil {
		t.Fatalf("count final recent rows: %v", err)
	}
	var usage model.BackupAssetOverlayUsage
	if err := harness.db.Where("owner_user_id = ?", actor.UserID).Take(&usage).Error; err != nil {
		t.Fatalf("load final overlay usage: %v", err)
	}
	if recentCount != 0 || usage.RecentCount != 0 || usage.RecentCount != recentCount {
		t.Fatalf("final recent/usage inconsistency: recent_rows=%d usage_recent_count=%d", recentCount, usage.RecentCount)
	}
}

func TestLifecycleOverlayPostgresCleanupExpiredRecentUsesCanonicalLockOrderWithoutRetry(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_OVERLAY_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_OVERLAY_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	service, harness := newOverlayPostgresTestHarness(t, dsn)
	actor := Actor{UserID: 796, Role: "operator"}
	pointID := strings.Repeat("9", 32)
	ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat("9", 64)}
	harness.assets[ref] = true
	if _, err := service.RecordRecent(context.Background(), actor, ref); err != nil {
		t.Fatalf("seed recent access: %v", err)
	}
	harness.clock.Advance(31 * 24 * time.Hour)

	type operationMarker struct{}
	const (
		cleanupOperation = "cleanup_expired"
		clearOperation   = "clear"
	)
	cleanupRecentReady := make(chan struct{}, 1)
	clearDeleteReady := make(chan struct{}, 1)
	releaseBoth := make(chan struct{})
	var cleanupReadyOnce sync.Once
	var clearReadyOnce sync.Once
	var cleanupTransactions sync.Map
	var clearTransactions sync.Map
	if err := harness.db.Callback().Query().After("gorm:query").Register(
		"overlay_postgres_cleanup_expired_recent_lock_barrier",
		func(tx *gorm.DB) {
			if tx.Statement.Context.Value(operationMarker{}) != cleanupOperation ||
				tx.Statement.Table != "backup_asset_recent_access" {
				return
			}
			cleanupTransactions.Store(tx.Statement.ConnPool, struct{}{})
			cleanupReadyOnce.Do(func() {
				cleanupRecentReady <- struct{}{}
				select {
				case <-releaseBoth:
				case <-tx.Statement.Context.Done():
					_ = tx.AddError(tx.Statement.Context.Err())
				}
			})
		},
	); err != nil {
		t.Fatalf("register CleanupExpiredRecent barrier: %v", err)
	}
	if err := harness.db.Callback().Delete().Before("gorm:delete").Register(
		"overlay_postgres_cleanup_clear_recent_delete_barrier",
		func(tx *gorm.DB) {
			if tx.Statement.Context.Value(operationMarker{}) != clearOperation ||
				tx.Statement.Table != "backup_asset_recent_access" {
				return
			}
			clearTransactions.Store(tx.Statement.ConnPool, struct{}{})
			clearReadyOnce.Do(func() {
				clearDeleteReady <- struct{}{}
				select {
				case <-releaseBoth:
				case <-tx.Statement.Context.Done():
					_ = tx.AddError(tx.Statement.Context.Err())
				}
			})
		},
	); err != nil {
		_ = harness.db.Callback().Query().Remove("overlay_postgres_cleanup_expired_recent_lock_barrier")
		t.Fatalf("register ClearRecent delete barrier: %v", err)
	}
	t.Cleanup(func() {
		_ = harness.db.Callback().Delete().Remove("overlay_postgres_cleanup_clear_recent_delete_barrier")
		_ = harness.db.Callback().Query().Remove("overlay_postgres_cleanup_expired_recent_lock_barrier")
	})

	type cleanupCall struct {
		deleted int64
		err     error
	}
	type clearCall struct {
		cleared int64
		err     error
	}
	cleanupDone := make(chan cleanupCall, 1)
	clearDone := make(chan clearCall, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		deleted, err := service.CleanupExpiredRecent(
			context.WithValue(ctx, operationMarker{}, cleanupOperation),
			1,
		)
		cleanupDone <- cleanupCall{deleted: deleted, err: err}
	}()
	select {
	case <-cleanupRecentReady:
	case <-ctx.Done():
		close(releaseBoth)
		t.Fatalf("CleanupExpiredRecent did not reach the recent discovery barrier: %v", ctx.Err())
	}
	go func() {
		cleared, err := service.ClearRecent(
			context.WithValue(ctx, operationMarker{}, clearOperation),
			actor.UserID,
			"cleanup-expired-clear-lock-order",
		)
		clearDone <- clearCall{cleared: cleared, err: err}
	}()
	select {
	case <-clearDeleteReady:
	case <-ctx.Done():
		close(releaseBoth)
		t.Fatalf("ClearRecent did not reach the post-usage delete barrier: %v", ctx.Err())
	}
	close(releaseBoth)

	var cleanupOutcome cleanupCall
	select {
	case cleanupOutcome = <-cleanupDone:
	case <-ctx.Done():
		t.Fatalf("CleanupExpiredRecent did not finish after the barriers opened: %v", ctx.Err())
	}
	var clearOutcome clearCall
	select {
	case clearOutcome = <-clearDone:
	case <-ctx.Done():
		t.Fatalf("ClearRecent did not finish after the barriers opened: %v", ctx.Err())
	}
	countTransactions := func(transactions *sync.Map) int {
		count := 0
		transactions.Range(func(_, _ any) bool {
			count++
			return true
		})
		return count
	}
	cleanupAttempts := countTransactions(&cleanupTransactions)
	clearAttempts := countTransactions(&clearTransactions)
	deadlocks := 0
	for _, err := range []error{cleanupOutcome.err, clearOutcome.err} {
		if err != nil && strings.Contains(strings.ToUpper(err.Error()), "SQLSTATE 40P01") {
			deadlocks++
		}
	}
	if cleanupRetries, clearRetries := max(cleanupAttempts-1, 0), max(clearAttempts-1, 0); deadlocks != 0 || cleanupRetries != 0 || clearRetries != 0 {
		t.Fatalf("deadlocks=%d cleanup_retries=%d clear_retries=%d cleanup_err=%v clear_err=%v",
			deadlocks, cleanupRetries, clearRetries, cleanupOutcome.err, clearOutcome.err)
	}
	if cleanupOutcome.err != nil || clearOutcome.err != nil {
		t.Fatalf("CleanupExpiredRecent deleted=%d err=%v ClearRecent cleared=%d err=%v",
			cleanupOutcome.deleted, cleanupOutcome.err, clearOutcome.cleared, clearOutcome.err)
	}
	if cleanupOutcome.deleted != 0 || clearOutcome.cleared != 1 {
		t.Fatalf("post-lock requery cleanup_deleted=%d ClearRecent_cleared=%d, want exact zero/one proof",
			cleanupOutcome.deleted, clearOutcome.cleared)
	}
	var recentCount int64
	if err := harness.db.Model(&model.BackupAssetRecentAccess{}).
		Where("owner_user_id = ? AND recovery_point_id = ?", actor.UserID, pointID).
		Count(&recentCount).Error; err != nil {
		t.Fatalf("count final recent rows: %v", err)
	}
	var usage model.BackupAssetOverlayUsage
	if err := harness.db.Where("owner_user_id = ?", actor.UserID).Take(&usage).Error; err != nil {
		t.Fatalf("load final overlay usage: %v", err)
	}
	if recentCount != 0 || usage.RecentCount != 0 || usage.RecentCount != recentCount {
		t.Fatalf("final expired recent/usage inconsistency: recent_rows=%d usage_recent_count=%d", recentCount, usage.RecentCount)
	}
}

func TestLifecycleOverlayClosedReadableMatrix(t *testing.T) {
	_, harness := newOverlayTestHarness(t)
	pointID := strings.Repeat("4", 32)
	semantics := []backupasset.PointVersionSemantics{
		backupasset.PointMutableHead,
		backupasset.PointNativeSnapshot,
		backupasset.PointXirangManifest,
		backupasset.PointImportedBaseline,
		"unknown_semantics",
	}
	states := []backupasset.RecoveryPointState{
		backupasset.RecoveryPointObserved,
		backupasset.RecoveryPointRetired,
		backupasset.RecoveryPointPreparing,
		backupasset.RecoveryPointVerifying,
		backupasset.RecoveryPointCommitted,
		backupasset.RecoveryPointDegraded,
		backupasset.RecoveryPointExpiring,
		backupasset.RecoveryPointExpired,
		backupasset.RecoveryPointFailed,
		backupasset.RecoveryPointPurgeBlocked,
		"unknown_state",
	}
	for _, pointSemantics := range semantics {
		for _, pointState := range states {
			name := string(pointSemantics) + "/" + string(pointState)
			t.Run(name, func(t *testing.T) {
				if err := harness.db.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).
					Updates(map[string]any{"semantics": pointSemantics, "state": pointState}).Error; err != nil {
					t.Fatalf("seed point product: %v", err)
				}
				err := validatePointLifecycleAdmissionTx(context.Background(), harness.db, []string{pointID})
				readable := pointSemantics == backupasset.PointMutableHead && pointState == backupasset.RecoveryPointObserved ||
					(pointSemantics == backupasset.PointNativeSnapshot ||
						pointSemantics == backupasset.PointXirangManifest ||
						pointSemantics == backupasset.PointImportedBaseline) &&
						(pointState == backupasset.RecoveryPointCommitted || pointState == backupasset.RecoveryPointDegraded)
				if readable && err != nil {
					t.Fatalf("closed readable product rejected: %v", err)
				}
				if !readable && !errors.Is(err, backupasset.ErrConflict) {
					t.Fatalf("closed unreadable product error=%v, want ErrConflict", err)
				}
			})
		}
	}
}

func TestLifecycleBreaksExactTombstonesOpaqueOverlaysAndDeletesRecent(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	audit := &overlayAuditSpy{}
	actor := Actor{UserID: 701, Role: "operator"}
	pointID := strings.Repeat("c", 32)
	ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat("c", 64)}
	harness.points[pointID] = true
	harness.assets[ref] = true
	saved, err := service.CreateSavedSearch(context.Background(), actor, CreateSavedSearchRequest{
		Query: savedQuery(pointID, "lifecycle"), IdempotencyKey: "lifecycle-saved-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	favorite, _ := service.AddFavorite(context.Background(), actor, AddFavoriteRequest{Ref: ref, Label: "keep me", IdempotencyKey: "lifecycle-fav-0001"})
	tag, _ := service.CreateTag(context.Background(), actor.UserID, "keep-tag", "lifecycle-tag-0001")
	assignment, _ := service.AssignTag(context.Background(), actor, tag.ID, ref, "lifecycle-assign01")
	recent, _ := service.RecordRecent(context.Background(), actor, ref)
	service.audit = audit

	result, err := service.ReconcileSource(context.Background(), SourceLifecycle{
		RecoveryPointID: pointID, Reason: SourceExpired,
	}, 100)
	if err != nil {
		t.Fatalf("ReconcileSource: %v", err)
	}
	if result.SavedSearches != 1 || result.Favorites != 1 || result.TagAssignments != 1 || result.RecentsDeleted != 1 {
		t.Fatalf("lifecycle result=%+v", result)
	}
	loaded, _ := service.GetSavedSearch(context.Background(), actor.UserID, saved.ID)
	if loaded.State != SavedSearchBroken || loaded.StateReason != SavedReasonPointExpired || loaded.Query.Scope.Mode != assetsearch.SearchScopeExactPoints {
		t.Fatalf("saved exact scope widened or not broken: saved_id=%s state=%s state_reason=%s version=%d scope_mode=%s broken_at_present=%t created_at_present=%t updated_at_present=%t",
			loaded.ID, loaded.State, loaded.StateReason, loaded.Version, loaded.Query.Scope.Mode,
			loaded.BrokenAt != nil, !loaded.CreatedAt.IsZero(), !loaded.UpdatedAt.IsZero())
	}
	if loaded.Version != saved.Version+1 {
		t.Fatalf("saved lifecycle transition did not advance version: before=%d after=%d", saved.Version, loaded.Version)
	}
	var favoriteRow model.BackupAssetFavorite
	if err := harness.db.Where("id = ?", favorite.ID).Take(&favoriteRow).Error; err != nil {
		t.Fatal(err)
	}
	if favoriteRow.State != string(OverlayTombstone) || favoriteRow.TombstoneReason != string(TombstoneSourceExpired) {
		t.Fatalf("favorite tombstone mismatch: favorite_id=%s state=%s tombstone_reason=%s version=%d created_at_present=%t updated_at_present=%t",
			favoriteRow.ID, favoriteRow.State, favoriteRow.TombstoneReason, favoriteRow.Version,
			!favoriteRow.CreatedAt.IsZero(), !favoriteRow.UpdatedAt.IsZero())
	}
	if favoriteRow.Version != favorite.Version+1 {
		t.Fatalf("favorite lifecycle transition did not advance version: before=%d after=%d", favorite.Version, favoriteRow.Version)
	}
	var assignmentRow model.BackupAssetTagAssignment
	if err := harness.db.Where("id = ?", assignment.ID).Take(&assignmentRow).Error; err != nil {
		t.Fatal(err)
	}
	if assignmentRow.State != string(OverlayTombstone) || assignmentRow.TombstoneReason != string(TombstoneSourceExpired) {
		t.Fatalf("assignment tombstone mismatch: assignment_id=%s state=%s tombstone_reason=%s version=%d created_at_present=%t updated_at_present=%t",
			assignmentRow.ID, assignmentRow.State, assignmentRow.TombstoneReason, assignmentRow.Version,
			!assignmentRow.CreatedAt.IsZero(), !assignmentRow.UpdatedAt.IsZero())
	}
	if assignmentRow.Version != assignment.Version+1 {
		t.Fatalf("tag-assignment lifecycle transition did not advance version: before=%d after=%d", assignment.Version, assignmentRow.Version)
	}
	var recentCount int64
	if err := harness.db.Model(&model.BackupAssetRecentAccess{}).Where("id = ?", recent.ID).Count(&recentCount).Error; err != nil || recentCount != 0 {
		t.Fatalf("recent survived source expiry count=%d err=%v", recentCount, err)
	}

	second, err := service.ReconcileSource(context.Background(), SourceLifecycle{RecoveryPointID: pointID, Reason: SourceExpired}, 100)
	if err != nil || second != (LifecycleResult{}) {
		t.Fatalf("lifecycle is not idempotent: result=%+v err=%v", second, err)
	}
	wantActions := []backupasset.AuditAction{
		backupasset.AuditActionSavedSearchBroken,
		backupasset.AuditActionFavoriteTombstone,
		backupasset.AuditActionTagAssignmentTombstone,
		backupasset.AuditActionOverlayCleanup,
	}
	if len(audit.inputs) != len(wantActions) {
		t.Fatalf("lifecycle audit count=%d inputs=%+v", len(audit.inputs), audit.inputs)
	}
	for index, action := range wantActions {
		if audit.inputs[index].Action != action || audit.inputs[index].RecoveryPointID != pointID || audit.inputs[index].Outcome != backupasset.AuditOutcomeSuccess {
			t.Fatalf("lifecycle audit[%d]=%+v", index, audit.inputs[index])
		}
	}
}

func TestLifecycleMissingSourceNeverReactivatesTombstone(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	actor := Actor{UserID: 702, Role: "operator"}
	ref := backupasset.AssetRef{RecoveryPointID: strings.Repeat("d", 32), EntryID: strings.Repeat("d", 64)}
	harness.assets[ref] = true
	favorite, _ := service.AddFavorite(context.Background(), actor, AddFavoriteRequest{Ref: ref, IdempotencyKey: "missing-source-fav1"})
	_, _ = service.ReconcileSource(context.Background(), SourceLifecycle{RecoveryPointID: ref.RecoveryPointID, Reason: SourceMissing}, 10)
	harness.assets[ref] = true
	var row model.BackupAssetFavorite
	if err := harness.db.Where("id = ?", favorite.ID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != string(OverlayTombstone) {
		t.Fatalf("source return silently reactivated favorite: favorite_id=%s state=%s tombstone_reason=%s version=%d created_at_present=%t updated_at_present=%t",
			row.ID, row.State, row.TombstoneReason, row.Version, !row.CreatedAt.IsZero(), !row.UpdatedAt.IsZero())
	}
}

func TestLifecycleReconcileHonorsPerOverlayBatchLimit(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	actor := Actor{UserID: 703, Role: "operator"}
	pointID := strings.Repeat("e", 32)
	harness.points[pointID] = true
	tag, err := service.CreateTag(context.Background(), actor.UserID, "batch-tag", "batch-limit-tag-01")
	if err != nil {
		t.Fatal(err)
	}
	for index, entryCharacter := range []string{"1", "2"} {
		ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat(entryCharacter, 64)}
		harness.assets[ref] = true
		if _, err := service.AddFavorite(context.Background(), actor, AddFavoriteRequest{
			Ref: ref, IdempotencyKey: "batch-limit-favorite-0" + entryCharacter,
		}); err != nil {
			t.Fatalf("add favorite %d: %v", index, err)
		}
		if _, err := service.AssignTag(context.Background(), actor, tag.ID, ref, "batch-limit-assign-0"+entryCharacter); err != nil {
			t.Fatalf("assign tag %d: %v", index, err)
		}
	}

	first, err := service.ReconcileSource(context.Background(), SourceLifecycle{RecoveryPointID: pointID, Reason: SourceExpired}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Favorites != 1 || first.TagAssignments != 1 {
		t.Fatalf("first lifecycle batch exceeded limit: %+v", first)
	}
	second, err := service.ReconcileSource(context.Background(), SourceLifecycle{RecoveryPointID: pointID, Reason: SourceExpired}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if second.Favorites != 1 || second.TagAssignments != 1 {
		t.Fatalf("second lifecycle batch did not process remainder: %+v", second)
	}
}

func TestLifecycleOverlayRejectsLostConditionalBatchBeforeFalseZero(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	actor := Actor{UserID: 706, Role: "operator"}
	pointID := strings.Repeat("9", 32)
	harness.points[pointID] = true

	created := make([]SavedSearch, 0, 2)
	for index := range 2 {
		saved, err := service.CreateSavedSearch(context.Background(), actor, CreateSavedSearchRequest{
			Query:          savedQuery(pointID, "lost conditional batch"),
			IdempotencyKey: "lifecycle-lost-batch-0" + strconv.Itoa(index),
		})
		if err != nil {
			t.Fatalf("seed saved search %d: %v", index, err)
		}
		created = append(created, saved)
	}
	firstID := created[0].ID
	if created[1].ID < firstID {
		firstID = created[1].ID
	}

	now := harness.clock.Now()
	attemptID := strings.Repeat("8", 32)
	if err := harness.db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: attemptID, RecoveryPointID: pointID, Operation: string(backupasset.LifecycleRetentionExpire),
		Phase: string(backupasset.LifecyclePhaseCleaning), TransitionRevision: 1,
		ClaimedAt: &now, HeartbeatAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed lifecycle attempt: %v", err)
	}

	injected := false
	const callbackName = "overlay_lifecycle_lost_saved_batch"
	if err := harness.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if injected || tx.Statement == nil || tx.Statement.Table != (model.BackupAssetSavedSearch{}).TableName() {
			return
		}
		injected = true
		if err := tx.Exec(`UPDATE backup_asset_saved_searches
			SET state = ?, state_reason = ?, broken_at = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND state = ?`,
			SavedSearchBroken, SavedReasonPointExpired, now, now, firstID, SavedSearchActive).Error; err != nil {
			_ = tx.AddError(err)
		}
	}); err != nil {
		t.Fatalf("register lost-batch callback: %v", err)
	}
	t.Cleanup(func() { _ = harness.db.Callback().Update().Remove(callbackName) })

	result, err := service.ReconcileSourceLifecycle(context.Background(), backupasset.SourceLifecycleRequest{
		RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
		Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecycleCleanup,
	}, SourceLifecycle{RecoveryPointID: pointID, Reason: SourceExpired}, 1)
	if !errors.Is(err, backupasset.ErrConflict) || result != (LifecycleResult{}) {
		t.Fatalf("lost conditional batch result saved=%d favorite=%d tag=%d recent=%d err=%v, want closed ErrConflict",
			result.SavedSearches, result.Favorites, result.TagAssignments, result.RecentsDeleted, err)
	}
	if !injected {
		t.Fatal("lost-batch callback did not reach the selected saved-search update")
	}
	var active int64
	if err := harness.db.Model(&model.BackupAssetSavedSearch{}).
		Where("state = ?", SavedSearchActive).Count(&active).Error; err != nil {
		t.Fatalf("count remaining active saved searches: %v", err)
	}
	if active != 2 {
		t.Fatalf("remaining active saved searches=%d, want rollback to preserve both batches", active)
	}
}

func TestLifecycleReconcileInvalidSourcesDiscoversTerminalAndMissingPoints(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	actor := Actor{UserID: 704, Role: "operator"}
	expiredPointID := strings.Repeat("7", 32)
	missingPointID := strings.Repeat("8", 32)
	for _, pointID := range []string{expiredPointID, missingPointID} {
		ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat(pointID[:1], 64)}
		harness.assets[ref] = true
		if _, err := service.AddFavorite(context.Background(), actor, AddFavoriteRequest{
			Ref: ref, IdempotencyKey: "invalid-source-fav-" + pointID[:1],
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := harness.db.Model(&model.RecoveryPoint{}).Where("id = ?", expiredPointID).
		Updates(map[string]any{"state": backupasset.RecoveryPointExpired, "physical_availability": backupasset.PhysicalUnknown}).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Where("id = ?", missingPointID).Delete(&model.RecoveryPoint{}).Error; err != nil {
		t.Fatal(err)
	}

	count, err := service.ReconcileInvalidSources(context.Background(), 10)
	if err != nil || count != 2 {
		t.Fatalf("ReconcileInvalidSources count=%d err=%v", count, err)
	}
	var active int64
	if err := harness.db.Model(&model.BackupAssetFavorite{}).Where("state = ?", OverlayActive).Count(&active).Error; err != nil || active != 0 {
		t.Fatalf("invalid-source favorites remained active=%d err=%v", active, err)
	}
	if count, err := service.ReconcileInvalidSources(context.Background(), 10); err != nil || count != 0 {
		t.Fatalf("repeated invalid-source reconcile count=%d err=%v", count, err)
	}
}

func TestLifecycleCleanupExpiredRecentReleasesQuotaAndIsIdempotent(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	actor := Actor{UserID: 705, Role: "operator"}
	oldRef := backupasset.AssetRef{RecoveryPointID: strings.Repeat("1", 32), EntryID: strings.Repeat("1", 64)}
	newRef := backupasset.AssetRef{RecoveryPointID: strings.Repeat("2", 32), EntryID: strings.Repeat("2", 64)}
	harness.assets[oldRef] = true
	harness.assets[newRef] = true
	if _, err := service.RecordRecent(context.Background(), actor, oldRef); err != nil {
		t.Fatal(err)
	}
	harness.clock.Advance(29 * 24 * time.Hour)
	if _, err := service.RecordRecent(context.Background(), actor, newRef); err != nil {
		t.Fatal(err)
	}
	harness.clock.Advance(2 * 24 * time.Hour)
	count, err := service.CleanupExpiredRecent(context.Background(), 1)
	if err != nil || count != 1 {
		t.Fatalf("CleanupExpiredRecent count=%d err=%v", count, err)
	}
	var rows []model.BackupAssetRecentAccess
	if err := harness.db.Where("owner_user_id = ?", actor.UserID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RecoveryPointID != newRef.RecoveryPointID {
		t.Fatalf("expired cleanup rows mismatch: row_count=%d first_row_present=%t", len(rows), len(rows) > 0)
	}
	var usage model.BackupAssetOverlayUsage
	if err := harness.db.Where("owner_user_id = ?", actor.UserID).Take(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if usage.RecentCount != 1 {
		t.Fatalf("expired cleanup usage mismatch: recent_count=%d version=%d updated_at_present=%t",
			usage.RecentCount, usage.Version, !usage.UpdatedAt.IsZero())
	}
	if count, err := service.CleanupExpiredRecent(context.Background(), 1); err != nil || count != 0 {
		t.Fatalf("repeated expired cleanup count=%d err=%v", count, err)
	}
}

type overlayAuditSpy struct {
	inputs []backupasset.AuditEventInput
}

func (spy *overlayAuditSpy) Write(_ context.Context, input backupasset.AuditEventInput) error {
	spy.inputs = append(spy.inputs, input)
	return nil
}
