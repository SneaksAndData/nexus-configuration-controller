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
	"fmt"
	"github.com/DataDog/datadog-go/v5/statsd"
	v1 "github.com/SneaksAndData/nexus-core/pkg/apis/science/v1"
	"github.com/SneaksAndData/nexus-core/pkg/generated/clientset/versioned/scheme"
	"github.com/SneaksAndData/nexus-core/pkg/shards"
	"github.com/SneaksAndData/nexus-core/pkg/telemetry"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"reflect"
	"time"

	clientset "github.com/SneaksAndData/nexus-core/pkg/generated/clientset/versioned"
	nexusscheme "github.com/SneaksAndData/nexus-core/pkg/generated/clientset/versioned/scheme"
	nexusinformers "github.com/SneaksAndData/nexus-core/pkg/generated/informers/externalversions/science/v1"
	nexuslisters "github.com/SneaksAndData/nexus-core/pkg/generated/listers/science/v1"
)

const (
	// ReconcileLatencyMetric name for statsd
	ReconcileLatencyMetric = "reconcile_latency"

	// WorkqueueLengthMetric name for statsd
	WorkqueueLengthMetric = "workqueue_length"
)

const controllerAgentName = "nexus-configuration-controller"

const (
	// SuccessSynced is used as part of the Event 'reason' when a NexusAlgorithmTemplate is synced
	SuccessSynced = "Synced"
	// ErrResourceExists is used as part of the Event 'reason' when a NexusAlgorithmTemplate fails
	// to sync due to one of: Template CR, Secret owned by Template CR, ConfigMap owned by Template CR of the same name already existing.
	ErrResourceExists = "ErrResourceExists"
	// ErrResourceMissing is used as part of the Event 'reason' when a NexusAlgorithmTemplate fails
	// to sync due to a Secret or a ConfigMap referenced by it is missing from the controller cluster
	ErrResourceMissing = "ErrResourceMissing"
	// ErrResourceSyncError is used when a secret/configmap fails to sync with a fatal exception
	ErrResourceSyncError = "ErrResourceSyncError"

	// MessageResourceExists is the message used for Events when a resource
	// fails to sync due to one of: Template CR, Secret owned by Template CR, ConfigMap owned by Template CR already existing
	MessageResourceExists = "Resource %q already exists and is not managed by any Machine Learning Algorithm"
	// MessageResourceSynced is the message used for an Event fired when a NexusAlgorithmTemplate
	// is synced successfully
	MessageResourceSynced = "Machine Learning Algorithm synced successfully"
	// MessageResourceMissing is the message used for an Event fired when a NexusAlgorithmTemplate references a missing Secret or a ConfigMap
	MessageResourceMissing = "Resource %q referenced by NexusAlgorithmTemplate %q is missing in the controller cluster"
	// MessageResourceOperationFailed is the message used for an Event fired in case of fatal exceptions occurring during Secret/Configmap sync
	MessageResourceOperationFailed = "Synchronization/update of a resource %q referenced by NexusAlgorithmTemplate %q failed with a fatal error %s"
	// FieldManager distinguishes this controller from other things writing to API objects
	FieldManager = controllerAgentName
)

// Controller is the controller implementation for NexusAlgorithmTemplate resources
type Controller struct {
	// controllerKubeClientSet is a standard kubernetes clientset, for the cluster where controller is deployed
	controllerKubeClientSet kubernetes.Interface
	// controllerNexusClientSet is a clientset for Machine Learning Algorithm API group, for the cluster where controller is deployed
	controllerNexusClientSet clientset.Interface

	nexusShards []*shards.Shard

	// secretLister is a Secret lister in the cluster where controller is deployed
	secretLister  corelisters.SecretLister
	secretsSynced cache.InformerSynced

	// configMapLister is a ConfigMap lister in the cluster where controller is deployed
	configMapLister  corelisters.ConfigMapLister
	configMapsSynced cache.InformerSynced

	// templateLister is a NexusAlgorithmTemplate lister in the cluster where controller is deployed
	templateLister nexuslisters.NexusAlgorithmTemplateLister
	templateSynced cache.InformerSynced

	// workqueue is a rate limited work queue. This is used to queue work to be
	// processed instead of performing it as soon as a change happens. This
	// means we can ensure we only process a fixed amount of resources at a
	// time, and makes it easy to ensure we are never processing the same item
	// simultaneously in two different workers.
	workQueue workqueue.TypedRateLimitingInterface[cache.ObjectName]
	// recorder is an event recorder for recording Event resources to the
	// Kubernetes API.
	recorder record.EventRecorder
}

