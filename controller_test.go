package main

import (
	"context"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/diff"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	ktesting "k8s.io/klog/v2/ktesting"
	"reflect"
	"science.sneaksanddata.com/nexus-configuration-controller/pkg/generated/clientset/versioned/fake"
	sharding "science.sneaksanddata.com/nexus-configuration-controller/pkg/shards"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
	nexuscontroller "science.sneaksanddata.com/nexus-configuration-controller/pkg/apis/science/v1"
	informers "science.sneaksanddata.com/nexus-configuration-controller/pkg/generated/informers/externalversions"
)

var (
	alwaysReady        = func() bool { return true }
	noResyncPeriodFunc = func() time.Duration { return 0 }
)

type FakeInformers struct {
	nexusInformers informers.SharedInformerFactory
	k8sInformers   kubeinformers.SharedInformerFactory
}

type FakeControllerInformers = FakeInformers

type FakeShardInformers = FakeInformers

type ApiFixture struct {
	mlaListResults       []*nexuscontroller.MachineLearningAlgorithm
	secretListResults    []*corev1.Secret
	configMapListResults []*corev1.ConfigMap

	existingCoreObjects []runtime.Object
	existingMlaObjects  []runtime.Object
}

type ControllerFixture = ApiFixture
type NexusFixture = ApiFixture

type fixture struct {
	t *testing.T

	controllerNexusClient *fake.Clientset
	controllerKubeClient  *k8sfake.Clientset

	shardNexusClient *fake.Clientset
	shardKubeClient  *k8sfake.Clientset
	// Objects to put in the store for controller cluster
	mlaLister       []*nexuscontroller.MachineLearningAlgorithm
	secretLister    []*corev1.Secret
	configMapLister []*corev1.ConfigMap

	// Objects to put in the store for shard cluster
	shardMlaLister    []*nexuscontroller.MachineLearningAlgorithm
	shardSecretLister []*corev1.Secret
	shardConfigLister []*corev1.ConfigMap

	// Actions expected to happen on the controller and shard clients respectively.
	controllerKubeActions  []core.Action
	controllerNexusActions []core.Action

	shardKubeActions  []core.Action
	shardNexusActions []core.Action
	// Objects from here preloaded into NewSimpleFake for controller and a shard.
	controllerKubeObjects []runtime.Object
	controllerObjects     []runtime.Object

	shardKubeObjects []runtime.Object
	shardObjects     []runtime.Object
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t

	f.mlaLister = []*nexuscontroller.MachineLearningAlgorithm{}
	f.secretLister = []*corev1.Secret{}
	f.configMapLister = []*corev1.ConfigMap{}
	f.controllerObjects = []runtime.Object{}
	f.controllerKubeObjects = []runtime.Object{}

	f.shardMlaLister = []*nexuscontroller.MachineLearningAlgorithm{}
	f.shardSecretLister = []*corev1.Secret{}
	f.shardConfigLister = []*corev1.ConfigMap{}
	f.shardObjects = []runtime.Object{}
	f.shardKubeObjects = []runtime.Object{}
	return f
}

// configure adds necessary mock return results for Kubernetes API calls for the respective listers
// and adds existing objects to the respective containers
func (f *fixture) configure(controllerFixture *ControllerFixture, nexusShardFixture *NexusFixture) *fixture {
	f.mlaLister = append(f.mlaLister, controllerFixture.mlaListResults...)
	f.secretLister = append(f.secretLister, controllerFixture.secretListResults...)
	f.configMapLister = append(f.configMapLister, controllerFixture.configMapListResults...)
	f.controllerObjects = append(f.controllerObjects, controllerFixture.existingMlaObjects...)
	f.controllerKubeObjects = append(f.controllerKubeObjects, controllerFixture.existingCoreObjects...)

	f.shardMlaLister = append(f.shardMlaLister, nexusShardFixture.mlaListResults...)
	f.shardSecretLister = append(f.shardSecretLister, nexusShardFixture.secretListResults...)
	f.shardConfigLister = append(f.shardConfigLister, nexusShardFixture.configMapListResults...)
	f.shardObjects = append(f.shardObjects, nexusShardFixture.existingMlaObjects...)
	f.shardKubeObjects = append(f.shardKubeObjects, nexusShardFixture.existingCoreObjects...)

	return f
}

func int32Ptr(i int32) *int32 { return &i }

