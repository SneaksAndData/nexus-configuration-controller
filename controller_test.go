/*
 * Copyright (c) 2024-2025. ECCO Data & AI Open-Source Project Maintainers.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 *   Unless required by applicable law or agreed to in writing, software
 *   distributed under the License is distributed on an "AS IS" BASIS,
 *   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *   See the License for the specific language governing permissions and
 *   limitations under the License.
 */

package main

import (
	"context"
	"github.com/SneaksAndData/nexus-core/pkg/generated/clientset/versioned/fake"
	sharding "github.com/SneaksAndData/nexus-core/pkg/shards"
	"github.com/aws/smithy-go/ptr"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/diff"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2/ktesting"
	"reflect"
	"testing"
	"time"

	nexusv1 "github.com/SneaksAndData/nexus-core/pkg/apis/science/v1"
	informers "github.com/SneaksAndData/nexus-core/pkg/generated/informers/externalversions"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
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
	templateListResults  []*nexusv1.NexusAlgorithmTemplate
	workgroupListResults []*nexusv1.NexusAlgorithmWorkgroup
	secretListResults    []*corev1.Secret
	configMapListResults []*corev1.ConfigMap

	existingCoreObjects  []runtime.Object
	existingNexusObjects []runtime.Object
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
	templateLister  []*nexusv1.NexusAlgorithmTemplate
	workgroupLister []*nexusv1.NexusAlgorithmWorkgroup
	secretLister    []*corev1.Secret
	configMapLister []*corev1.ConfigMap

	// Objects to put in the store for shard cluster
	shardTemplateLister  []*nexusv1.NexusAlgorithmTemplate
	shardWorkgroupLister []*nexusv1.NexusAlgorithmWorkgroup
	shardSecretLister    []*corev1.Secret
	shardConfigLister    []*corev1.ConfigMap

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

	f.templateLister = []*nexusv1.NexusAlgorithmTemplate{}
	f.workgroupLister = []*nexusv1.NexusAlgorithmWorkgroup{}
	f.secretLister = []*corev1.Secret{}
	f.configMapLister = []*corev1.ConfigMap{}
	f.controllerObjects = []runtime.Object{}
	f.controllerKubeObjects = []runtime.Object{}

	f.shardTemplateLister = []*nexusv1.NexusAlgorithmTemplate{}
	f.shardWorkgroupLister = []*nexusv1.NexusAlgorithmWorkgroup{}
	f.shardSecretLister = []*corev1.Secret{}
	f.shardConfigLister = []*corev1.ConfigMap{}
	f.shardObjects = []runtime.Object{}
	f.shardKubeObjects = []runtime.Object{}
	return f
}

// configure adds necessary mock return results for Kubernetes API calls for the respective listers
// and adds existing objects to the respective containers
func (f *fixture) configure(controllerFixture *ControllerFixture, nexusShardFixture *NexusFixture) *fixture {
	f.templateLister = append(f.templateLister, controllerFixture.templateListResults...)
	f.workgroupLister = append(f.workgroupLister, controllerFixture.workgroupListResults...)
	f.secretLister = append(f.secretLister, controllerFixture.secretListResults...)
	f.configMapLister = append(f.configMapLister, controllerFixture.configMapListResults...)
	f.controllerObjects = append(f.controllerObjects, controllerFixture.existingNexusObjects...)
	f.controllerKubeObjects = append(f.controllerKubeObjects, controllerFixture.existingCoreObjects...)

	f.shardTemplateLister = append(f.shardTemplateLister, nexusShardFixture.templateListResults...)
	f.shardWorkgroupLister = append(f.shardWorkgroupLister, nexusShardFixture.workgroupListResults...)
	f.shardSecretLister = append(f.shardSecretLister, nexusShardFixture.secretListResults...)
	f.shardConfigLister = append(f.shardConfigLister, nexusShardFixture.configMapListResults...)
	f.shardObjects = append(f.shardObjects, nexusShardFixture.existingNexusObjects...)
	f.shardKubeObjects = append(f.shardKubeObjects, nexusShardFixture.existingCoreObjects...)

	return f
}

func expectedTemplate(template *nexusv1.NexusAlgorithmTemplate, secret *corev1.Secret, configMap *corev1.ConfigMap, syncedTo []string, conditions []metav1.Condition) *nexusv1.NexusAlgorithmTemplate {
	templateCopy := template.DeepCopy()
	templateCopy.Status.Conditions = conditions
	if secret != nil {
		templateCopy.Status.SyncedSecrets = []string{secret.Name}
	}

	if configMap != nil {
		templateCopy.Status.SyncedConfigurations = []string{configMap.Name}
	}
	if syncedTo != nil {
		templateCopy.Status.SyncedToClusters = syncedTo
	}

	return templateCopy
}

func expectedWorkgroup(workgroup *nexusv1.NexusAlgorithmWorkgroup, conditions []metav1.Condition) *nexusv1.NexusAlgorithmWorkgroup {
	workgroupCopy := workgroup.DeepCopy()
	workgroupCopy.Status.Conditions = conditions

	return workgroupCopy
}

func expectedShardWorkgroup(workgroup *nexusv1.NexusAlgorithmWorkgroup, name string) *nexusv1.NexusAlgorithmWorkgroup {
	workgroupCopy := workgroup.DeepCopy()
	workgroupCopy.UID = types.UID(name)
	workgroupCopy.Labels = expectedLabels()

	return workgroupCopy
}

func expectedLabels() map[string]string {
	return map[string]string{
		"science.sneaksanddata.com/controller-app":      "nexus-configuration-controller",
		"science.sneaksanddata.com/configuration-owner": "test-controller-cluster",
	}
}

func expectedShardTemplate(template *nexusv1.NexusAlgorithmTemplate, uid string) *nexusv1.NexusAlgorithmTemplate {
	templateCopy := template.DeepCopy()
	templateCopy.UID = types.UID(uid)
	templateCopy.Labels = expectedLabels()

	return templateCopy
}

