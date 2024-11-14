/*
 * Copyright (c) 2024. ECCO Data & AI Open-Source Project Maintainers.
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
	"errors"
	"fmt"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	v1 "science.sneaksanddata.com/nexus-configuration-controller/pkg/apis/science/v1"
	"science.sneaksanddata.com/nexus-configuration-controller/pkg/generated/clientset/versioned/scheme"
	"science.sneaksanddata.com/nexus-configuration-controller/pkg/shards"
	"strings"
	"time"

	clientset "science.sneaksanddata.com/nexus-configuration-controller/pkg/generated/clientset/versioned"
	nexusscheme "science.sneaksanddata.com/nexus-configuration-controller/pkg/generated/clientset/versioned/scheme"
	nexusinformers "science.sneaksanddata.com/nexus-configuration-controller/pkg/generated/informers/externalversions/science/v1"
	nexuslisters "science.sneaksanddata.com/nexus-configuration-controller/pkg/generated/listers/science/v1"
)

const controllerAgentName = "nexus-configuration-controller"

const (
	// SuccessSynced is used as part of the Event 'reason' when a MachineLearningAlgorithm is synced
	SuccessSynced = "Synced"
	// ErrResourceExists is used as part of the Event 'reason' when a MachineLearningAlgorithm fails
	// to sync due to one of: MLA CR, Secret owned by MLA CR, ConfigMap owned by MLA CR of the same name already existing.
	ErrResourceExists = "ErrResourceExists"
	// ErrResourceMissing is used as part of the Event 'reason' when a MachineLearningAlgorithm fails
	// to sync due to a Secret or a ConfigMap referenced by it is missing from the controller cluster
	ErrResourceMissing = "ErrResourceMissing"
	// SuccessSkipped is used as part of the Event 'reason' when a MachineLearningAlgorithm is skipped from processing
	SuccessSkipped = "Skipped"

	// MessageResourceExists is the message used for Events when a resource
	// fails to sync due to one of: MLA CR, Secret owned by MLA CR, ConfigMap owned by MLA CR already existing
	MessageResourceExists = "Resource %q already exists and is not managed by any Machine Learning Algorithm"
	// MessageResourceSynced is the message used for an Event fired when a MachineLearningAlgorithm
	// is synced successfully
	MessageResourceSynced = "Machine Learning Algorithm synced successfully"
	// MessageResourceMissing is the message used for an Event fired when a MachineLearningAlgorithm references a missing Secret or a ConfigMap
	MessageResourceMissing = "Resource %q referenced by MachineLearningAlgorithm %q is missing in the controller cluster"
	MessageResourceSkipped = "Resource %q skipped, controller launched in dev mode"
	// FieldManager distinguishes this controller from other things writing to API objects
	FieldManager = controllerAgentName
)

// Controller is the controller implementation for MachineLearningAlgorithm resources
type Controller struct {
	// controllerkubeclientset is a standard kubernetes clientset, for the cluster where controller is deployed
	controllerkubeclientset kubernetes.Interface
	// controllernexusclientset is a clientset for Machine Learning Algorithm API group, for the cluster where controller is deployed
	controllernexusclientset clientset.Interface

	nexusShards []*shards.Shard

	// secretLister is a Secret lister in the cluster where controller is deployed
	secretLister  corelisters.SecretLister
	secretsSynced cache.InformerSynced

	// configMapLister is a ConfigMap lister in the cluster where controller is deployed
	configMapLister  corelisters.ConfigMapLister
	configMapsSynced cache.InformerSynced

	// mlaLister is a MachineLearningAlgorithm lister in the cluster where controller is deployed
	mlaLister nexuslisters.MachineLearningAlgorithmLister
	mlaSynced cache.InformerSynced

	// workqueue is a rate limited work queue. This is used to queue work to be
	// processed instead of performing it as soon as a change happens. This
	// means we can ensure we only process a fixed amount of resources at a
	// time, and makes it easy to ensure we are never processing the same item
	// simultaneously in two different workers.
	workqueue workqueue.TypedRateLimitingInterface[cache.ObjectName]
	// recorder is an event recorder for recording Event resources to the
	// Kubernetes API.
	recorder record.EventRecorder
}

type SyncError struct {
	failedSecretError    error
	failedConfigMapError error
}

func (se *SyncError) isEmpty() bool {
	return se.failedSecretError == nil && se.failedConfigMapError == nil
}

func (se *SyncError) merged() string {
	var sb strings.Builder
	if se.failedSecretError != nil {
		sb.WriteString(se.failedSecretError.Error())
		sb.WriteString("\n")
	}
	if se.failedConfigMapError != nil {
		sb.WriteString(se.failedConfigMapError.Error())
	}
	return sb.String()
}

// enqueueMachineLearningAlgorithm takes a MachineLearningAlgorithm resource and converts it into a namespace/name
// string which is then put onto the work queue.
func (c *Controller) enqueueMachineLearningAlgorithm(obj interface{}) {
	switch ot := obj.(type) {
	case *v1.MachineLearningAlgorithm:
		if objectRef, err := cache.ObjectToName(obj); err != nil {
			utilruntime.HandleError(err)
			return
		} else {
			c.workqueue.Add(objectRef)
		}
	default:
		utilruntime.HandleError(fmt.Errorf("unsupported type passed into work queue: %s", ot))
		return
	}
}

// handleObject will take any resource implementing metav1.Object and attempt
// to find the MachineLearningAlgorithm resource that 'owns' it. It does this by looking at the
// objects metadata.ownerReferences field for an appropriate OwnerReference.
// It then enqueues that MachineLearningAlgorithm resource to be processed. If the object does not
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

	switch ot := object.(type) {
	case *v1.MachineLearningAlgorithm:
		logger.V(4).Info("MLA resource deleted, removing it from shards", "mla", klog.KObj(object))
		for _, shard := range c.nexusShards {
			deleteErr := shard.DeleteMachineLearningAlgorithm(object.(*v1.MachineLearningAlgorithm))
			if deleteErr != nil {
				utilruntime.HandleErrorWithContext(context.Background(), nil, "Error deleting MLA from a connected shard", "type", ot, "shard", shard.Name)
				return
			}
		}
	default:
		logger.V(4).Info("Processing object", "object", klog.KObj(object))
		if ownerRef := metav1.GetControllerOf(object); ownerRef != nil {
			// If this object is not owned by a MachineLearningAlgorithm, skip it
			if ownerRef.Kind != "MachineLearningAlgorithm" {
				return
			}

			mla, err := c.mlaLister.MachineLearningAlgorithms(object.GetNamespace()).Get(ownerRef.Name)
			if err != nil {
				logger.V(4).Info("Ignore orphaned object", "object", klog.KObj(object), "mla", ownerRef.Name)
				return
			}

			c.enqueueMachineLearningAlgorithm(mla)
			return
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
	controllerMlaInformer nexusinformers.MachineLearningAlgorithmInformer,

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
		controllerkubeclientset:  controllerKubeClientSet,
		controllernexusclientset: controllerNexusClientSet,

		nexusShards: connectedShards,

		secretLister:  controllerSecretInformer.Lister(),
		secretsSynced: controllerSecretInformer.Informer().HasSynced,

		configMapLister:  controllerConfigmapInformer.Lister(),
		configMapsSynced: controllerConfigmapInformer.Informer().HasSynced,

		mlaLister: controllerMlaInformer.Lister(),
		mlaSynced: controllerMlaInformer.Informer().HasSynced,
		workqueue: workqueue.NewTypedRateLimitingQueue(ratelimiter),
		recorder:  recorder,
	}

	logger.Info("Setting up event handlers")
	// Set up an event handler for when Machine Learning Algorithm resources change
	_, handlerErr := controllerMlaInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueueMachineLearningAlgorithm,
		UpdateFunc: func(old, new interface{}) {
			controller.enqueueMachineLearningAlgorithm(new)
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
	// owned by a MachineLearningAlgorithm resource then the handler will enqueue that
	// MachineLearningAlgorithm resource for processing.
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
	// owned by a MachineLearningAlgorithm resource then the handler will enqueue that
	// MachineLearningAlgorithm resource for processing.
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
// processNextWorkItem function in order to read and process a message on the
// workqueue.
func (c *Controller) runWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

// processNextWorkItem will read a single work item from the workqueue and
// attempt to process it, by calling the syncHandler.
func (c *Controller) processNextWorkItem(ctx context.Context) bool { // coverage-ignore
	objRef, shutdown := c.workqueue.Get()
	logger := klog.FromContext(ctx)

	if shutdown {
		return false
	}

	// We call Done at the end of this func so the workqueue knows we have
	// finished processing this item. We also must remember to call Forget
	// if we do not want this work item being re-queued. For example, we do
	// not call Forget if a transient error occurs, instead the item is
	// put back on the workqueue and attempted again after a back-off
	// period.
	defer c.workqueue.Done(objRef)

	// Run the syncHandler, passing it the structured reference to the object to be synced.
	err := c.syncHandler(ctx, objRef)
	if err == nil {
		// If no error occurs then we Forget this item so it does not
		// get queued again until another change happens.
		c.workqueue.Forget(objRef)
		logger.Info("Successfully synced", "objectName", objRef)
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
	c.workqueue.AddRateLimited(objRef)
	return true
}

func (c *Controller) updateMachineLearningAlgorithmStatus(mla *v1.MachineLearningAlgorithm, updatedSecrets []string, updatedConfigMaps []string, receivedBy []string, syncErrors map[string]string) error {
	// NEVER modify objects from the store. It's a read-only, local cache.
	mlaCopy := mla.DeepCopy()
	if len(syncErrors) > 0 {
		mlaCopy.Status.State = "Failed"
	} else {
		mlaCopy.Status.State = "Ready"
	}
	mlaCopy.Status.LastUpdatedTimestamp = metav1.Now()
	mlaCopy.Status.SyncedSecrets = updatedSecrets
	mlaCopy.Status.SyncedConfigurations = updatedConfigMaps
	mlaCopy.Status.SyncedToClusters = receivedBy
	mlaCopy.Status.SyncErrors = syncErrors

	_, err := c.controllernexusclientset.ScienceV1().MachineLearningAlgorithms(mla.Namespace).UpdateStatus(context.TODO(), mlaCopy, metav1.UpdateOptions{FieldManager: FieldManager})
	return err
}

// shardNames returns names of all Shards operated by the controller
func (c *Controller) shardNames() []string {
	result := make([]string, 0, len(c.nexusShards))
	for _, shard := range c.nexusShards {
		result = append(result, shard.Name)
	}
	return result
}

// isMissingOwnership checks if the resource is controlled by this MachineLearningAlgorithm resource,
// and if not AND the resource is not owned by any other MachineLearningAlgorithm, logs a warning to the event recorder and returns error msg.
func (c *Controller) isMissingOwnership(obj metav1.Object, owner metav1.Object) (bool, error) {
	// if already controlled, no error
	if metav1.IsControlledBy(obj, owner) {
		return false, nil
	}
	// check if we have any MachineLearningAlgorithm already owning this obj
	// this can be the case if a secret is shard by multiple MachineLearningAlgorithm resources
	for _, ownerRef := range obj.GetOwnerReferences() {
		if ownerRef.Kind == "MachineLearningAlgorithm" {
			return true, nil
		}
	}
	// rogue resource not owned by any MachineLearningAlgorithm - report error
	msg := fmt.Sprintf(MessageResourceExists, obj.GetName())
	c.recorder.Event(owner.(*v1.MachineLearningAlgorithm), corev1.EventTypeWarning, ErrResourceExists, msg)
	return false, fmt.Errorf("%s", msg)
}

func (c *Controller) syncSecretsToShard(secretNamespace string, controllerMla *v1.MachineLearningAlgorithm, shardMla *v1.MachineLearningAlgorithm, shard *shards.Shard, logger *klog.Logger) error {
	var syncErr error = nil
	for _, secretName := range shardMla.GetSecretNames() {
		// Get the secret with the name specified in MachineLearningAlgorithm.spec
		secret, err := c.secretLister.Secrets(secretNamespace).Get(secretName)
		// If the referenced Secret resource doesn't exist in the cluster where the controller is deployed, update the syncErr and move on to the next Secret
		if k8serrors.IsNotFound(err) {
			msg := fmt.Sprintf(MessageResourceMissing, secretName, controllerMla.Name)
			c.recorder.Event(controllerMla, corev1.EventTypeWarning, ErrResourceMissing, msg)
			logger.V(4).Info("Secret not found", "secretName", secretName, "shard", shard.Name)
			syncErr = errors.Join(syncErr, err)
			continue
		}

		shardSecret, err := shard.SecretLister.Secrets(shardMla.Namespace).Get(secret.Name)
		// secret does not exist in this shard, create it
		if k8serrors.IsNotFound(err) {
			shardSecret, err = shard.CreateSecret(shardMla, secret, FieldManager)
		}

		// requeue on error
		if err != nil {
			return errors.Join(syncErr, err)
		}

		missingOwner, err := c.isMissingOwnership(shardSecret, shardMla)
		// requeue on error
		if err != nil {
			return errors.Join(syncErr, err)
		}

		// if Secret data differs, update the Secret
		// if ownership is missing, update the Secret
		if !reflect.DeepEqual(secret.Data, shardSecret.Data) {
			logger.V(4).Info(fmt.Sprintf("Content changed for Secret %s, updating", secret.Name))
			_, err = shard.UpdateSecret(shardSecret, secret.Data, nil, FieldManager)

			// requeue on error
			if err != nil {
				return errors.Join(syncErr, err)
			}
		}
		if missingOwner {
			logger.V(4).Info(fmt.Sprintf("Ownership missing for Secret %s, updating", secret.Name))
			_, err = shard.UpdateSecret(shardSecret, nil, shardMla, FieldManager)

			// requeue on error
			if err != nil {
				return errors.Join(syncErr, err)
			}
		}
	}
	return syncErr
}

func (c *Controller) syncConfigMapsToShard(configMapNamespace string, controllerMla *v1.MachineLearningAlgorithm, shardMla *v1.MachineLearningAlgorithm, shard *shards.Shard, logger *klog.Logger) error {
	var syncErr error = nil
	for _, configMapName := range shardMla.GetConfigMapNames() {
		// Get the ConfigMap with the name specified in MachineLearningAlgorithm.spec
		configMap, err := c.configMapLister.ConfigMaps(configMapNamespace).Get(configMapName)
		// If the referenced ConfigMap resource doesn't exist in the cluster where the controller is deployed, update syncErr and move on to the next ConfigMap
		if k8serrors.IsNotFound(err) {
			msg := fmt.Sprintf(MessageResourceMissing, configMapName, controllerMla.Name)
			c.recorder.Event(controllerMla, corev1.EventTypeWarning, ErrResourceMissing, msg)
			logger.V(4).Info("ConfigMap not found", "configMapName", configMapName, "shard", shard.Name)
			syncErr = errors.Join(syncErr, err)
			continue
		}

		shardConfigMap, err := shard.ConfigMapLister.ConfigMaps(shardMla.Namespace).Get(configMap.Name)
		// secret does not exist in this shard, create it
		if k8serrors.IsNotFound(err) {
			shardConfigMap, err = shard.CreateConfigMap(shardMla, configMap, FieldManager)
		}

		// requeue on error
		if err != nil {
			return errors.Join(syncErr, err)
		}

		missingOwner, err := c.isMissingOwnership(shardConfigMap, shardMla)
		// requeue on error
		if err != nil {
			return errors.Join(syncErr, err)
		}

		// if data differs, update
		if !reflect.DeepEqual(configMap.Data, shardConfigMap.Data) {
			logger.V(4).Info(fmt.Sprintf("Content changed for ConfigMap %s, updating", configMap.Name))
			_, err = shard.UpdateConfigMap(shardConfigMap, configMap.Data, nil, FieldManager)

			// requeue on error
			if err != nil {
				return errors.Join(syncErr, err)
			}
		}
		// if ownership is not set yet, update it
		if missingOwner {
			logger.V(4).Info(fmt.Sprintf("Ownership missing for ConfigMap %s, updating", configMap.Name))
			_, err = shard.UpdateConfigMap(shardConfigMap, nil, shardMla, FieldManager)

			// requeue on error
			if err != nil {
				return errors.Join(syncErr, err)
			}
		}
	}
	return syncErr
}

// syncHandler compares the actual state with the desired, and attempts to
// converge the two. It then updates the Status block of the MachineLearningAlgorithm resource
// with the current status of the resource.
func (c *Controller) syncHandler(ctx context.Context, objectRef cache.ObjectName) error {
	logger := klog.LoggerWithValues(klog.FromContext(ctx), "objectRef", objectRef)

	// Get the MachineLearningAlgorithm resource with this namespace/name
	logger.V(4).Info(fmt.Sprintf("Syncing algorithm %s", objectRef.Name))
	mla, err := c.mlaLister.MachineLearningAlgorithms(objectRef.Namespace).Get(objectRef.Name)
	if err != nil {
		// The MachineLearningAlgorithm resource may no longer exist, in which case we stop
		// processing.
		if k8serrors.IsNotFound(err) {
			utilruntime.HandleErrorWithContext(ctx, err, "MachineLearningAlgorithm referenced by item in work queue no longer exists", "objectReference", objectRef)
			return nil
		}

		return err
	}
	// TODO: must add itself to owners of all referenced secrets and configmaps, so those can be synchronised as well

	// sync MachineLearningAlgorithm, Secrets and ConfigMaps referenced by it
	syncErrors := map[string]*SyncError{}

	for _, shard := range c.nexusShards {
		logger.V(4).Info(fmt.Sprintf("Syncing to shard %s", shard.Name))
		shardMla, shardErr := shard.MlaLister.MachineLearningAlgorithms(objectRef.Namespace).Get(objectRef.Name)

		// update this MLA in case it exists and has drifted
		if shardErr == nil && !reflect.DeepEqual(shardMla.Spec, mla.Spec) {
			logger.V(4).Info(fmt.Sprintf("Content changed for MachineLearningAlgorithm %s, updating", mla.Name))
			shardMla, shardErr = shard.UpdateMachineLearningAlgorithm(shardMla, mla.Spec, FieldManager)
			// requeue on error
			if shardErr != nil {
				return shardErr
			}
		}

		// if MachineLearningAlgorithm has not been created yet, create a new one in this shard
		if k8serrors.IsNotFound(shardErr) {
			logger.V(4).Info(fmt.Sprintf("Algorithm %s not found in shard %s, creating", objectRef.Name, shard.Name))
			shardMla, shardErr = shard.CreateMachineLearningAlgorithm(mla.Name, mla.Namespace, mla.Spec, FieldManager)
		}

		// requeue on error
		if shardErr != nil {
			logger.V(4).Error(shardErr, fmt.Sprintf("Error processing algorithm resource on shard %s", shard.Name))
			return shardErr
		}

		logger.V(4).Info(fmt.Sprintf("Syncing secrets to shard %s", shard.Name))
		secretSyncErr := c.syncSecretsToShard(mla.Namespace, mla, shardMla, shard, &logger)

		logger.V(4).Info(fmt.Sprintf("Syncing configmaps to shard %s", shard.Name))
		configMapSyncErr := c.syncConfigMapsToShard(mla.Namespace, mla, shardMla, shard, &logger)
		// in case secrets were not synced successfully, save the error for Status update later
		// for this Shard and move on to the next
		syncErrors[shard.Name] = &SyncError{
			failedSecretError:    secretSyncErr,
			failedConfigMapError: configMapSyncErr,
		}
	}

	// Finally, we update the status block of the MachineLearningAlgorithm resource in the controller cluster to reflect the
	// current state of the world across all Shards
	// merge sync errors for convenience
	mergedSyncErrors := map[string]string{}
	for shardName, syncErr := range syncErrors {
		if !syncErr.isEmpty() {
			mergedSyncErrors[shardName] = syncErr.merged()
		}
	}
	logger.V(4).Info(fmt.Sprintf("Synced all shards, updating status for %s", mla.Name))
	err = c.updateMachineLearningAlgorithmStatus(mla, mla.GetSecretNames(), mla.GetConfigMapNames(), c.shardNames(), mergedSyncErrors)
	if err != nil {
		return err
	}
	if len(mergedSyncErrors) > 0 {
		return fmt.Errorf("errors occured when syncing Secrets/ConfigMaps to Shards: %v", mergedSyncErrors)
	}

	c.recorder.Event(mla, corev1.EventTypeNormal, SuccessSynced, MessageResourceSynced)
	return nil
}

// Run will set up the event handlers for types we are interested in, as well
// as syncing informer caches and starting workers. It will block until stopCh
// is closed, at which point it will shutdown the workqueue and wait for
// workers to finish processing their current work items.
func (c *Controller) Run(ctx context.Context, workers int) error { // coverage-ignore
	defer utilruntime.HandleCrash()
	defer c.workqueue.ShutDown()
	logger := klog.FromContext(ctx)

	// Start the informer factories to begin populating the informer caches
	logger.Info("Starting MachineLearningAlgorithm controller")

	// Wait for the caches to be synced before starting workers
	logger.Info("Waiting for informer caches to sync")

	if ok := cache.WaitForCacheSync(ctx.Done(), c.secretsSynced, c.configMapsSynced, c.mlaSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}
	logger.Info("Controller informers synced")
	for _, shard := range c.nexusShards {
		if ok := cache.WaitForCacheSync(ctx.Done(), shard.SecretsSynced, shard.ConfigMapsSynced, shard.MlaSynced); !ok {
			return fmt.Errorf("failed to wait for shard %s caches to sync", shard.Name)
		}
	}
	logger.Info("Shard informers synced")

	logger.Info("Starting workers", "count", workers)
	// Launch workers to process MachineLearningAlgorithm resources
	for i := 0; i < workers; i++ {
		go wait.UntilWithContext(ctx, c.runWorker, time.Second)
	}

	logger.Info("Started workers")
	<-ctx.Done()
	logger.Info("Shutting down workers")

	return nil
}