// enqueueResource takes a NexusAlgorithmTemplate resource and converts it into a namespace/name
// string which is then put onto the work queue.
func (c *Controller) enqueueResource(obj interface{}) {
	switch ot := obj.(type) {
	case *v1.NexusAlgorithmTemplate, *v1.NexusAlgorithmWorkgroup:
		if objectRef, err := cache.ObjectToName(obj); err != nil {
			utilruntime.HandleError(err)
			return
		} else {
			c.workQueue.Add(objectRef)
		}
	default:
		utilruntime.HandleError(fmt.Errorf("unsupported type passed into work queue: %s", ot))
		return
	}
}

// handleObject will take any resource implementing metav1.Object and attempt
// to find the NexusAlgorithmTemplate resource that 'owns' it. It does this by looking at the
// objects metadata.ownerReferences field for an appropriate OwnerReference.
// It then enqueues that NexusAlgorithmTemplate resource to be processed. If the object does not
// have an appropriate OwnerReference, it will simply be skipped.
func (c *Controller) handleObject(obj interface{}) {
	var object metav1.Object
	var ok bool
	logger := klog.FromContext(context.Background())

	// attempt to read the object metadata
	if object, ok = obj.(metav1.Object); !ok {
		// check if object was deleted while we were not watching by attempting to get its tombstone info
		tombstone, deleted := obj.(cache.DeletedFinalStateUnknown)
		if !deleted {
			// If the object value is not too big and does not contain sensitive information then
			// it may be useful to include it.
			utilruntime.HandleErrorWithContext(context.Background(), nil, "Error decoding object, invalid type", "type", fmt.Sprintf("%T", obj))
			return
		}
		// recover object data from the tombstone
		object, ok = tombstone.Obj.(metav1.Object)
		if !ok {
			// If the object value is not too big and does not contain sensitive information then
			// it may be useful to include it.
			utilruntime.HandleErrorWithContext(context.Background(), nil, "Error decoding object tombstone, invalid type", "type", fmt.Sprintf("%T", tombstone.Obj))
			return
		}
		logger.V(4).Info("Recovered deleted object", "resourceName", object.GetName())
	}

	// TODO: Unclear delete case here - improvement needed
	switch object := object.(type) {
	case *v1.NexusAlgorithmTemplate:
		logger.V(4).Info("Algorithm template resource deleted, removing it from shards", "template", klog.KObj(object))
		for _, shard := range c.nexusShards {
			deleteErr := shard.DeleteTemplate(object)
			if deleteErr != nil {
				utilruntime.HandleErrorWithContext(context.Background(), nil, "Error deleting Template from a connected shard", "shard", shard.Name)
				return
			}
		}
	default:
		logger.V(4).Info("Processing object", "object", klog.KObj(object))
		if objRefs := object.GetOwnerReferences(); len(objRefs) > 0 {
			for _, ownerRef := range objRefs {
				if ownerRef.Kind != "NexusAlgorithmTemplate" {
					continue
				}

				template, err := c.templateLister.NexusAlgorithmTemplates(object.GetNamespace()).Get(ownerRef.Name)
				if err != nil {
					logger.V(4).Info("Ignore orphaned object", "object", klog.KObj(object), "template", ownerRef.Name)
					return
				}

				c.enqueueResource(template)
			}
		}
	}
}