func expectedShardSecret(secret *corev1.Secret, templates []*nexusv1.NexusAlgorithmTemplate) *corev1.Secret {
	secretCopy := secret.DeepCopy()
	secretCopy.Labels = expectedLabels()
	secretCopy.OwnerReferences = make([]metav1.OwnerReference, 0)
	for _, template := range templates {
		secretCopy.OwnerReferences = append(secretCopy.OwnerReferences, metav1.OwnerReference{
			APIVersion: nexusv1.SchemeGroupVersion.String(),
			Kind:       "NexusAlgorithmTemplate",
			Name:       template.Name,
			UID:        template.UID,
		})
	}

	return secretCopy
}

func expectedShardConfigMap(configMap *corev1.ConfigMap, templates []*nexusv1.NexusAlgorithmTemplate) *corev1.ConfigMap {
	configMapCopy := configMap.DeepCopy()
	configMapCopy.Labels = expectedLabels()
	configMapCopy.OwnerReferences = make([]metav1.OwnerReference, 0)
	for _, template := range templates {
		configMapCopy.OwnerReferences = append(configMapCopy.OwnerReferences, metav1.OwnerReference{
			APIVersion: nexusv1.SchemeGroupVersion.String(),
			Kind:       "NexusAlgorithmTemplate",
			Name:       template.Name,
			UID:        template.UID,
		})
	}

	return configMapCopy
}

func newWorkgroup(name string, onShard bool, clusterName string, status *nexusv1.NexusAlgorithmWorkgroupStatus) *nexusv1.NexusAlgorithmWorkgroup {
	var labels map[string]string
	if onShard {
		labels = expectedLabels()
	}

	workgroup := &nexusv1.NexusAlgorithmWorkgroup{
		TypeMeta: metav1.TypeMeta{APIVersion: nexusv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: metav1.NamespaceDefault,
			Labels:    labels,
			UID:       types.UID(name),
		},
		Spec: nexusv1.NexusAlgorithmWorkgroupSpec{
			Description:  "test workgroup",
			Capabilities: make(map[string]bool),
			Cluster:      clusterName,
			Tolerations:  []corev1.Toleration{},
			Affinity:     &corev1.Affinity{},
		},
	}

	if status != nil {
		workgroup.Status = *status
	}

	return workgroup
}

func newTemplate(name string, secret *corev1.Secret, configMap *corev1.ConfigMap, onShard bool, status *nexusv1.NexusAlgorithmStatus) *nexusv1.NexusAlgorithmTemplate {
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

	template := &nexusv1.NexusAlgorithmTemplate{
		TypeMeta: metav1.TypeMeta{APIVersion: nexusv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: metav1.NamespaceDefault,
			Labels:    labels,
			UID:       types.UID(name),
		},
		Spec: nexusv1.NexusAlgorithmSpec{
			Container: &nexusv1.NexusAlgorithmContainer{
				Image:              "test",
				Registry:           "test",
				VersionTag:         "v1.0.0",
				ServiceAccountName: "test",
			},
			ComputeResources: &nexusv1.NexusAlgorithmResources{
				CpuLimit:        "1000m",
				MemoryLimit:     "2000Mi",
				CustomResources: nil,
			},
			WorkgroupRef: &nexusv1.NexusAlgorithmWorkgroupRef{
				Name:  "test",
				Group: "test",
				Kind:  "NexusAlgorithmWorkgroup",
			},
			Command: "python",
			Args:    cargs,
			RuntimeEnvironment: &nexusv1.NexusAlgorithmRuntimeEnvironment{
				EnvironmentVariables:       nil,
				MappedEnvironmentVariables: envFrom,
				Annotations:                nil,
				DeadlineSeconds:            nil,
				MaximumRetries:             nil,
			},
			ErrorHandlingBehaviour: &nexusv1.NexusErrorHandlingBehaviour{
				TransientExitCodes: make([]int32, 0),
				FatalExitCodes:     make([]int32, 0),
			},
			DatadogIntegrationSettings: &nexusv1.NexusDatadogIntegrationSettings{
				MountDatadogSocket: ptr.Bool(true),
			},
		},
	}

	if status != nil {
		template.Status = *status
	}

	return template
}

func newSecret(name string, owner *nexusv1.NexusAlgorithmTemplate) *corev1.Secret {
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
			{
				APIVersion: nexusv1.SchemeGroupVersion.String(),
				Kind:       "NexusAlgorithmTemplate",
				Name:       owner.Name,
				UID:        owner.UID,
			},
		})
	}
	return &secret
}