func expectedMla(mla *nexuscontroller.MachineLearningAlgorithm, secret *corev1.Secret, configMap *corev1.ConfigMap) *nexuscontroller.MachineLearningAlgorithm {
	mlaCopy := mla.DeepCopy()
	mlaCopy.Status.State = "Ready"
	mlaCopy.Status.LastUpdatedTimestamp = metav1.Now()
	mlaCopy.Status.SyncedSecrets = []string{secret.Name}
	mlaCopy.Status.SyncedConfigurations = []string{configMap.Name}
	mlaCopy.Status.SyncedToClusters = []string{"shard0"}
	mlaCopy.Status.SyncErrors = map[string]string{}

	return mlaCopy
}

func expectedMlaWithErrors(mla *nexuscontroller.MachineLearningAlgorithm, secret *corev1.Secret, configMap *corev1.ConfigMap, syncErrors map[string]string) *nexuscontroller.MachineLearningAlgorithm {
	mlaCopy := mla.DeepCopy()
	mlaCopy.Status.State = "Failed"
	mlaCopy.Status.LastUpdatedTimestamp = metav1.Now()
	mlaCopy.Status.SyncedSecrets = []string{secret.Name}
	mlaCopy.Status.SyncedConfigurations = []string{configMap.Name}
	mlaCopy.Status.SyncedToClusters = []string{"shard0"}
	mlaCopy.Status.SyncErrors = syncErrors

	return mlaCopy
}

func expectedLabels() map[string]string {
	return map[string]string{
		"science.sneaksanddata.com/controller-app":      "nexus-configuration-controller",
		"science.sneaksanddata.com/configuration-owner": "test-controller-cluster",
	}
}

func expectedShardMla(mla *nexuscontroller.MachineLearningAlgorithm, uid string) *nexuscontroller.MachineLearningAlgorithm {
	mlaCopy := mla.DeepCopy()
	mlaCopy.UID = types.UID(uid)
	mlaCopy.Labels = expectedLabels()

	return mlaCopy
}

func expectedShardSecret(secret *corev1.Secret, mlas []*nexuscontroller.MachineLearningAlgorithm) *corev1.Secret {
	secretCopy := secret.DeepCopy()
	secretCopy.Labels = expectedLabels()
	secretCopy.OwnerReferences = make([]metav1.OwnerReference, 0)
	for _, mla := range mlas {
		secretCopy.OwnerReferences = append(secretCopy.OwnerReferences, *metav1.NewControllerRef(mla, nexuscontroller.SchemeGroupVersion.WithKind("MachineLearningAlgorithm")))
	}

	return secretCopy
}

func expectedShardConfigMap(configMap *corev1.ConfigMap, mlas []*nexuscontroller.MachineLearningAlgorithm) *corev1.ConfigMap {
	configMapCopy := configMap.DeepCopy()
	configMapCopy.Labels = expectedLabels()
	configMapCopy.OwnerReferences = make([]metav1.OwnerReference, 0)
	for _, mla := range mlas {
		configMapCopy.OwnerReferences = append(configMapCopy.OwnerReferences, *metav1.NewControllerRef(mla, nexuscontroller.SchemeGroupVersion.WithKind("MachineLearningAlgorithm")))
	}

	return configMapCopy
}

func newMla(name string, secret *corev1.Secret, configMap *corev1.ConfigMap, onShard bool) *nexuscontroller.MachineLearningAlgorithm {
	envFrom := make([]corev1.EnvFromSource, 2)
	cargs := make([]string, 1)
	var labels map[string]string
	if onShard {
		labels = expectedLabels()
	}
	cargs[0] = "job.py"
	if secret != nil {
		envFrom[0] = corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: secret.Name},
			},
		}
	}

	if configMap != nil {
		envFrom[1] = corev1.EnvFromSource{
			ConfigMapRef: &corev1.ConfigMapEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: configMap.Name},
			},
		}
	}

	return &nexuscontroller.MachineLearningAlgorithm{
		TypeMeta: metav1.TypeMeta{APIVersion: nexuscontroller.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: metav1.NamespaceDefault,
			Labels:    labels,
			UID:       types.UID(name),
		},
		Spec: nexuscontroller.MachineLearningAlgorithmSpec{
			ImageRegistry:        "test",
			ImageRepository:      "test",
			ImageTag:             "v1.0.0",
			DeadlineSeconds:      int32Ptr(120),
			MaximumRetries:       int32Ptr(3),
			Env:                  make([]corev1.EnvVar, 0),
			EnvFrom:              envFrom,
			CpuLimit:             "1000m",
			MemoryLimit:          "2000Mi",
			WorkgroupHost:        "test-cluster.io",
			Workgroup:            "default",
			AdditionalWorkgroups: make(map[string]string),
			MonitoringParameters: make([]string, 0),
			CustomResources:      make(map[string]string),
			SpeculativeAttempts:  int32Ptr(0),
			TransientExitCodes:   make([]int32, 0),
			FatalExitCodes:       make([]int32, 0),
			Command:              "python",
			Args:                 cargs,
			MountDatadogSocket:   true,
		},
	}
}