// NewController returns a new nexus-configuration-controller
func NewController(
	ctx context.Context,
	controllerNamespace string,
	controllerKubeClientSet kubernetes.Interface,
	controllerNexusClientSet clientset.Interface,

	connectedShards []*shards.Shard,

	controllerSecretInformer coreinformers.SecretInformer,
	controllerConfigmapInformer coreinformers.ConfigMapInformer,
	controllerTemplateInformer nexusinformers.NexusAlgorithmTemplateInformer,

	failureRateBaseDelay time.Duration,
	failureRateMaxDelay time.Duration,
	rateLimitElementsPerSecond int,
	rateLimitElementsBurst int) (*Controller, error) {
	logger := klog.FromContext(ctx)

	// Create event broadcaster
	// Add nexus-configuration-controller types to the default Kubernetes Scheme so Events can be
	// logged for nexus-configuration-controller types.
	utilruntime.Must(nexusscheme.AddToScheme(scheme.Scheme))
	logger.V(4).Info("Creating event broadcaster")

	eventBroadcaster := record.NewBroadcaster(record.WithContext(ctx))
	eventBroadcaster.StartStructuredLogging(0)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: controllerKubeClientSet.CoreV1().Events(controllerNamespace)})

	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerAgentName})
	ratelimiter := workqueue.NewTypedMaxOfRateLimiter(
		workqueue.NewTypedItemExponentialFailureRateLimiter[cache.ObjectName](failureRateBaseDelay, failureRateMaxDelay),
		&workqueue.TypedBucketRateLimiter[cache.ObjectName]{Limiter: rate.NewLimiter(rate.Limit(rateLimitElementsPerSecond), rateLimitElementsBurst)},
	)

	controller := &Controller{
		controllerKubeClientSet:  controllerKubeClientSet,
		controllerNexusClientSet: controllerNexusClientSet,

		nexusShards: connectedShards,

		secretLister:  controllerSecretInformer.Lister(),
		secretsSynced: controllerSecretInformer.Informer().HasSynced,

		configMapLister:  controllerConfigmapInformer.Lister(),
		configMapsSynced: controllerConfigmapInformer.Informer().HasSynced,

		templateLister: controllerTemplateInformer.Lister(),
		templateSynced: controllerTemplateInformer.Informer().HasSynced,
		workQueue:      workqueue.NewTypedRateLimitingQueue(ratelimiter),
		recorder:       recorder,
	}

	logger.Info("Setting up event handlers")
	// Set up an event handler for when Machine Learning Algorithm resources change
	_, handlerErr := controllerTemplateInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueueResource,
		UpdateFunc: func(old, new interface{}) {
			controller.enqueueResource(new)
		},
		DeleteFunc: controller.handleObject,
	})

	if handlerErr != nil {
		return nil, handlerErr
	}

	// This way, we don't need to implement custom logic for handling Secret/ConfigMap resources.
	// More info on this pattern:
	// https://github.com/kubernetes/community/blob/8cafef897a22026d42f5e5bb3f104febe7e29830/contributors/devel/controllers.md

	// Set up an event handler for when Secret resources change. This
	// handler will lookup the owner of the given Secret, and if it is
	// owned by a NexusAlgorithmTemplate resource then the handler will enqueue that
	// NexusAlgorithmTemplate resource for processing.
	_, handlerErr = controllerSecretInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handleObject,
		UpdateFunc: func(old, new interface{}) {
			newSecret := new.(*corev1.Secret)
			oldSecret := old.(*corev1.Secret)
			if newSecret.ResourceVersion == oldSecret.ResourceVersion {
				// Periodic resync will send update events for all known Secrets.
				// Two different versions of the same Secret will always have different RVs.
				return
			}
			controller.handleObject(new)
		},
		DeleteFunc: controller.handleObject,
	})

	if handlerErr != nil {
		return nil, handlerErr
	}

	// Set up an event handler for when ConfigMap resources change. This
	// handler will lookup the owner of the given ConfigMap, and if it is
	// owned by a NexusAlgorithmTemplate resource then the handler will enqueue that
	// NexusAlgorithmTemplate resource for processing.
	_, handlerErr = controllerConfigmapInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handleObject,
		UpdateFunc: func(old, new interface{}) {
			newConfigMap := new.(*corev1.ConfigMap)
			oldConfigMap := old.(*corev1.ConfigMap)
			if newConfigMap.ResourceVersion == oldConfigMap.ResourceVersion {
				// Periodic resync will send update events for all known ConfigMaps.
				// Two different versions of the same ConfigMap will always have different RVs.
				return
			}
			controller.handleObject(new)
		},
		DeleteFunc: controller.handleObject,
	})

	if handlerErr != nil {
		return nil, handlerErr
	}

	return controller, nil
}