func newConfigMap(name string, owner *nexusv1.NexusAlgorithmTemplate) *corev1.ConfigMap {
	configMap := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: metav1.NamespaceDefault,
		},
		Data: make(map[string]string),
	}

	if owner != nil {
		configMap.SetOwnerReferences([]metav1.OwnerReference{
			{
				APIVersion: nexusv1.SchemeGroupVersion.String(),
				Kind:       "NexusAlgorithmTemplate",
				Name:       owner.Name,
				UID:        owner.UID,
			},
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
		case *nexusv1.NexusAlgorithmTemplate:
			// avoid issues with time drift
			currentTime := metav1.Now()
			expCopy := expObject.DeepCopyObject().(*nexusv1.NexusAlgorithmTemplate)
			for ix := range expCopy.Status.Conditions {
				expCopy.Status.Conditions[ix].LastTransitionTime = currentTime
			}

			objCopy := object.DeepCopyObject().(*nexusv1.NexusAlgorithmTemplate)
			for ix := range objCopy.Status.Conditions {
				objCopy.Status.Conditions[ix].LastTransitionTime = currentTime
			}

			if !reflect.DeepEqual(expCopy, objCopy) {
				t.Errorf("Action %s %s has wrong object\nDiff:\n %s",
					a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintSideBySide(expCopy, objCopy))
			}
		case *nexusv1.NexusAlgorithmWorkgroup:
			// avoid issues with time drift
			currentTime := metav1.Now()
			expCopy := expObject.DeepCopyObject().(*nexusv1.NexusAlgorithmWorkgroup)
			for ix := range expCopy.Status.Conditions {
				expCopy.Status.Conditions[ix].LastTransitionTime = currentTime
			}

			objCopy := object.DeepCopyObject().(*nexusv1.NexusAlgorithmWorkgroup)
			for ix := range objCopy.Status.Conditions {
				objCopy.Status.Conditions[ix].LastTransitionTime = currentTime
			}

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
	case core.DeleteActionImpl:
		e, _ := expected.(core.DeleteActionImpl)
		if e.GetName() != a.GetName() || e.GetNamespace() != a.GetNamespace() {
			t.Errorf("Action %s targets wrong resource %s/%s, should target %s/%s", a.GetVerb(), a.GetNamespace(), a.GetName(), e.GetNamespace(), e.GetName())
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
			(action.Matches("list", "NexusAlgorithmTemplates") ||
				action.Matches("watch", "NexusAlgorithmTemplates") ||
				action.Matches("list", "NexusAlgorithmWorkgroups") ||
				action.Matches("watch", "NexusAlgorithmWorkgroups") ||
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
	// CRD fakes do not properly support OpenAPI schema gen, see https://github.com/kubernetes/kubernetes/issues/126850
	// Using deprecated clients for now
	f.controllerNexusClient = fake.NewSimpleClientset(f.controllerObjects...)
	f.controllerKubeClient = k8sfake.NewClientset(f.controllerKubeObjects...)

	f.shardNexusClient = fake.NewSimpleClientset(f.shardObjects...)
	f.shardKubeClient = k8sfake.NewClientset(f.shardKubeObjects...)

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
		shardNexusInf.Science().V1().NexusAlgorithmTemplates(),
		shardNexusInf.Science().V1().NexusAlgorithmWorkgroups(),
		shardKubeInf.Core().V1().Secrets(),
		shardKubeInf.Core().V1().ConfigMaps())

	newShard.TemplateSynced = alwaysReady
	newShard.WorkgroupSynced = alwaysReady
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
		controllerNexusInf.Science().V1().NexusAlgorithmTemplates(),
		controllerNexusInf.Science().V1().NexusAlgorithmWorkgroups(),
		30*time.Millisecond,
		5*time.Second,
		50,
		300,
	)

	c.templateSynced = alwaysReady
	c.workgroupSynced = alwaysReady
	c.secretsSynced = alwaysReady
	c.configMapsSynced = alwaysReady
	c.recorder = &record.FakeRecorder{}

	for _, d := range f.templateLister {
		_ = controllerNexusInf.Science().V1().NexusAlgorithmTemplates().Informer().GetIndexer().Add(d)
	}

	for _, d := range f.shardTemplateLister {
		_ = shardNexusInf.Science().V1().NexusAlgorithmTemplates().Informer().GetIndexer().Add(d)
	}

	for _, d := range f.workgroupLister {
		_ = controllerNexusInf.Science().V1().NexusAlgorithmWorkgroups().Informer().GetIndexer().Add(d)
	}

	for _, d := range f.shardWorkgroupLister {
		_ = shardNexusInf.Science().V1().NexusAlgorithmWorkgroups().Informer().GetIndexer().Add(d)
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

func (f *fixture) runController(ctx context.Context, templateRefs []cache.ObjectName, workgroupRefs []cache.ObjectName, startInformers bool, expectError bool) {
	controllerRef, controllerInformers, shardInformers := f.newController(ctx)
	if startInformers {
		controllerInformers.nexusInformers.Start(ctx.Done())
		controllerInformers.k8sInformers.Start(ctx.Done())

		shardInformers.nexusInformers.Start(ctx.Done())
		shardInformers.k8sInformers.Start(ctx.Done())
	}

	for _, templateRef := range templateRefs {
		err := controllerRef.templateSyncHandler(ctx, templateRef)
		if !expectError && err != nil {
			f.t.Errorf("error syncing template: %v", err)
		} else if expectError && err == nil {
			f.t.Error("expected error syncing template, got nil")
		}
	}

	for _, workgroupRef := range workgroupRefs {
		err := controllerRef.workgroupSyncHandler(ctx, workgroupRef)
		if !expectError && err != nil {
			f.t.Errorf("error syncing workgroup: %v", err)
		} else if expectError && err == nil {
			f.t.Error("expected error syncing workgroup, got nil")
		}
	}

	f.checkActions(f.controllerNexusActions, f.controllerNexusClient.Actions())
	f.checkActions(f.shardNexusActions, f.shardNexusClient.Actions())
	f.checkActions(f.shardKubeActions, f.shardKubeClient.Actions())
	f.checkActions(f.controllerKubeActions, f.controllerKubeClient.Actions())
}

func (f *fixture) runObjectHandler(ctx context.Context, objs []interface{}, startInformers bool) {
	controllerRef, controllerInformers, shardInformers := f.newController(ctx)
	if startInformers {
		controllerInformers.nexusInformers.Start(ctx.Done())
		controllerInformers.k8sInformers.Start(ctx.Done())

		shardInformers.nexusInformers.Start(ctx.Done())
		shardInformers.k8sInformers.Start(ctx.Done())
	}

	for _, obj := range objs {
		controllerRef.handleObject(obj)
	}

	f.checkActions(f.shardNexusActions, f.shardNexusClient.Actions())
	f.checkActions(f.shardKubeActions, f.shardKubeClient.Actions())
}

func (f *fixture) run(ctx context.Context, templateRefs []cache.ObjectName, workgroupRefs []cache.ObjectName, expectError bool) {
	f.runController(ctx, templateRefs, workgroupRefs, true, expectError)
}

func getRef(template *nexusv1.NexusAlgorithmTemplate) cache.ObjectName {
	ref := cache.MetaObjectToName(template)
	return ref
}

func getWorkgroupRef(workgroup *nexusv1.NexusAlgorithmWorkgroup) cache.ObjectName {
	ref := cache.MetaObjectToName(workgroup)
	return ref
}

// expectControllerUpdateWorkgroupStatusAction sets expectations for the workgroup actions in a controller cluster
// for a workgroup in the controller cluster we only expect a status update
func (f *fixture) expectControllerUpdateWorkgroupStatusAction(workgroup *nexusv1.NexusAlgorithmWorkgroup) {
	updateWorkgroupStatusAction := core.NewUpdateSubresourceAction(schema.GroupVersionResource{Resource: "NexusAlgorithmWorkgroups"}, "status", workgroup.Namespace, workgroup)
	f.controllerNexusActions = append(f.controllerNexusActions, updateWorkgroupStatusAction)
}

// expectControllerUpdateTemplateStatusAction sets expectations for the resource actions in a controller cluster
// for a template in the controller cluster we only expect a status update
func (f *fixture) expectControllerUpdateTemplateStatusAction(template *nexusv1.NexusAlgorithmTemplate) {
	updateTemplateStatusAction := core.NewUpdateSubresourceAction(schema.GroupVersionResource{Resource: "NexusAlgorithmTemplates"}, "status", template.Namespace, template)
	f.controllerNexusActions = append(f.controllerNexusActions, updateTemplateStatusAction)
}

// expectShardActions sets expectations for the resource actions in a shard cluster
// for resources in the shard cluster we expect the following: Template is created, all referenced secrets and configmaps are created, with owner references assigned
func (f *fixture) expectShardActions(shardTemplate *nexusv1.NexusAlgorithmTemplate, templateSecret *corev1.Secret, templateConfigMap *corev1.ConfigMap, templateUpdated bool) {
	if !templateUpdated {
		f.shardNexusActions = append(f.shardNexusActions, core.NewCreateAction(schema.GroupVersionResource{Resource: "NexusAlgorithmTemplates"}, shardTemplate.Namespace, shardTemplate))
	} else {
		f.shardNexusActions = append(f.shardNexusActions, core.NewUpdateAction(schema.GroupVersionResource{Resource: "NexusAlgorithmTemplates"}, shardTemplate.Namespace, shardTemplate))
	}

	if templateSecret != nil {
		f.shardKubeActions = append(f.shardKubeActions, core.NewCreateAction(schema.GroupVersionResource{Resource: "secrets", Version: "v1"}, templateSecret.Namespace, templateSecret))
	}

	if templateConfigMap != nil {
		f.shardKubeActions = append(f.shardKubeActions, core.NewCreateAction(schema.GroupVersionResource{Resource: "configmaps", Version: "v1"}, templateConfigMap.Namespace, templateConfigMap))
	}
}

// expectShardWorkgroupActions sets expectations for the workgroup actions in a shard cluster
// for workgroup in the shard cluster we expect the following: Workgroup is created or updated
func (f *fixture) expectShardWorkgroupActions(shardWorkgroup *nexusv1.NexusAlgorithmWorkgroup, updated bool) {
	if !updated {
		f.shardNexusActions = append(f.shardNexusActions, core.NewCreateAction(schema.GroupVersionResource{Resource: "NexusAlgorithmWorkgroups"}, shardWorkgroup.Namespace, shardWorkgroup))
	} else {
		f.shardNexusActions = append(f.shardNexusActions, core.NewUpdateAction(schema.GroupVersionResource{Resource: "NexusAlgorithmWorkgroups"}, shardWorkgroup.Namespace, shardWorkgroup))
	}
}

func (f *fixture) expectOwnershipUpdateActions(templateSecret *corev1.Secret, templateConfigMap *corev1.ConfigMap) {
	if templateSecret != nil {
		f.shardKubeActions = append(f.shardKubeActions, core.NewUpdateAction(schema.GroupVersionResource{Resource: "secrets", Version: "v1"}, templateSecret.Namespace, templateSecret))
	}

	if templateConfigMap != nil {
		f.shardKubeActions = append(f.shardKubeActions, core.NewUpdateAction(schema.GroupVersionResource{Resource: "configmaps", Version: "v1"}, templateConfigMap.Namespace, templateConfigMap))
	}
}

// expectedUpdateActions sets expectations for the resource actions in a shard cluster when a referenced secret or configmap in the controller cluster is updated
func (f *fixture) expectedUpdateActions(controllerTemplate *nexusv1.NexusAlgorithmTemplate, shardTemplate *nexusv1.NexusAlgorithmTemplate, templateSecret *corev1.Secret, templateConfigMap *corev1.ConfigMap, controllerStatusUpdated bool) {
	updatedSecretAction := core.NewUpdateAction(schema.GroupVersionResource{Resource: "secrets", Version: "v1"}, shardTemplate.Namespace, templateSecret)
	updatedConfigAction := core.NewUpdateAction(schema.GroupVersionResource{Resource: "configmaps", Version: "v1"}, shardTemplate.Namespace, templateConfigMap)
	if controllerStatusUpdated {
		f.controllerNexusActions = append(f.controllerNexusActions, core.NewUpdateSubresourceAction(schema.GroupVersionResource{Resource: "NexusAlgorithmTemplates"}, "status", controllerTemplate.Namespace, controllerTemplate))
	}
	f.shardKubeActions = append(f.shardKubeActions, updatedSecretAction, updatedConfigAction)
}

// expectedUpdateActions sets expectations for the resource actions in a shard cluster when a referenced secret or configmap in the controller cluster is updated
func (f *fixture) expectedControllerUpdateActions(controllerTemplate *nexusv1.NexusAlgorithmTemplate, templateSecret *corev1.Secret, templateConfigMap *corev1.ConfigMap, controllerStatusUpdated bool) {
	updatedSecretAction := core.NewUpdateAction(schema.GroupVersionResource{Resource: "secrets", Version: "v1"}, controllerTemplate.Namespace, templateSecret)
	updatedConfigAction := core.NewUpdateAction(schema.GroupVersionResource{Resource: "configmaps", Version: "v1"}, controllerTemplate.Namespace, templateConfigMap)
	if controllerStatusUpdated {
		f.controllerNexusActions = append(f.controllerNexusActions, core.NewUpdateSubresourceAction(schema.GroupVersionResource{Resource: "NexusAlgorithmTemplates"}, "status", controllerTemplate.Namespace, controllerTemplate))
	}
	f.controllerKubeActions = append(f.controllerKubeActions, updatedSecretAction, updatedConfigAction)
}

// expectedDeleteActions sets expectations for resource deletions
func (f *fixture) expectedDeleteActions(shardTemplate *nexusv1.NexusAlgorithmTemplate) {
	f.shardNexusActions = append(f.shardNexusActions, core.NewDeleteAction(schema.GroupVersionResource{Resource: "NexusAlgorithmTemplates"}, shardTemplate.Namespace, shardTemplate.Name))
}

func provisionControllerResources() (*corev1.Secret, *corev1.ConfigMap, *nexusv1.NexusAlgorithmTemplate) {
	templateSecret := newSecret("test-secret", nil)
	templateConfigMap := newConfigMap("test-config", nil)
	template := newTemplate("test", templateSecret, templateConfigMap, false, nil)
	return templateSecret, templateConfigMap, template
}

func provisionOwnedControllerResources(templateSecret *corev1.Secret, templateConfigMap *corev1.ConfigMap, template *nexusv1.NexusAlgorithmTemplate) (*corev1.Secret, *corev1.ConfigMap) {
	ownedTemplateSecret := templateSecret.DeepCopy()
	ownedTemplateSecret.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: nexusv1.SchemeGroupVersion.String(),
			Kind:       "NexusAlgorithmTemplate",
			Name:       template.Name,
			UID:        template.UID,
		},
	}
	ownedTemplateConfigMap := templateConfigMap.DeepCopy()
	ownedTemplateConfigMap.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: nexusv1.SchemeGroupVersion.String(),
			Kind:       "NexusAlgorithmTemplate",
			Name:       template.Name,
			UID:        template.UID,
		},
	}

	return ownedTemplateSecret, ownedTemplateConfigMap
}

