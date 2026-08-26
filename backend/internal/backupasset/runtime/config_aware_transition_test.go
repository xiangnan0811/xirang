package runtime

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	stdruntime "runtime"
	"sort"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset"
)

type configAwareContentManagerFake struct {
	enableConfigs  []backupasset.ContentConfig
	restoreConfigs []backupasset.ContentConfig
}

type alternatingFoundationSettings struct {
	snapshots []map[string]string
	calls     int
}

func (source *alternatingFoundationSettings) GetEffective(key string) string {
	if source == nil || len(source.snapshots) == 0 {
		return ""
	}
	return source.snapshots[0][key]
}

func (source *alternatingFoundationSettings) BackupAssetSettingsSnapshot() (map[string]string, error) {
	if source == nil || len(source.snapshots) == 0 {
		return nil, errors.New("foundation snapshot unavailable")
	}
	index := source.calls
	if index >= len(source.snapshots) {
		index = len(source.snapshots) - 1
	}
	source.calls++
	result := make(map[string]string, len(source.snapshots[index]))
	for key, value := range source.snapshots[index] {
		result[key] = value
	}
	return result, nil
}

func (*configAwareContentManagerFake) Startup(context.Context) error { return nil }
func (fake *configAwareContentManagerFake) PrepareEnable(_ context.Context, config backupasset.ContentConfig) error {
	fake.enableConfigs = append(fake.enableConfigs, config)
	return nil
}
func (fake *configAwareContentManagerFake) RestoreEnable(_ context.Context, config backupasset.ContentConfig) error {
	fake.restoreConfigs = append(fake.restoreConfigs, config)
	return nil
}
func (*configAwareContentManagerFake) PrepareDisable(context.Context) error { return nil }
func (*configAwareContentManagerFake) SetReady(bool)                        {}
func (*configAwareContentManagerFake) StopAccepting()                       {}
func (*configAwareContentManagerFake) Run(context.Context)                  {}
func (*configAwareContentManagerFake) Shutdown(context.Context) error       { return nil }
func (*configAwareContentManagerFake) PrepareSchemaDown(_ context.Context, down func() error) error {
	return down()
}

func TestRuntimeConfigAwareContentEnableReceivesProspectiveConfig(t *testing.T) {
	configs := testFoundationTransitionConfigs(t, false, true)
	configs.Prospective.Content.PreviewTTL *= 3
	manager := &configAwareContentManagerFake{}
	events := []string{}
	runtime := &Runtime{
		contentManager: manager,
		transitioner:   &runtimeFeatureTransitionerFake{events: &events},
		enablement:     readyGAEnablement(),
	}

	if err := runtime.transitionFeatureWithConfigs(context.Background(), configs, func() error { return nil }); err != nil {
		t.Fatalf("transitionFeatureWithConfigs: %v", err)
	}
	if len(manager.enableConfigs) != 1 || !reflect.DeepEqual(manager.enableConfigs[0], configs.Prospective.Content) {
		t.Fatalf("Content enable configs=%+v, want prospective %+v", manager.enableConfigs, configs.Prospective.Content)
	}
}

func TestRuntimeConfigAwareContentRestoreReceivesPriorConfig(t *testing.T) {
	configs := testFoundationTransitionConfigs(t, true, false)
	configs.Prior.Content.PreviewTTL *= 3
	persistErr := errors.New("FAKE_CONFIG_AWARE_DISABLE_PERSIST_FAILURE_FOR_TEST_ONLY")
	manager := &configAwareContentManagerFake{}
	events := []string{}
	runtime := &Runtime{
		contentManager: manager,
		transitioner:   &runtimeFeatureTransitionerFake{events: &events},
	}

	err := runtime.transitionFeatureWithConfigs(context.Background(), configs, func() error { return persistErr })
	if !errors.Is(err, persistErr) {
		t.Fatalf("transition error=%v, want persistence failure", err)
	}
	if len(manager.restoreConfigs) != 1 || !reflect.DeepEqual(manager.restoreConfigs[0], configs.Prior.Content) {
		t.Fatalf("Content restore configs=%+v, want prior %+v", manager.restoreConfigs, configs.Prior.Content)
	}
}

func TestRuntimeCompatibilityTransitionBuildsOneCompleteAtomicConfigBundle(t *testing.T) {
	first := runtimeFoundationSettings(false)
	second := runtimeFoundationSettings(false)
	second["backup_assets.search_batch_size"] = "73"
	source := &alternatingFoundationSettings{snapshots: []map[string]string{first, second}}
	runtime := &Runtime{foundation: backupasset.NewFoundationService(source)}

	configs, err := runtime.currentFeatureTransitionConfigs(true)
	if err != nil {
		t.Fatalf("currentFeatureTransitionConfigs: %v", err)
	}
	if source.calls != 1 {
		t.Fatalf("compatibility transition read %d Foundation snapshots, want exactly one", source.calls)
	}
	want, err := backupasset.FoundationTransitionConfigFromValues(first)
	if err != nil {
		t.Fatalf("parse expected complete Foundation config: %v", err)
	}
	if !reflect.DeepEqual(configs.Prior, want) {
		t.Fatalf("prior config=%+v, want one complete atomic bundle %+v", configs.Prior, want)
	}
	if !configs.Prospective.Enabled || !configs.Prospective.Content.Enabled ||
		!configs.Prospective.Search.Enabled || !configs.Prospective.Overlay.Enabled {
		t.Fatalf("prospective bundle did not apply enabled=true to every typed config: %+v", configs.Prospective)
	}
	if !reflect.DeepEqual(configs.Prospective.Export, want.Export) ||
		!reflect.DeepEqual(configs.Prospective.Recovery, want.Recovery) {
		t.Fatalf("global enable changed independent Export/Recovery subfeature config: %+v", configs.Prospective)
	}
}