// runWorker is a long-running function that will continually call the
// processNextWorkItem function in order to read and process a message on the work queue
func (c *Controller) runWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

// processNextWorkItem will read a single work item from the workqueue and
// attempt to process it, by calling the syncHandler.
func (c *Controller) processNextWorkItem(ctx context.Context) bool { // coverage-ignore
	objRef, shutdown := c.workQueue.Get()
	metrics := ctx.Value(telemetry.MetricsClientContextKey).(*statsd.Client)
	itemProcessStart := time.Now()

	if shutdown {
		return false
	}

	// We call Done at the end of this func so the workqueue knows we have
	// finished processing this item. We also must remember to call Forget
	// if we do not want this work item being re-queued. For example, we do
	// not call Forget if a transient error occurs, instead the item is
	// put back on the workqueue and attempted again after a back-off
	// period.
	defer c.workQueue.Done(objRef)
	defer telemetry.GaugeDuration(metrics, ReconcileLatencyMetric, itemProcessStart, map[string]string{}, 1)
	defer telemetry.Gauge(metrics, WorkqueueLengthMetric, float64(c.workQueue.Len()), map[string]string{}, 1)

	// Run the syncHandler, passing it the structured reference to the object to be synced.
	err := c.syncHandler(ctx, objRef)
	if err == nil {
		// If no error occurs then we Forget this item so it does not
		// get queued again until another change happens.
		c.workQueue.Forget(objRef)
		return true
	}
	// there was a failure so be sure to report it.  This method allows for
	// pluggable error handling which can be used for things like
	// cluster-monitoring.
	utilruntime.HandleErrorWithContext(ctx, err, "Error syncing; requeuing for later retry", "objectReference", objRef)
	// since we failed, we should requeue the item to work on later.  This
	// method will add a backoff to avoid hotlooping on particular items
	// (they're probably still not going to work right away) and overall
	// controller protection (everything I've done is broken, this controller
	// needs to calm down or it can starve other useful work) cases.
	c.workQueue.AddRateLimited(objRef)
	return true
}

func (c *Controller) reportTemplateInitCondition(template *v1.NexusAlgorithmTemplate) (*v1.NexusAlgorithmTemplate, error) {
	// NEVER modify objects from the store. It's a read-only, local cache.
	templateCopy := template.DeepCopy()
	// init condition is only assigned to new resources
	if len(templateCopy.Status.Conditions) == 0 {
		templateCopy.Status.Conditions = []metav1.Condition{*v1.NewResourceReadyCondition(metav1.Now(), metav1.ConditionFalse, fmt.Sprintf("Algorithm %q initializing", template.Name))}
		return c.controllerNexusClientSet.ScienceV1().NexusAlgorithmTemplates(template.Namespace).UpdateStatus(context.TODO(), templateCopy, metav1.UpdateOptions{FieldManager: FieldManager})
	}
	return template, nil
}

func (c *Controller) reportTemplateSyncedCondition(template *v1.NexusAlgorithmTemplate, updatedSecrets []string, updatedConfigMaps []string, shards []string) (*v1.NexusAlgorithmTemplate, error) {
	// NEVER modify objects from the store. It's a read-only, local cache.
	templateCopy := template.DeepCopy()
	// update conditions if changed
	// later if multiple conditions are introduced this should compare possible sets of conditions to one another
	// set time to prev instance first so DeepEqual can be used
	newCondition := *v1.NewResourceReadyCondition(templateCopy.Status.Conditions[0].LastTransitionTime, metav1.ConditionTrue, fmt.Sprintf("Algorithm %q ready", template.Name))
	templateCopy.Status.Conditions[0] = newCondition
	templateCopy.Status.SyncedSecrets = updatedSecrets
	templateCopy.Status.SyncedConfigurations = updatedConfigMaps
	templateCopy.Status.SyncedToClusters = shards
	if !reflect.DeepEqual(template.Status, templateCopy.Status) {
		templateCopy.Status.Conditions[0].LastTransitionTime = metav1.Now()
		return c.controllerNexusClientSet.ScienceV1().NexusAlgorithmTemplates(template.Namespace).UpdateStatus(context.TODO(), templateCopy, metav1.UpdateOptions{FieldManager: FieldManager})
	}

	return template, nil
}