// TestCreatesTemplate test that resource creation results in a correct status update event for the main resource and correct resource creations in the shard cluster
func TestCreatesTemplate(t *testing.T) {
	f := newFixture(t)
	templateSecret, templateConfigMap, template := provisionControllerResources()
	ownedtemplateSecret, ownedtemplateConfigMap := provisionOwnedControllerResources(templateSecret, templateConfigMap, template)

	_, ctx := ktesting.NewTestContext(t)

	f = f.configure(
		&ControllerFixture{
			templateListResults:  []*nexusv1.NexusAlgorithmTemplate{template},
			secretListResults:    []*corev1.Secret{templateSecret},
			configMapListResults: []*corev1.ConfigMap{templateConfigMap},
			existingCoreObjects:  []runtime.Object{templateSecret, templateConfigMap},
			existingNexusObjects: []runtime.Object{template},
		},
		&NexusFixture{},
	)

	f.expectControllerUpdateTemplateStatusAction(expectedTemplate(template, nil, nil, nil, []metav1.Condition{
		*nexusv1.NewResourceReadyCondition(
			metav1.Now(),
			metav1.ConditionFalse,
			"Algorithm \"test\" initializing",
		),
	}))
	f.expectControllerUpdateTemplateStatusAction(expectedTemplate(template, templateSecret, templateConfigMap, []string{"shard0"}, []metav1.Condition{
		*nexusv1.NewResourceReadyCondition(
			metav1.Now(),
			metav1.ConditionTrue,
			"Algorithm \"test\" ready",
		),
	}))

	f.expectedControllerUpdateActions(template, ownedtemplateSecret, ownedtemplateConfigMap, false)

	f.expectShardActions(
		expectedShardTemplate(template, ""),
		expectedShardSecret(templateSecret, []*nexusv1.NexusAlgorithmTemplate{expectedShardTemplate(template, "")}),
		expectedShardConfigMap(templateConfigMap, []*nexusv1.NexusAlgorithmTemplate{expectedShardTemplate(template, "")}),
		false)

	f.run(ctx, []cache.ObjectName{getRef(template)}, []cache.ObjectName{}, false)
	t.Log("Controller successfully created a new NexusAlgorithmTemplate and related secrets and configurations on the shard cluster")
}