func newSecret(name string, owner *nexuscontroller.MachineLearningAlgorithm) *corev1.Secret {
	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: metav1.NamespaceDefault,
		},
		Data: make(map[string][]byte),
		Type: "",
	}

	if owner != nil {
		secret.SetOwnerReferences([]metav1.OwnerReference{
			*metav1.NewControllerRef(owner, nexuscontroller.SchemeGroupVersion.WithKind("MachineLearningAlgorithm")),
		})
	}
	return &secret
}

func newConfigMap(name string, owner *nexuscontroller.MachineLearningAlgorithm) *corev1.ConfigMap {
	configMap := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: metav1.NamespaceDefault,
		},
		Data: make(map[string]string),
	}

	if owner != nil {
		configMap.SetOwnerReferences([]metav1.OwnerReference{
			*metav1.NewControllerRef(owner, nexuscontroller.SchemeGroupVersion.WithKind("MachineLearningAlgorithm")),
		})
	}

	return &configMap
}

// checkAction verifies that expected and actual actions are equal and both have
// same attached resources
func checkAction(expected, actual core.Action, t *testing.T) {
	if !(expected.Matches(actual.GetVerb(), actual.GetResource().Resource) && actual.GetSubresource() == expected.GetSubresource()) {
		t.Errorf("Expected\n\t%#v\ngot\n\t%#v", expected, actual)
		return
	}

	if reflect.TypeOf(actual) != reflect.TypeOf(expected) {
		t.Errorf("Action has wrong type. Expected: %t. Got: %t", expected, actual)
		return
	}

	switch a := actual.(type) {
	case core.CreateActionImpl:
		e, _ := expected.(core.CreateActionImpl)
		expObject := e.GetObject()
		object := a.GetObject()

		if !reflect.DeepEqual(expObject, object) {
			t.Errorf("Action %s %s has wrong object\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintSideBySide(expObject, object))
		}
	case core.UpdateActionImpl:
		e, _ := expected.(core.UpdateActionImpl)
		expObject := e.GetObject()
		object := a.GetObject()
		switch expObject.(type) {
		case *nexuscontroller.MachineLearningAlgorithm:
			// avoid issues with time drift
			currentTime := metav1.Now()
			expCopy := expObject.DeepCopyObject().(*nexuscontroller.MachineLearningAlgorithm)
			expCopy.Status.LastUpdatedTimestamp = currentTime

			objCopy := object.DeepCopyObject().(*nexuscontroller.MachineLearningAlgorithm)
			objCopy.Status.LastUpdatedTimestamp = currentTime

			if !reflect.DeepEqual(expCopy, objCopy) {
				t.Errorf("Action %s %s has wrong object\nDiff:\n %s",
					a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintSideBySide(expCopy, objCopy))
			}
		default:
			if !reflect.DeepEqual(expObject, object) {
				t.Errorf("Action %s %s has wrong object\nDiff:\n %s",
					a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintSideBySide(expObject, object))
			}
		}
	case core.PatchActionImpl:
		e, _ := expected.(core.PatchActionImpl)
		expPatch := e.GetPatch()
		patch := a.GetPatch()
		if !reflect.DeepEqual(expPatch, patch) {
			t.Errorf("Action %s %s has wrong patch\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintSideBySide(expPatch, patch))
		}
	default:
		t.Errorf("Uncaptured Action %s %s, you should explicitly add a case to capture it",
			actual.GetVerb(), actual.GetResource().Resource)
	}
}

// filterInformerActions filters list and watch actions for testing resources.
// Since list and watch don't change resource state we can filter it to lower
// nose level in our tests.
func filterInformerActions(actions []core.Action) []core.Action {
	ret := []core.Action{}
	for _, action := range actions {
		if len(action.GetNamespace()) == 0 &&
			(action.Matches("list", "machinelearningalgorithms") ||
				action.Matches("watch", "machinelearningalgorithms") ||
				action.Matches("list", "configmaps") ||
				action.Matches("watch", "configmaps") ||
				action.Matches("list", "secrets") ||
				action.Matches("watch", "secrets")) {
			continue
		}
		ret = append(ret, action)
	}

	return ret
}

func (f *fixture) newController(ctx context.Context) (*Controller, *FakeControllerInformers, *FakeShardInformers) {
	f.controllerNexusClient = fake.NewSimpleClientset(f.controllerObjects...)
	f.controllerKubeClient = k8sfake.NewSimpleClientset(f.controllerKubeObjects...)

	f.shardNexusClient = fake.NewSimpleClientset(f.shardObjects...)
	f.shardKubeClient = k8sfake.NewSimpleClientset(f.shardKubeObjects...)

	controllerNexusInf := informers.NewSharedInformerFactory(f.controllerNexusClient, noResyncPeriodFunc())
	controllerKubeInf := kubeinformers.NewSharedInformerFactory(f.controllerKubeClient, noResyncPeriodFunc())

	shardNexusInf := informers.NewSharedInformerFactory(f.shardNexusClient, noResyncPeriodFunc())
	shardKubeInf := kubeinformers.NewSharedInformerFactory(f.shardKubeClient, noResyncPeriodFunc())

	shards := make([]*sharding.Shard, 0)
	newShard := sharding.NewShard(
		"test-controller-cluster",
		"shard0",
		f.shardKubeClient,
		f.shardNexusClient,
		shardNexusInf.Science().V1().MachineLearningAlgorithms(),
		shardKubeInf.Core().V1().Secrets(),
		shardKubeInf.Core().V1().ConfigMaps())

	newShard.MlaSynced = alwaysReady
	newShard.SecretsSynced = alwaysReady
	newShard.ConfigMapsSynced = alwaysReady

	shards = append(shards, newShard)

	c, _ := NewController(
		ctx,
		"test",
		f.controllerKubeClient,
		f.controllerNexusClient,
		shards,
		controllerKubeInf.Core().V1().Secrets(),
		controllerKubeInf.Core().V1().ConfigMaps(),
		controllerNexusInf.Science().V1().MachineLearningAlgorithms())

	c.mlaSynced = alwaysReady
	c.secretsSynced = alwaysReady
	c.configMapsSynced = alwaysReady
	c.recorder = &record.FakeRecorder{}

	for _, d := range f.mlaLister {
		_ = controllerNexusInf.Science().V1().MachineLearningAlgorithms().Informer().GetIndexer().Add(d)
	}

	for _, d := range f.shardMlaLister {
		_ = shardNexusInf.Science().V1().MachineLearningAlgorithms().Informer().GetIndexer().Add(d)
	}

	for _, d := range f.secretLister {
		_ = controllerKubeInf.Core().V1().Secrets().Informer().GetIndexer().Add(d)
	}

	for _, d := range f.shardSecretLister {
		_ = shardKubeInf.Core().V1().Secrets().Informer().GetIndexer().Add(d)
	}

	for _, d := range f.shardConfigLister {
		_ = shardKubeInf.Core().V1().ConfigMaps().Informer().GetIndexer().Add(d)
	}

	for _, d := range f.configMapLister {
		_ = controllerKubeInf.Core().V1().ConfigMaps().Informer().GetIndexer().Add(d)
	}

	return c, &FakeControllerInformers{
			nexusInformers: controllerNexusInf,
			k8sInformers:   controllerKubeInf,
		},
		&FakeShardInformers{
			nexusInformers: shardNexusInf,
			k8sInformers:   shardKubeInf,
		}
}

func (f *fixture) checkActions(expected []core.Action, actual []core.Action) {
	actions := filterInformerActions(actual)
	for i, action := range actions {
		if len(expected) < i+1 {
			f.t.Errorf("%d unexpected actions: %+v", len(actions)-len(expected), actions[i:])
			break
		}

		expectedAction := expected[i]
		checkAction(expectedAction, action, f.t)
	}
	if len(expected) > len(actions) {
		f.t.Errorf("%d additional expected actions:%+v", len(expected)-len(actions), expected[len(actions):])
	}
}

func (f *fixture) runController(ctx context.Context, mlaRefs []cache.ObjectName, startInformers bool, expectError bool) {
	controllerRef, controllerInformers, shardInformers := f.newController(ctx)
	if startInformers {
		controllerInformers.nexusInformers.Start(ctx.Done())
		controllerInformers.k8sInformers.Start(ctx.Done())

		shardInformers.nexusInformers.Start(ctx.Done())
		shardInformers.k8sInformers.Start(ctx.Done())
	}

	for _, mlaRef := range mlaRefs {
		err := controllerRef.syncHandler(ctx, mlaRef)
		if !expectError && err != nil {
			f.t.Errorf("error syncing mla: %v", err)
		} else if expectError && err == nil {
			f.t.Error("expected error syncing mla, got nil")
		}
	}

	f.checkActions(f.controllerNexusActions, f.controllerNexusClient.Actions())
	f.checkActions(f.shardNexusActions, f.shardNexusClient.Actions())
	f.checkActions(f.shardKubeActions, f.shardKubeClient.Actions())
	f.checkActions(f.controllerKubeActions, f.controllerKubeClient.Actions())
}

func (f *fixture) run(ctx context.Context, mlaRefs []cache.ObjectName, expectError bool) {
	f.runController(ctx, mlaRefs, true, expectError)
}

func getRef(mla *nexuscontroller.MachineLearningAlgorithm) cache.ObjectName {
	ref := cache.MetaObjectToName(mla)
	return ref
}

// expectControllerUpdateMlaStatusAction sets expectations for the resource actions in a controller cluster
// for MLA in the controller cluster we only expect a status update
func (f *fixture) expectControllerUpdateMlaStatusAction(mla *nexuscontroller.MachineLearningAlgorithm) {
	updateMlaStatusAction := core.NewUpdateSubresourceAction(schema.GroupVersionResource{Resource: "machinelearningalgorithms"}, "status", mla.Namespace, mla)
	f.controllerNexusActions = append(f.controllerNexusActions, updateMlaStatusAction)
}

// expectShardActions sets expectations for the resource actions in a shard cluster
// for resources in the shard cluster we expect the following: MLA is created, all referenced secrets and configmaps are created, with owner references assigned
func (f *fixture) expectShardActions(shardMla *nexuscontroller.MachineLearningAlgorithm, mlaSecret *corev1.Secret, mlaConfigMap *corev1.ConfigMap, mlaUpdated bool) {
	if !mlaUpdated {
		f.shardNexusActions = append(f.shardNexusActions, core.NewCreateAction(schema.GroupVersionResource{Resource: "machinelearningalgorithms"}, shardMla.Namespace, shardMla))
	} else {
		f.shardNexusActions = append(f.shardNexusActions, core.NewUpdateAction(schema.GroupVersionResource{Resource: "machinelearningalgorithms"}, shardMla.Namespace, shardMla))
	}

	if mlaSecret != nil {
		f.shardKubeActions = append(f.shardKubeActions, core.NewCreateAction(schema.GroupVersionResource{Resource: "secrets", Version: "v1"}, mlaSecret.Namespace, mlaSecret))
	}

	if mlaConfigMap != nil {
		f.shardKubeActions = append(f.shardKubeActions, core.NewCreateAction(schema.GroupVersionResource{Resource: "configmaps", Version: "v1"}, mlaConfigMap.Namespace, mlaConfigMap))
	}
}

func (f *fixture) expectOwnershipUpdateActions(mlaSecret *corev1.Secret, mlaConfigMap *corev1.ConfigMap) {
	if mlaSecret != nil {
		f.shardKubeActions = append(f.shardKubeActions, core.NewUpdateAction(schema.GroupVersionResource{Resource: "secrets", Version: "v1"}, mlaSecret.Namespace, mlaSecret))
	}

	if mlaConfigMap != nil {
		f.shardKubeActions = append(f.shardKubeActions, core.NewUpdateAction(schema.GroupVersionResource{Resource: "configmaps", Version: "v1"}, mlaConfigMap.Namespace, mlaConfigMap))
	}
}

// expectedUpdateActions sets expectations for the resource actions in a shard cluster when a referenced secret or configmap in the controller cluster is updated
func (f *fixture) expectedUpdateActions(controllerMla *nexuscontroller.MachineLearningAlgorithm, shardMla *nexuscontroller.MachineLearningAlgorithm, mlaSecret *corev1.Secret, mlaConfigMap *corev1.ConfigMap) {
	updatedSecretAction := core.NewUpdateAction(schema.GroupVersionResource{Resource: "secrets", Version: "v1"}, shardMla.Namespace, mlaSecret)
	updatedConfigAction := core.NewUpdateAction(schema.GroupVersionResource{Resource: "configmaps", Version: "v1"}, shardMla.Namespace, mlaConfigMap)
	updateMlaStatusAction := core.NewUpdateSubresourceAction(schema.GroupVersionResource{Resource: "machinelearningalgorithms"}, "status", controllerMla.Namespace, controllerMla)
	f.controllerNexusActions = append(f.controllerNexusActions, updateMlaStatusAction)
	f.shardKubeActions = append(f.shardKubeActions, updatedSecretAction, updatedConfigAction)
}

// TestCreatesMla test that resource creation results in a correct status update event for the main resource and correct resource creations in the shard cluster
func TestCreatesMla(t *testing.T) {
	f := newFixture(t)
	mlaSecret := newSecret("test-secret", nil)
	mlaConfigMap := newConfigMap("test-config", nil)
	mla := newMla("test", mlaSecret, mlaConfigMap, false)
	_, ctx := ktesting.NewTestContext(t)

	f = f.configure(
		&ControllerFixture{
			mlaListResults:       []*nexuscontroller.MachineLearningAlgorithm{mla},
			secretListResults:    []*corev1.Secret{mlaSecret},
			configMapListResults: []*corev1.ConfigMap{mlaConfigMap},
			existingCoreObjects:  []runtime.Object{},
			existingMlaObjects:   []runtime.Object{mla},
		},
		&NexusFixture{},
	)

	f.expectControllerUpdateMlaStatusAction(expectedMla(mla, mlaSecret, mlaConfigMap))
	f.expectShardActions(
		expectedShardMla(mla, ""),
		expectedShardSecret(mlaSecret, []*nexuscontroller.MachineLearningAlgorithm{expectedShardMla(mla, "")}),
		expectedShardConfigMap(mlaConfigMap, []*nexuscontroller.MachineLearningAlgorithm{expectedShardMla(mla, "")}),
		false)

	f.run(ctx, []cache.ObjectName{getRef(mla)}, false)
	t.Log("Controller successfully created a new MachineLearningAlgorithm and related secrets and configurations on the shard cluster")
}

// TestDetectsRogue tests the rogue secrets or configs are detected and reported as errors correctly
func TestDetectsRogue(t *testing.T) {
	f := newFixture(t)
	mlaSecret := newSecret("test-secret", nil)
	mlaConfigMap := newConfigMap("test-config", nil)
	mla := newMla("test", mlaSecret, mlaConfigMap, false)
	_, ctx := ktesting.NewTestContext(t)

	f = f.configure(
		&ControllerFixture{
			mlaListResults:       []*nexuscontroller.MachineLearningAlgorithm{mla},
			secretListResults:    []*corev1.Secret{mlaSecret},
			configMapListResults: []*corev1.ConfigMap{mlaConfigMap},
			existingCoreObjects:  []runtime.Object{},
			existingMlaObjects:   []runtime.Object{mla},
		},
		&NexusFixture{
			secretListResults: []*corev1.Secret{mlaSecret},
		},
	)

	f.expectControllerUpdateMlaStatusAction(expectedMlaWithErrors(mla, mlaSecret, mlaConfigMap, map[string]string{
		"shard0": "Resource \"test-secret\" already exists and is not managed by any Machine Learning Algorithm\n",
	}))
	f.expectShardActions(
		expectedShardMla(mla, ""),
		nil,
		expectedShardConfigMap(mlaConfigMap, []*nexuscontroller.MachineLearningAlgorithm{expectedShardMla(mla, "")}),
		false)

	f.run(ctx, []cache.ObjectName{getRef(mla)}, true)
	t.Log("Controller successfully detected a rogue resource on the shard cluster")
}

// TestHandlesNotExistingResource tests that missing Mla case is handled by the controller
func TestHandlesNotExistingResource(t *testing.T) {
	f := newFixture(t)
	mlaSecret := newSecret("test-secret", nil)
	mlaConfigMap := newConfigMap("test-config", nil)
	mla := newMla("test", mlaSecret, mlaConfigMap, false)
	_, ctx := ktesting.NewTestContext(t)

	f = f.configure(
		&ControllerFixture{
			mlaListResults: []*nexuscontroller.MachineLearningAlgorithm{},

			secretListResults:    []*corev1.Secret{},
			configMapListResults: []*corev1.ConfigMap{},
			existingCoreObjects:  []runtime.Object{},
			existingMlaObjects:   []runtime.Object{},
		},
		&NexusFixture{},
	)

	f.run(ctx, []cache.ObjectName{getRef(mla)}, false)
	t.Log("Controller successfully reported an error for the missing Mla resource")
}

// TestSkipsInvalidMla tests that resource creation is skipped with a status update in case referenced configurations do not exist
func TestSkipsInvalidMla(t *testing.T) {
	f := newFixture(t)
	mlaSecret := newSecret("test-secret", nil)
	mlaConfigMap := newConfigMap("test-config", nil)
	mla := newMla("test", mlaSecret, mlaConfigMap, false)
	_, ctx := ktesting.NewTestContext(t)

	f = f.configure(
		&ControllerFixture{
			mlaListResults: []*nexuscontroller.MachineLearningAlgorithm{mla},

			secretListResults:    []*corev1.Secret{},
			configMapListResults: []*corev1.ConfigMap{},
			existingCoreObjects:  []runtime.Object{},
			existingMlaObjects:   []runtime.Object{mla},
		},
		&NexusFixture{},
	)

	f.expectControllerUpdateMlaStatusAction(expectedMlaWithErrors(mla, mlaSecret, mlaConfigMap, map[string]string{"shard0": "secret \"test-secret\" not found\nconfigmap \"test-config\" not found"}))
	f.expectShardActions(
		expectedShardMla(mla, ""),
		nil,
		nil,
		false)

	f.run(ctx, []cache.ObjectName{getRef(mla)}, true)
	t.Log("Controller skipped a misconfigured Mla resource")
}

// TestUpdatesMlaSecretAndConfig test that update to a secret referenced by the MLA is propagated to shard clusters
func TestUpdatesMlaSecretAndConfig(t *testing.T) {
	f := newFixture(t)
	mlaSecret := newSecret("test-secret", nil)
	mlaSecretUpdated := mlaSecret.DeepCopy()
	mlaSecretUpdated.Data = map[string][]byte{
		"secret.file": []byte("updated-secret"),
	}

	mlaConfigMap := newConfigMap("test-config", nil)
	mlaConfigMapUpdated := mlaConfigMap.DeepCopy()
	mlaConfigMapUpdated.Data = map[string]string{
		"new.file": "updated-config",
	}

	mla := newMla("test", mlaSecretUpdated, mlaConfigMapUpdated, false)

	mlaOnShard := newMla("test", mlaSecret, mlaConfigMap, true)
	mlaSecretOnShard := newSecret("test-secret", mlaOnShard)
	mlaSecretOnShardUpdated := mlaSecretOnShard.DeepCopy()
	mlaSecretOnShardUpdated.Data = map[string][]byte{
		"secret.file": []byte("updated-secret"),
	}

	mlaConfigMapOnShard := newConfigMap("test-config", mlaOnShard)
	mlaConfigMapOnShardUpdated := mlaConfigMapOnShard.DeepCopy()
	mlaConfigMapOnShardUpdated.Data = map[string]string{
		"new.file": "updated-config",
	}

	_, ctx := ktesting.NewTestContext(t)

	f = f.configure(
		// controller lister returns a new secret and a new configmap
		&ControllerFixture{
			mlaListResults:       []*nexuscontroller.MachineLearningAlgorithm{mla},
			secretListResults:    []*corev1.Secret{mlaSecretUpdated},
			configMapListResults: []*corev1.ConfigMap{mlaConfigMapUpdated},
			existingCoreObjects:  []runtime.Object{mlaSecretUpdated, mlaConfigMapUpdated},
			existingMlaObjects:   []runtime.Object{mla},
		},
		&NexusFixture{
			mlaListResults:       []*nexuscontroller.MachineLearningAlgorithm{mlaOnShard},
			secretListResults:    []*corev1.Secret{mlaSecretOnShard},
			configMapListResults: []*corev1.ConfigMap{mlaConfigMapOnShard},
			existingCoreObjects:  []runtime.Object{mlaSecretOnShard, mlaConfigMapOnShard},
			existingMlaObjects:   []runtime.Object{mlaOnShard},
		},
	)

	f.expectedUpdateActions(expectedMla(mla, mlaSecretUpdated, mlaConfigMapUpdated), mlaOnShard, mlaSecretOnShardUpdated, mlaConfigMapOnShardUpdated)

	f.run(ctx, []cache.ObjectName{getRef(mla)}, false)
	t.Log("Controller successfully updated a Secret and a ConfigMap in the shard cluster after those were updated in the controller cluster")
}

// TestCreatesSharedResources tests that the controller can successfully create an MLA that owns the secret and configmap created by another MLA
func TestCreatesSharedResources(t *testing.T) {
	f := newFixture(t)
	mlaSecret := newSecret("test-secret", nil)
	mlaConfigMap := newConfigMap("test-config", nil)
	mla1 := newMla("test1", mlaSecret, mlaConfigMap, false)
	mla2 := newMla("test2", mlaSecret, mlaConfigMap, false)
	mlaSecretOnShard1 := expectedShardSecret(mlaSecret, []*nexuscontroller.MachineLearningAlgorithm{expectedShardMla(mla1, mla1.GetName())})
	mlaConfigOnShard1 := expectedShardConfigMap(mlaConfigMap, []*nexuscontroller.MachineLearningAlgorithm{expectedShardMla(mla1, mla1.GetName())})
	mlaOnShard1 := expectedShardMla(mla1, mla1.GetName())

	_, ctx := ktesting.NewTestContext(t)

	f = f.configure(
		&ControllerFixture{
			mlaListResults:       []*nexuscontroller.MachineLearningAlgorithm{expectedMla(mla1, mlaSecret, mlaConfigMap), mla2},
			secretListResults:    []*corev1.Secret{mlaSecret},
			configMapListResults: []*corev1.ConfigMap{mlaConfigMap},
			existingCoreObjects:  []runtime.Object{mlaSecret, mlaConfigMap},
			existingMlaObjects:   []runtime.Object{expectedMla(mla1, mlaSecret, mlaConfigMap), mla2},
		},
		// shard cluster now has an MLA, a secret and a configmap
		&NexusFixture{
			mlaListResults:       []*nexuscontroller.MachineLearningAlgorithm{mlaOnShard1},
			secretListResults:    []*corev1.Secret{mlaSecretOnShard1},
			configMapListResults: []*corev1.ConfigMap{mlaConfigOnShard1},
			existingCoreObjects:  []runtime.Object{mlaSecretOnShard1, mlaConfigOnShard1},
			existingMlaObjects:   []runtime.Object{mlaOnShard1},
		},
	)
	f.expectControllerUpdateMlaStatusAction(expectedMla(mla2, mlaSecret, mlaConfigMap))
	f.expectShardActions(expectedShardMla(mla2, ""), nil, nil, false)
	f.expectOwnershipUpdateActions(
		expectedShardSecret(mlaSecretOnShard1, []*nexuscontroller.MachineLearningAlgorithm{mlaOnShard1, expectedShardMla(mla2, "")}),
		expectedShardConfigMap(mlaConfigOnShard1, []*nexuscontroller.MachineLearningAlgorithm{mlaOnShard1, expectedShardMla(mla2, "")}))

	f.run(ctx, []cache.ObjectName{getRef(mla2)}, false)
	t.Log("Controller successfully created a second Mla resource referencing the same Secret and ConfigMap in the controller cluster")
}

// TestTakesOwnership test verifies that controller doesn't fail if it finds an existing MLA not created by it, and simply takes ownership
func TestTakesOwnership(t *testing.T) {
	f := newFixture(t)
	mlaSecret := newSecret("test-secret", nil)
	mlaConfigMap := newConfigMap("test-config", nil)
	mla := newMla("test", mlaSecret, mlaConfigMap, false)
	rogueMla := expectedShardMla(mla, "")
	rogueMla.Spec.MountDatadogSocket = false

	_, ctx := ktesting.NewTestContext(t)

	f = f.configure(
		&ControllerFixture{
			mlaListResults:       []*nexuscontroller.MachineLearningAlgorithm{mla},
			secretListResults:    []*corev1.Secret{mlaSecret},
			configMapListResults: []*corev1.ConfigMap{mlaConfigMap},
			existingCoreObjects:  []runtime.Object{mlaSecret, mlaConfigMap},
			existingMlaObjects:   []runtime.Object{mla},
		},
		&NexusFixture{
			mlaListResults:     []*nexuscontroller.MachineLearningAlgorithm{rogueMla},
			existingMlaObjects: []runtime.Object{rogueMla},
		},
	)

	f.expectControllerUpdateMlaStatusAction(expectedMla(mla, mlaSecret, mlaConfigMap))
	f.expectShardActions(
		expectedShardMla(mla, ""),
		expectedShardSecret(mlaSecret, []*nexuscontroller.MachineLearningAlgorithm{expectedShardMla(mla, "")}),
		expectedShardConfigMap(mlaConfigMap, []*nexuscontroller.MachineLearningAlgorithm{expectedShardMla(mla, "")}),
		true)

	f.run(ctx, []cache.ObjectName{getRef(mla)}, false)
	t.Log("Controller successfully took ownership of a MachineLearningAlgorithm and related secrets and configurations on the shard cluster")
}