// isMissingOwnership checks if the resource is controlled by this NexusAlgorithmTemplate resource,
// and if not AND the resource is not owned by any other NexusAlgorithmTemplate, logs a warning to the event recorder and returns error msg.
func (c *Controller) isMissingOwnership(obj metav1.Object, owner metav1.Object) (bool, error) {
	// if already controlled, no error
	if objRefs := obj.GetOwnerReferences(); len(objRefs) > 0 {
		// check if we own this object
		// since secrets and configmaps can be referenced by multiple templates, we need to find `owner` there
		for _, ownerRef := range obj.GetOwnerReferences() {
			if ownerRef.Kind == "NexusAlgorithmTemplate" && ownerRef.UID == owner.GetUID() {
				return false, nil
			}
		}
	} else {
		// rogue resource not owned by any NexusAlgorithmTemplate - report error
		msg := fmt.Sprintf(MessageResourceExists, obj.GetName())
		c.recorder.Event(obj.(runtime.Object), corev1.EventTypeWarning, ErrResourceExists, msg)
		return false, fmt.Errorf("%s", msg)
	}

	return true, nil
}

func (c *Controller) syncSecretsToShard(secretNamespace string, controllerTemplate *v1.NexusAlgorithmTemplate, shardTemplate *v1.NexusAlgorithmTemplate, shard *shards.Shard, logger *klog.Logger) error {
	for _, secretName := range shardTemplate.GetSecretNames() {
		// Get the secret with the name specified in NexusAlgorithmTemplate.spec
		secret, err := c.secretLister.Secrets(secretNamespace).Get(secretName)
		// If the referenced Secret resource doesn't exist in the cluster where the controller is deployed, update the syncErr and move on to the next Secret
		if k8serrors.IsNotFound(err) { // coverage-ignore
			msg := fmt.Sprintf(MessageResourceMissing, secretName, controllerTemplate.Name)
			c.recorder.Event(controllerTemplate, corev1.EventTypeWarning, ErrResourceMissing, msg)
			logger.V(4).Info("Secret not found", "secretName", secretName, "shard", shard.Name)
			return err
		}

		shardSecret, err := shard.SecretLister.Secrets(shardTemplate.Namespace).Get(secret.Name)
		// secret does not exist in this shard, create it
		if k8serrors.IsNotFound(err) {
			shardSecret, err = shard.CreateSecret(shardTemplate, secret, FieldManager)
		}

		// requeue on error
		if err != nil { // coverage-ignore
			msg := fmt.Sprintf(MessageResourceOperationFailed, secretName, controllerTemplate.Name, err)
			c.recorder.Event(controllerTemplate, corev1.EventTypeWarning, ErrResourceSyncError, msg)
			return err
		}

		missingOwner, err := c.isMissingOwnership(shardSecret, shardTemplate)
		// requeue on error
		if err != nil { // coverage-ignore
			msg := fmt.Sprintf(MessageResourceOperationFailed, secretName, controllerTemplate.Name, err)
			c.recorder.Event(controllerTemplate, corev1.EventTypeWarning, ErrResourceSyncError, msg)
			return err
		}

		// if Secret data differs, update the Secret
		// if ownership is missing, update the Secret
		if !reflect.DeepEqual(secret.Data, shardSecret.Data) {
			logger.V(4).Info(fmt.Sprintf("Content changed for Secret %s, updating", secret.Name))
			_, err = shard.UpdateSecret(shardSecret, secret.Data, nil, FieldManager)

			// requeue on error
			if err != nil {
				msg := fmt.Sprintf(MessageResourceOperationFailed, secretName, controllerTemplate.Name, err)
				c.recorder.Event(controllerTemplate, corev1.EventTypeWarning, ErrResourceSyncError, msg)
				return err
			}
		}
		if missingOwner {
			logger.V(4).Info(fmt.Sprintf("Ownership missing for Secret %s, updating", secret.Name))
			_, err = shard.UpdateSecret(shardSecret, nil, shardTemplate, FieldManager)

			// requeue on error
			if err != nil {
				msg := fmt.Sprintf(MessageResourceOperationFailed, secretName, controllerTemplate.Name, err)
				c.recorder.Event(controllerTemplate, corev1.EventTypeWarning, ErrResourceSyncError, msg)
				return err
			}
		}
	}

	return nil
}