// TestDetectsRogue tests the rogue secrets or configs are detected and reported as errors correctly
func TestDetectsRogue(t *testing.T) {
	f := newFixture(t)
	templateSecret, templateConfigMap, template := provisionControllerResources()
	ownedTemplateSecret, ownedTemplateConfigMap := provisionOwnedControllerResources(templateSecret, templateConfigMap, template)

	_, ctx := ktesting.NewTestContext(t)

	f = f.configure(
		&ControllerFixture{
			templateListResults:  []*nexusv1.NexusAlgorithmTemplate{template},
			secretListResults:    []*corev1.Secret{templateSecret},
			configMapListResults: []*corev1.ConfigMap{templateConfigMap},
			existingCoreObjects:  []runtime.Object{templateSecret, templateConfigMap},
			existingNexusObjects: []runtime.Object{template},
		},
		&NexusFixture{
			secretListResults: []*corev1.Secret{templateSecret},
		},
	)

	f.expectControllerUpdateTemplateStatusAction(expectedTemplate(template, nil, nil, nil, []metav1.Condition{
		*nexusv1.NewResourceReadyCondition(
			metav1.Now(),
			metav1.ConditionFalse,
			"Algorithm \"test\" initializing",
		),
	}))
	// no actions expected due to fail-fast approach in sync
	f.expectShardActions(
		expectedShardTemplate(template, ""),
		nil,
		nil,
		false)

	f.expectedControllerUpdateActions(template, ownedTemplateSecret, ownedTemplateConfigMap, false)

	f.run(ctx, []cache.ObjectName{getRef(template)}, []cache.ObjectName{}, true)
	t.Log("Controller successfully detected a rogue resource on the shard cluster")
}

// TestHandlesNotExistingResource tests that missing template case is handled by the controller
func TestHandlesNotExistingResource(t *testing.T) {
	f := newFixture(t)
	_, _, template := provisionControllerResources()

	_, ctx := ktesting.NewTestContext(t)

	f = f.configure(
		&ControllerFixture{
			templateListResults: []*nexusv1.NexusAlgorithmTemplate{},

			secretListResults:    []*corev1.Secret{},
			configMapListResults: []*corev1.ConfigMap{},
			existingCoreObjects:  []runtime.Object{},
			existingNexusObjects: []runtime.Object{},
		},
		&NexusFixture{},
	)

	f.run(ctx, []cache.ObjectName{getRef(template)}, []cache.ObjectName{}, false)
	t.Log("Controller successfully reported an error for the missing Template resource")
}