func TestProspectiveFeatureLiveRequiresEnabledReadinessAndManagedAdmission(t *testing.T) {
	managed := newAdmissionControllerFixture(t, true, nil)
	managed.initialize(t)
	runtime := &Runtime{admission: managed.controller}
	if !runtime.prospectiveFeatureLive(true, true) {
		t.Fatal("prospective enabled plus readiness and managed admission was not live")
	}
	if runtime.prospectiveFeatureLive(false, true) {
		t.Fatal("prospective disabled state was treated as live")
	}
	if runtime.prospectiveFeatureLive(true, false) {
		t.Fatal("missing readiness authorization was treated as live")
	}

	disabled := newAdmissionControllerFixture(t, false, nil)
	disabled.initialize(t)
	runtime.admission = disabled.controller
	if runtime.prospectiveFeatureLive(true, true) {
		t.Fatal("disabled admission state was treated as live")
	}
	runtime.admission = nil
	if runtime.prospectiveFeatureLive(true, true) {
		t.Fatal("missing admission authority was treated as live")
	}
	events := []string{}
	runtime.transitioner = &runtimeFeatureTransitionerFake{events: &events, enabled: true}
	if runtime.prospectiveFeatureLive(true, true) {
		t.Fatal("generic transitioner CurrentMode fallback was treated as admission authority")
	}
}

func TestRuntimeSearchWorkerConfigMapsProspectiveSearchAndOverlayBundle(t *testing.T) {
	configs := testFoundationTransitionConfigs(t, false, true)
	got := runtimeSearchWorkerConfig(configs.Prospective, true)
	want := SearchWorkerConfig{
		Enabled:            true,
		ReconcileInterval:  configs.Prospective.Search.ReconcileInterval,
		ReconcileBatchSize: configs.Prospective.Search.BatchSize,
		WorkerConcurrency:  configs.Prospective.Search.MaxConcurrency,
		AbandonedAfter:     configs.Prospective.Search.BuildTimeout,
	}
	if got != want {
		t.Fatalf("Search worker config=%+v, want %+v", got, want)
	}
	configs.Prospective.Overlay.Enabled = false
	if got := runtimeSearchWorkerConfig(configs.Prospective, true); got.Enabled {
		t.Fatalf("disabled prospective Overlay produced enabled Search worker config: %+v", got)
	}
}

func TestConfigAwareTransitionGuardFollowsDelegatedHelpers(t *testing.T) {
	source := `package runtime

type Foundation struct{}
func (*Foundation) ContentConfig() {}

type managedContentRuntime struct { foundation *Foundation }
func (runtime *managedContentRuntime) PrepareEnable() { runtime.prepareEnableWithConfig() }
func (runtime *managedContentRuntime) prepareEnableWithConfig() { runtime.readCurrent() }
func (runtime *managedContentRuntime) readCurrent() { runtime.foundation.ContentConfig() }
`
	violations, err := configAwareForbiddenSelectors(
		map[string]string{"runtime.go": source},
		[]string{"managedContentRuntime.PrepareEnable"},
	)
	if err != nil {
		t.Fatalf("inspect delegated config-aware source: %v", err)
	}
	if !reflect.DeepEqual(violations, []string{"managedContentRuntime.readCurrent:ContentConfig"}) {
		t.Fatalf("delegated guard violations=%v, want nested Foundation getter", violations)
	}
}

type configAwareFunctionKey struct {
	receiver string
	name     string
}

type configAwareFunction struct {
	declaration  *ast.FuncDecl
	receiverName string
}