func (c *Controller) syncConfigMapsToShard(configMapNamespace string, controllerTemplate *v1.NexusAlgorithmTemplate, shardTemplate *v1.NexusAlgorithmTemplate, shard *shards.Shard, logger *klog.Logger) error {
	for _, configMapName := range shardTemplate.GetConfigMapNames() {
		// Get the ConfigMap with the name specified in NexusAlgorithmTemplate.spec
		configMap, err := c.configMapLister.ConfigMaps(configMapNamespace).Get(configMapName)
		// If the referenced ConfigMap resource doesn't exist in the cluster where the controller is deployed, update syncErr and move on to the next ConfigMap
		if k8serrors.IsNotFound(err) { // coverage-ignore
			msg := fmt.Sprintf(MessageResourceMissing, configMapName, controllerTemplate.Name)
			c.recorder.Event(controllerTemplate, corev1.EventTypeWarning, ErrResourceMissing, msg)
			logger.V(4).Info("ConfigMap not found", "configMapName", configMapName, "shard", shard.Name)
			return err
		}

		shardConfigMap, err := shard.ConfigMapLister.ConfigMaps(shardTemplate.Namespace).Get(configMap.Name)
		// secret does not exist in this shard, create it
		if k8serrors.IsNotFound(err) { // coverage-ignore
			shardConfigMap, err = shard.CreateConfigMap(shardTemplate, configMap, FieldManager)
		}

		// requeue on error
		if err != nil { // coverage-ignore
			msg := fmt.Sprintf(MessageResourceOperationFailed, configMapName, controllerTemplate.Name, err)
			c.recorder.Event(controllerTemplate, corev1.EventTypeWarning, ErrResourceSyncError, msg)
			return err
		}

		missingOwner, err := c.isMissingOwnership(shardConfigMap, shardTemplate)
		// requeue on error
		if err != nil { // coverage-ignore
			msg := fmt.Sprintf(MessageResourceOperationFailed, configMapName, controllerTemplate.Name, err)
			c.recorder.Event(controllerTemplate, corev1.EventTypeWarning, ErrResourceSyncError, msg)
			return err
		}

		// if data differs, update
		if !reflect.DeepEqual(configMap.Data, shardConfigMap.Data) {
			logger.V(4).Info(fmt.Sprintf("Content changed for ConfigMap %s, updating", configMap.Name))
			_, err = shard.UpdateConfigMap(shardConfigMap, configMap.Data, nil, FieldManager)

			// requeue on error
			if err != nil {
				msg := fmt.Sprintf(MessageResourceOperationFailed, configMapName, controllerTemplate.Name, err)
				c.recorder.Event(controllerTemplate, corev1.EventTypeWarning, ErrResourceSyncError, msg)
				return err
			}
		}
		// if ownership is not set yet, update it
		if missingOwner {
			logger.V(4).Info(fmt.Sprintf("Ownership missing for ConfigMap %s, updating", configMap.Name))
			_, err = shard.UpdateConfigMap(shardConfigMap, nil, shardTemplate, FieldManager)

			// requeue on error
			if err != nil {
				msg := fmt.Sprintf(MessageResourceOperationFailed, configMapName, controllerTemplate.Name, err)
				c.recorder.Event(controllerTemplate, corev1.EventTypeWarning, ErrResourceSyncError, msg)
				return err
			}
		}
	}

	return nil
}

// shardNames returns names of all shards available for sync
func (c *Controller) shardNames() []string {
	result := make([]string, 0, len(c.nexusShards))
	for _, shard := range c.nexusShards {
		result = append(result, shard.Name)
	}
	return result
}

func (c *Controller) isOwnedBy(obj metav1.ObjectMeta, controllerTemplate *v1.NexusAlgorithmTemplate) bool {
	for _, ownerRef := range obj.OwnerReferences {
		if ownerRef.UID == controllerTemplate.UID {
			return true
		}
	}

	return false
}