// TestSkipsInvalidTemplate tests that resource creation is skipped with a status update in case referenced configurations do not exist
func TestSkipsInvalidTemplate(t *testing.T) {
	f := newFixture(t)
	_, _, template := provisionControllerResources()
	_, ctx := ktesting.NewTestContext(t)

	f = f.configure(
		&ControllerFixture{
			templateListResults: []*nexusv1.NexusAlgorithmTemplate{template},

			secretListResults:    []*corev1.Secret{},
			configMapListResults: []*corev1.ConfigMap{},
			existingCoreObjects:  []runtime.Object{},
			existingNexusObjects: []runtime.Object{template},
		},
		&NexusFixture{},
	)

	f.expectControllerUpdateTemplateStatusAction(expectedTemplate(template, nil, nil, nil, []metav1.Condition{
		*nexusv1.NewResourceReadyCondition(
			metav1.Now(),
			metav1.ConditionFalse,
			"Algorithm \"test\" initializing",
		),
	}))

	f.run(ctx, []cache.ObjectName{getRef(template)}, []cache.ObjectName{}, true)
	t.Log("Controller skipped a misconfigured Template resource")
}

// TestUpdatesTemplateSecretAndConfig test that update to a secret referenced by the Template is propagated to shard clusters
func TestUpdatesTemplateSecretAndConfig(t *testing.T) {
	f := newFixture(t)
	templateSecret, templateConfigMap, template := provisionControllerResources()
	ownedtemplateSecret, ownedtemplateConfigMap := provisionOwnedControllerResources(templateSecret, templateConfigMap, template)

	templateSecretUpdated := ownedtemplateSecret.DeepCopy()
	templateSecretUpdated.Data = map[string][]byte{
		"secret.file": []byte("updated-secret"),
	}

	templateConfigMapUpdated := ownedtemplateConfigMap.DeepCopy()
	templateConfigMapUpdated.Data = map[string]string{
		"new.file": "updated-config",
	}

	template = newTemplate("test", templateSecretUpdated, templateConfigMapUpdated, false, &nexusv1.NexusAlgorithmStatus{
		SyncedSecrets:        []string{"test-secret"},
		SyncedConfigurations: []string{"test-config"},
		SyncedToClusters:     []string{"shard0"},
		Conditions: []metav1.Condition{
			*nexusv1.NewResourceReadyCondition(
				metav1.Now(),
				metav1.ConditionTrue,
				"Algorithm \"test\" ready",
			),
		},
	})

	templateOnShard := newTemplate("test", templateSecret, templateConfigMap, true, nil)
	templateSecretOnShard := newSecret("test-secret", templateOnShard)
	templateSecretOnShardUpdated := templateSecretOnShard.DeepCopy()
	templateSecretOnShardUpdated.Data = map[string][]byte{
		"secret.file": []byte("updated-secret"),
	}

	templateConfigMapOnShard := newConfigMap("test-config", templateOnShard)
	templateConfigMapOnShardUpdated := templateConfigMapOnShard.DeepCopy()
	templateConfigMapOnShardUpdated.Data = map[string]string{
		"new.file": "updated-config",
	}

	_, ctx := ktesting.NewTestContext(t)

	f = f.configure(
		// controller lister returns a new secret and a new configmap
		&ControllerFixture{
			templateListResults:  []*nexusv1.NexusAlgorithmTemplate{template},
			secretListResults:    []*corev1.Secret{templateSecretUpdated},
			configMapListResults: []*corev1.ConfigMap{templateConfigMapUpdated},
			existingCoreObjects:  []runtime.Object{templateSecretUpdated, templateConfigMapUpdated},
			existingNexusObjects: []runtime.Object{template},
		},
		&NexusFixture{
			templateListResults:  []*nexusv1.NexusAlgorithmTemplate{templateOnShard},
			secretListResults:    []*corev1.Secret{templateSecretOnShard},
			configMapListResults: []*corev1.ConfigMap{templateConfigMapOnShard},
			existingCoreObjects:  []runtime.Object{templateSecretOnShard, templateConfigMapOnShard},
			existingNexusObjects: []runtime.Object{templateOnShard},
		},
	)

	// secret or cfg updates are not show in resource conditions rn
	f.expectedUpdateActions(
		expectedTemplate(template, templateSecretUpdated, templateConfigMapUpdated, []string{"shard0"}, nil),
		templateOnShard, templateSecretOnShardUpdated, templateConfigMapOnShardUpdated, false)

	f.run(ctx, []cache.ObjectName{getRef(template)}, []cache.ObjectName{}, false)
	t.Log("Controller successfully updated a Secret and a ConfigMap in the shard cluster after those were updated in the controller cluster")
}