func configAwareForbiddenSelectors(sources map[string]string, rootNames []string) ([]string, error) {
	set := token.NewFileSet()
	filenames := make([]string, 0, len(sources))
	for filename := range sources {
		filenames = append(filenames, filename)
	}
	sort.Strings(filenames)

	functions := make(map[configAwareFunctionKey]configAwareFunction)
	for _, filename := range filenames {
		parsed, err := parser.ParseFile(set, filename, sources[filename], 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", filename, err)
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			receiverType, receiverName, err := configAwareReceiver(function)
			if err != nil {
				return nil, fmt.Errorf("index %s.%s: %w", receiverType, function.Name.Name, err)
			}
			key := configAwareFunctionKey{receiver: receiverType, name: function.Name.Name}
			if _, exists := functions[key]; exists {
				return nil, fmt.Errorf("duplicate config-aware function %s.%s", receiverType, function.Name.Name)
			}
			functions[key] = configAwareFunction{declaration: function, receiverName: receiverName}
		}
	}

	roots := make([]configAwareFunctionKey, 0, len(rootNames))
	for _, rootName := range rootNames {
		receiver, name, ok := strings.Cut(rootName, ".")
		if !ok || receiver == "" || name == "" {
			return nil, fmt.Errorf("invalid config-aware root %q", rootName)
		}
		key := configAwareFunctionKey{receiver: receiver, name: name}
		if _, exists := functions[key]; !exists {
			return nil, fmt.Errorf("config-aware root %s is missing", rootName)
		}
		roots = append(roots, key)
	}

	forbidden := map[string]bool{
		"FeatureEnabled": true, "FeatureLive": true,
		"ContentConfig": true, "SearchConfig": true, "SearchOverlayConfig": true,
		"BackupAssetSettingsSnapshot": true, "foundationValuesSnapshot": true,
		"atomicFoundationValues": true, "effectiveFoundationValues": true, "config": true,
	}
	visited := make(map[configAwareFunctionKey]bool)
	violations := make(map[string]bool)
	var inspectFunction func(configAwareFunctionKey)
	inspectFunction = func(key configAwareFunctionKey) {
		if visited[key] {
			return
		}
		visited[key] = true
		function := functions[key]
		delegates := make(map[configAwareFunctionKey]bool)
		ast.Inspect(function.declaration.Body, func(node ast.Node) bool {
			switch expression := node.(type) {
			case *ast.SelectorExpr:
				if forbidden[expression.Sel.Name] {
					violations[key.receiver+"."+key.name+":"+expression.Sel.Name] = true
				}
				identifier, ok := expression.X.(*ast.Ident)
				if ok && identifier.Name == function.receiverName {
					delegate := configAwareFunctionKey{receiver: key.receiver, name: expression.Sel.Name}
					if _, exists := functions[delegate]; exists {
						delegates[delegate] = true
					}
				}
			case *ast.Ident:
				delegate := configAwareFunctionKey{name: expression.Name}
				if _, exists := functions[delegate]; exists {
					delegates[delegate] = true
				}
			}
			return true
		})
		for delegate := range delegates {
			inspectFunction(delegate)
		}
	}
	for _, root := range roots {
		inspectFunction(root)
	}

	result := make([]string, 0, len(violations))
	for violation := range violations {
		result = append(result, violation)
	}
	sort.Strings(result)
	return result, nil
}

func configAwareReceiver(function *ast.FuncDecl) (receiverType, receiverName string, err error) {
	if function.Recv == nil || len(function.Recv.List) == 0 {
		return "", "", nil
	}
	receiver := function.Recv.List[0]
	if len(receiver.Names) > 1 {
		return "", "", errors.New("method receiver has multiple names")
	}
	if len(receiver.Names) == 1 {
		receiverName = receiver.Names[0].Name
	}
	typeExpression := receiver.Type
	if pointer, ok := typeExpression.(*ast.StarExpr); ok {
		typeExpression = pointer.X
	}
	identifier, ok := typeExpression.(*ast.Ident)
	if !ok {
		return "", "", errors.New("method receiver type is not an identifier")
	}
	return identifier.Name, receiverName, nil
}

func TestConfigAwareTransitionFunctionsDoNotReadCurrentFoundationSettings(t *testing.T) {
	_, currentFile, _, ok := stdruntime.Caller(0)
	if !ok {
		t.Fatal("resolve config-aware transition test file")
	}
	files := []string{
		filepath.Join(filepath.Dir(currentFile), "runtime.go"),
		filepath.Join(filepath.Dir(currentFile), "search_worker.go"),
	}
	sources := make(map[string]string, len(files))
	for _, path := range files {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		sources[path] = string(contents)
	}
	violations, err := configAwareForbiddenSelectors(sources, []string{
		"Runtime.transitionFeatureWithConfigs",
		"Runtime.startSearchAfterEnableWithConfig",
		"Runtime.prospectiveFeatureLive",
		"Runtime.startupSearchWithConfig",
		"managedContentRuntime.PrepareEnable",
		"managedContentRuntime.RestoreEnable",
		"SearchWorker.PrepareWithConfig",
		"SearchWorker.StartupPassWithConfig",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, violation := range violations {
		t.Errorf("config-aware call graph reaches forbidden current-setting selector %s", violation)
	}
}

func testFoundationTransitionConfigs(t *testing.T, priorEnabled, prospectiveEnabled bool) foundationTransitionConfigs {
	t.Helper()
	configs, err := foundationTransitionConfigsFromValues(
		runtimeFoundationSettings(priorEnabled), runtimeFoundationSettings(prospectiveEnabled),
	)
	if err != nil {
		t.Fatalf("foundationTransitionConfigsFromValues: %v", err)
	}
	return configs
}