func (c *Controller) adoptReferences(template *v1.NexusAlgorithmTemplate) error {
	for _, secretName := range template.GetSecretNames() {
		referencedSecret, err := c.secretLister.Secrets(template.Namespace).Get(secretName)
		if err != nil {
			c.recorder.Event(template, corev1.EventTypeWarning, ErrResourceMissing, fmt.Sprintf(MessageResourceMissing, secretName, template.Name))
			return err
		}
		refCopy := referencedSecret.DeepCopy()
		if !c.isOwnedBy(referencedSecret.ObjectMeta, template) {
			refCopy.OwnerReferences = append(refCopy.OwnerReferences, metav1.OwnerReference{
				APIVersion: v1.SchemeGroupVersion.String(),
				Kind:       "NexusAlgorithmTemplate",
				Name:       template.Name,
				UID:        template.UID,
			})

			_, err := c.controllerKubeClientSet.CoreV1().Secrets(template.Namespace).Update(context.TODO(), refCopy, metav1.UpdateOptions{})
			if err != nil {
				c.recorder.Event(template, corev1.EventTypeWarning, ErrResourceSyncError, fmt.Sprintf(MessageResourceOperationFailed, secretName, template.Name, err))
				return err
			}
		}
	}

	for _, configMapName := range template.GetConfigMapNames() {
		referencedConfigMap, err := c.configMapLister.ConfigMaps(template.Namespace).Get(configMapName)
		if err != nil {
			c.recorder.Event(template, corev1.EventTypeWarning, ErrResourceMissing, fmt.Sprintf(MessageResourceMissing, configMapName, template.Name))
			return err
		}
		refCopy := referencedConfigMap.DeepCopy()
		if !c.isOwnedBy(referencedConfigMap.ObjectMeta, template) {
			refCopy.OwnerReferences = append(refCopy.OwnerReferences, metav1.OwnerReference{
				APIVersion: v1.SchemeGroupVersion.String(),
				Kind:       "NexusAlgorithmTemplate",
				Name:       template.Name,
				UID:        template.UID,
			})

			_, err := c.controllerKubeClientSet.CoreV1().ConfigMaps(template.Namespace).Update(context.TODO(), refCopy, metav1.UpdateOptions{})
			if err != nil {
				c.recorder.Event(template, corev1.EventTypeWarning, ErrResourceSyncError, fmt.Sprintf(MessageResourceOperationFailed, configMapName, template.Name, err))
				return err
			}
		}
	}

	return nil
}