// TestCreatesSharedResources tests that the controller can successfully create a Template that owns the secret and configmap created by another Template
func TestCreatesSharedResources(t *testing.T) {
	f := newFixture(t)
	templateSecret := newSecret("test-secret", nil)
	templateConfigMap := newConfigMap("test-config", nil)
	template1 := newTemplate("test1", templateSecret, templateConfigMap, false, &nexusv1.NexusAlgorithmStatus{
		SyncedSecrets:        []string{"test-secret"},
		SyncedConfigurations: []string{"test-config"},
		SyncedToClusters:     []string{"shard0"},
		Conditions: []metav1.Condition{
			*nexusv1.NewResourceReadyCondition(metav1.Now(), metav1.ConditionTrue, "Algorithm \"test1\" ready"),
		},
	})
	ownedTemplate1Secret, ownedTemplate1ConfigMap := provisionOwnedControllerResources(templateSecret, templateConfigMap, template1)

	template2 := newTemplate("test2", templateSecret, templateConfigMap, false, nil)
	ownedTemplate12Secret := ownedTemplate1Secret.DeepCopy()
	ownedTemplate12Secret.OwnerReferences = append(ownedTemplate12Secret.OwnerReferences, metav1.OwnerReference{
		APIVersion: nexusv1.SchemeGroupVersion.String(),
		Kind:       "NexusAlgorithmTemplate",
		Name:       template2.Name,
		UID:        template2.UID,
	})
	ownedTemplate12ConfigMap := ownedTemplate1ConfigMap.DeepCopy()
	ownedTemplate12ConfigMap.OwnerReferences = append(ownedTemplate12ConfigMap.OwnerReferences, metav1.OwnerReference{
		APIVersion: nexusv1.SchemeGroupVersion.String(),
		Kind:       "NexusAlgorithmTemplate",
		Name:       template2.Name,
		UID:        template2.UID,
	})

	templateSecretOnShard1 := expectedShardSecret(templateSecret, []*nexusv1.NexusAlgorithmTemplate{expectedShardTemplate(template1, template1.GetName())})
	templateConfigOnShard1 := expectedShardConfigMap(templateConfigMap, []*nexusv1.NexusAlgorithmTemplate{expectedShardTemplate(template1, template1.GetName())})
	templateOnShard1 := expectedShardTemplate(template1, template1.GetName())

	_, ctx := ktesting.NewTestContext(t)

	f = f.configure(
		&ControllerFixture{
			templateListResults: []*nexusv1.NexusAlgorithmTemplate{expectedTemplate(template1, templateSecret, templateConfigMap, []string{"shard0"}, []metav1.Condition{
				*nexusv1.NewResourceReadyCondition(metav1.Now(), metav1.ConditionTrue, "Algorithm \"test1\" ready"),
			}), template2},
			secretListResults:    []*corev1.Secret{ownedTemplate1Secret},
			configMapListResults: []*corev1.ConfigMap{ownedTemplate1ConfigMap},
			existingCoreObjects:  []runtime.Object{ownedTemplate1Secret, ownedTemplate1ConfigMap},
			existingNexusObjects: []runtime.Object{expectedTemplate(template1, templateSecret, templateConfigMap, []string{"shard0"}, []metav1.Condition{
				*nexusv1.NewResourceReadyCondition(metav1.Now(), metav1.ConditionTrue, "Algorithm \"test1\" ready"),
			}), template2},
		},
		// shard cluster now has a Template, a secret and a configmap
		&NexusFixture{
			templateListResults:  []*nexusv1.NexusAlgorithmTemplate{templateOnShard1},
			secretListResults:    []*corev1.Secret{templateSecretOnShard1},
			configMapListResults: []*corev1.ConfigMap{templateConfigOnShard1},
			existingCoreObjects:  []runtime.Object{templateSecretOnShard1, templateConfigOnShard1},
			existingNexusObjects: []runtime.Object{templateOnShard1},
		},
	)
	f.expectControllerUpdateTemplateStatusAction(expectedTemplate(template2, nil, nil, nil, []metav1.Condition{
		*nexusv1.NewResourceReadyCondition(
			metav1.Now(),
			metav1.ConditionFalse,
			"Algorithm \"test2\" initializing",
		)}))
	f.expectControllerUpdateTemplateStatusAction(expectedTemplate(template2, templateSecret, templateConfigMap, []string{"shard0"}, []metav1.Condition{
		*nexusv1.NewResourceReadyCondition(
			metav1.Now(),
			metav1.ConditionTrue,
			"Algorithm \"test2\" ready",
		),
	}))
	f.expectedControllerUpdateActions(template2, ownedTemplate12Secret, ownedTemplate12ConfigMap, false)
	f.expectShardActions(expectedShardTemplate(template2, ""), nil, nil, false)
	f.expectOwnershipUpdateActions(
		expectedShardSecret(templateSecretOnShard1, []*nexusv1.NexusAlgorithmTemplate{templateOnShard1, expectedShardTemplate(template2, "")}),
		expectedShardConfigMap(templateConfigOnShard1, []*nexusv1.NexusAlgorithmTemplate{templateOnShard1, expectedShardTemplate(template2, "")}))

	f.run(ctx, []cache.ObjectName{getRef(template2)}, []cache.ObjectName{}, false)
	t.Log("Controller successfully created a second Template resource referencing the same Secret and ConfigMap in the controller cluster")
}

// TestTakesOwnership test verifies that controller doesn't fail if it finds an existing Template not created by it, and simply takes ownership
func TestTakesOwnership(t *testing.T) {
	f := newFixture(t)
	templateSecret, templateConfigMap, template := provisionControllerResources()
	ownedtemplateSecret, ownedtemplateConfigMap := provisionOwnedControllerResources(templateSecret, templateConfigMap, template)

	rogueTemplate := expectedShardTemplate(template, "")
	rogueTemplate.Spec.DatadogIntegrationSettings.MountDatadogSocket = ptr.Bool(false)

	_, ctx := ktesting.NewTestContext(t)

	f = f.configure(
		&ControllerFixture{
			templateListResults:  []*nexusv1.NexusAlgorithmTemplate{template},
			secretListResults:    []*corev1.Secret{templateSecret},
			configMapListResults: []*corev1.ConfigMap{templateConfigMap},
			existingCoreObjects:  []runtime.Object{templateSecret, templateConfigMap},
			existingNexusObjects: []runtime.Object{template},
		},
		&NexusFixture{
			templateListResults:  []*nexusv1.NexusAlgorithmTemplate{rogueTemplate},
			existingNexusObjects: []runtime.Object{rogueTemplate},
		},
	)

	f.expectControllerUpdateTemplateStatusAction(expectedTemplate(template, nil, nil, nil, []metav1.Condition{
		*nexusv1.NewResourceReadyCondition(
			metav1.Now(),
			metav1.ConditionFalse,
			"Algorithm \"test\" initializing",
		)}))
	f.expectControllerUpdateTemplateStatusAction(expectedTemplate(template, templateSecret, templateConfigMap, []string{"shard0"}, []metav1.Condition{
		*nexusv1.NewResourceReadyCondition(
			metav1.Now(),
			metav1.ConditionTrue,
			"Algorithm \"test\" ready",
		),
	}))
	f.expectedControllerUpdateActions(template, ownedtemplateSecret, ownedtemplateConfigMap, false)
	f.expectShardActions(
		expectedShardTemplate(template, ""),
		expectedShardSecret(templateSecret, []*nexusv1.NexusAlgorithmTemplate{expectedShardTemplate(template, "")}),
		expectedShardConfigMap(templateConfigMap, []*nexusv1.NexusAlgorithmTemplate{expectedShardTemplate(template, "")}),
		true)

	f.run(ctx, []cache.ObjectName{getRef(template)}, []cache.ObjectName{}, false)
	t.Log("Controller successfully took ownership of a NexusAlgorithmTemplate and related secrets and configurations on the shard cluster")
}