// syncHandler compares the actual state with the desired, and attempts to
// converge the two. It then updates the Status block of the NexusAlgorithmTemplate resource
// with the current status of the resource.
func (c *Controller) syncHandler(ctx context.Context, objectRef cache.ObjectName) error {
	logger := klog.LoggerWithValues(klog.FromContext(ctx), "objectRef", objectRef)

	// Get the NexusAlgorithmTemplate resource with this namespace/name
	logger.V(4).Info(fmt.Sprintf("Syncing algorithm %s", objectRef.Name))
	template, err := c.templateLister.NexusAlgorithmTemplates(objectRef.Namespace).Get(objectRef.Name)
	if err != nil {
		// The NexusAlgorithmTemplate resource may no longer exist, in which case we stop
		// processing.
		if k8serrors.IsNotFound(err) {
			utilruntime.HandleErrorWithContext(ctx, err, "NexusAlgorithmTemplate referenced by item in work queue no longer exists", "objectReference", objectRef)
			return nil
		}

		return err
	}

	template, err = c.reportTemplateInitCondition(template)
	// requeue in case status update fails
	if err != nil {
		return err
	}

	err = c.adoptReferences(template)
	// requeue in case we can't take ownership of referenced secrets/configs
	if err != nil {
		logger.V(4).Error(err, fmt.Sprintf("Invalid machine learning algorithm resource: %s", template.Name))
		return err
	}

	for _, shard := range c.nexusShards {
		logger.V(4).Info(fmt.Sprintf("Syncing to shard %s", shard.Name))
		shardTemplate, shardErr := shard.TemplateLister.NexusAlgorithmTemplates(objectRef.Namespace).Get(objectRef.Name)

		// update this Template in case it exists and has drifted
		if shardErr == nil && !reflect.DeepEqual(shardTemplate.Spec, template.Spec) {
			logger.V(4).Info(fmt.Sprintf("Content changed for NexusAlgorithmTemplate %s, updating", template.Name))
			shardTemplate, shardErr = shard.UpdateTemplate(shardTemplate, template.Spec, FieldManager)
			// requeue on error
			if shardErr != nil {
				return shardErr
			}
		}

		// if NexusAlgorithmTemplate has not been created yet, create a new one in this shard
		if k8serrors.IsNotFound(shardErr) {
			logger.V(4).Info(fmt.Sprintf("Algorithm %s not found in shard %s, creating", objectRef.Name, shard.Name))
			shardTemplate, shardErr = shard.CreateTemplate(template.Name, template.Namespace, template.Spec, FieldManager)
		}

		// requeue on error
		if shardErr != nil {
			logger.V(4).Error(shardErr, fmt.Sprintf("Error processing algorithm resource on shard %s", shard.Name))
			return shardErr
		}

		logger.V(4).Info(fmt.Sprintf("Syncing secrets to shard %s", shard.Name))
		shardErr = c.syncSecretsToShard(template.Namespace, template, shardTemplate, shard, &logger)
		// requeue on error
		if shardErr != nil {
			logger.V(4).Error(shardErr, fmt.Sprintf("Error syncing secrets on shard %s", shard.Name))
			return shardErr
		}

		logger.V(4).Info(fmt.Sprintf("Syncing configmaps to shard %s", shard.Name))
		shardErr = c.syncConfigMapsToShard(template.Namespace, template, shardTemplate, shard, &logger)
		// requeue on error
		if shardErr != nil {
			logger.V(4).Error(shardErr, fmt.Sprintf("Error syncing configMaps on shard %s", shard.Name))
			return shardErr
		}
	}

	// Finally, we update the status block of the NexusAlgorithmTemplate resource in the controller cluster to reflect the
	// current state of the world across all Shards

	logger.V(4).Info(fmt.Sprintf("Processed all shards, updating status for %s", template.Name))
	template, err = c.reportTemplateSyncedCondition(template, template.GetSecretNames(), template.GetConfigMapNames(), c.shardNames())
	if err != nil {
		logger.V(4).Error(err, "Error setting ready status condition")
		return err
	}

	c.recorder.Event(template, corev1.EventTypeNormal, SuccessSynced, MessageResourceSynced)
	return nil
}

// Run will set up the event handlers for types we are interested in, as well
// as syncing informer caches and starting workers. It will block until stopCh
// is closed, at which point it will shutdown the workqueue and wait for
// workers to finish processing their current work items.
func (c *Controller) Run(ctx context.Context, workers int) error { // coverage-ignore
	defer utilruntime.HandleCrash()
	defer c.workQueue.ShutDown()
	logger := klog.FromContext(ctx)

	// Start the informer factories to begin populating the informer caches
	logger.Info("Starting NexusAlgorithmTemplate controller")

	// Wait for the caches to be synced before starting workers
	logger.Info("Waiting for informer caches to sync")

	if ok := cache.WaitForCacheSync(ctx.Done(), c.secretsSynced, c.configMapsSynced, c.templateSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}
	logger.Info("Controller informers synced")
	for _, shard := range c.nexusShards {
		if ok := cache.WaitForCacheSync(ctx.Done(), shard.SecretsSynced, shard.ConfigMapsSynced, shard.TemplateSynced); !ok {
			return fmt.Errorf("failed to wait for shard %s caches to sync", shard.Name)
		}
	}
	logger.Info("Shard informers synced")

	logger.Info("Starting workers", "count", workers)
	// Launch workers to process NexusAlgorithmTemplate resources
	for i := 0; i < workers; i++ {
		go wait.UntilWithContext(ctx, c.runWorker, time.Second)
	}

	logger.Info("Started workers")
	<-ctx.Done()
	logger.Info("Shutting down workers")

	return nil
}