// TestDeletesTemplate tests that Template removal from controller cluster will propagate to shard clusters
func TestDeletesTemplate(t *testing.T) {
	f := newFixture(t)
	templateSecret := newSecret("test-secret", nil)
	templateConfigMap := newConfigMap("test-config", nil)

	template := newTemplate("test", templateSecret, templateConfigMap, false, &nexusv1.NexusAlgorithmStatus{
		SyncedSecrets:        []string{"test-secret"},
		SyncedConfigurations: []string{"test-config"},
		SyncedToClusters:     []string{"shard0"},
		Conditions: []metav1.Condition{
			*nexusv1.NewResourceReadyCondition(
				metav1.Now(),
				metav1.ConditionTrue,
				"Algorithm \"test\" ready",
			),
		},
	})

	templateOnShard := newTemplate("test", templateSecret, templateConfigMap, true, nil)
	templateSecretOnShard := newSecret("test-secret", templateOnShard)
	templateConfigMapOnShard := newConfigMap("test-config", templateOnShard)

	_, ctx := ktesting.NewTestContext(t)

	f = f.configure(
		// controller lister returns a new secret and a new configmap
		&ControllerFixture{
			templateListResults:  []*nexusv1.NexusAlgorithmTemplate{template},
			secretListResults:    []*corev1.Secret{templateSecret},
			configMapListResults: []*corev1.ConfigMap{templateConfigMap},
			existingCoreObjects:  []runtime.Object{templateSecret, templateConfigMap},
			existingNexusObjects: []runtime.Object{template},
		},
		&NexusFixture{
			templateListResults:  []*nexusv1.NexusAlgorithmTemplate{templateOnShard},
			secretListResults:    []*corev1.Secret{templateSecretOnShard},
			configMapListResults: []*corev1.ConfigMap{templateConfigMapOnShard},
			existingCoreObjects:  []runtime.Object{templateSecretOnShard, templateConfigMapOnShard},
			existingNexusObjects: []runtime.Object{templateOnShard},
		},
	)

	// deletion of a Template must be triggered on the shard
	f.expectedDeleteActions(templateOnShard)

	f.runObjectHandler(ctx, []interface{}{template}, true)
	t.Log("Controller successfully deleted a Template for the shard after it was deleted from the controller cluster")
}

// TestCreatesWorkgroup test that workgroup creation results in a correct status update event for the main resource and correct resource creations in the shard cluster
func TestCreatesWorkgroup(t *testing.T) {
	f := newFixture(t)
	workgroup := newWorkgroup("test-workgroup", false, "shard0", nil)

	_, ctx := ktesting.NewTestContext(t)

	f = f.configure(
		&ControllerFixture{
			templateListResults:  []*nexusv1.NexusAlgorithmTemplate{},
			workgroupListResults: []*nexusv1.NexusAlgorithmWorkgroup{workgroup},

			secretListResults:    []*corev1.Secret{},
			configMapListResults: []*corev1.ConfigMap{},
			existingCoreObjects:  []runtime.Object{},
			existingNexusObjects: []runtime.Object{workgroup},
		},
		&NexusFixture{},
	)

	f.expectControllerUpdateWorkgroupStatusAction(expectedWorkgroup(workgroup, []metav1.Condition{
		*nexusv1.NewResourceReadyCondition(
			metav1.Now(),
			metav1.ConditionFalse,
			"Workgroup \"test-workgroup\" initializing",
		),
	}))
	f.expectControllerUpdateWorkgroupStatusAction(expectedWorkgroup(workgroup, []metav1.Condition{
		*nexusv1.NewResourceReadyCondition(
			metav1.Now(),
			metav1.ConditionTrue,
			"Workgroup \"test-workgroup\" ready",
		),
	}))

	f.expectShardWorkgroupActions(expectedShardWorkgroup(workgroup, ""), false)

	f.run(ctx, []cache.ObjectName{}, []cache.ObjectName{getWorkgroupRef(workgroup)}, false)
	t.Log("Controller successfully created a new NexusAlgorithmWorkgroup on the shard cluster")
}

// TestUpdatesWorkgroup test that workgroup update is handled correctly in connected shards
func TestUpdatesWorkgroup(t *testing.T) {
	f := newFixture(t)
	workgroup := newWorkgroup("test-workgroup", false, "shard0", nil)
	workgroupOnShard := newWorkgroup("test-workgroup", true, "shard0", nil)
	workgroupUpdated := workgroup.DeepCopy()
	workgroupUpdated.Spec.Tolerations = []corev1.Toleration{
		{
			Key:      "key",
			Operator: corev1.TolerationOpExists,
		},
	}
	workgroupOnShardUpdated := workgroupOnShard.DeepCopy()
	workgroupOnShardUpdated.Spec.Tolerations = workgroupUpdated.Spec.Tolerations

	_, ctx := ktesting.NewTestContext(t)

	f = f.configure(
		&ControllerFixture{
			templateListResults:  []*nexusv1.NexusAlgorithmTemplate{},
			workgroupListResults: []*nexusv1.NexusAlgorithmWorkgroup{workgroupUpdated},

			secretListResults:    []*corev1.Secret{},
			configMapListResults: []*corev1.ConfigMap{},
			existingCoreObjects:  []runtime.Object{},
			existingNexusObjects: []runtime.Object{workgroupUpdated},
		},
		&NexusFixture{
			workgroupListResults: []*nexusv1.NexusAlgorithmWorkgroup{workgroupOnShard},
			existingNexusObjects: []runtime.Object{workgroupOnShard},
		},
	)

	f.expectControllerUpdateWorkgroupStatusAction(expectedWorkgroup(workgroupUpdated, []metav1.Condition{
		*nexusv1.NewResourceReadyCondition(
			metav1.Now(),
			metav1.ConditionFalse,
			"Workgroup \"test-workgroup\" initializing",
		),
	}))
	f.expectControllerUpdateWorkgroupStatusAction(expectedWorkgroup(workgroupUpdated, []metav1.Condition{
		*nexusv1.NewResourceReadyCondition(
			metav1.Now(),
			metav1.ConditionTrue,
			"Workgroup \"test-workgroup\" ready",
		),
	}))

	f.expectShardWorkgroupActions(expectedShardWorkgroup(workgroupOnShardUpdated, "test-workgroup"), true)

	f.run(ctx, []cache.ObjectName{}, []cache.ObjectName{getWorkgroupRef(workgroup)}, false)
	t.Log("Controller successfully created a new NexusAlgorithmWorkgroup on the shard cluster")
}
