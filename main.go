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
	"flag"
	"github.com/SneaksAndData/nexus-configuration-controller/pkg/models"
	nexusconf "github.com/SneaksAndData/nexus-core/pkg/configurations"
	clientset "github.com/SneaksAndData/nexus-core/pkg/generated/clientset/versioned"
	informers "github.com/SneaksAndData/nexus-core/pkg/generated/informers/externalversions"
	"github.com/SneaksAndData/nexus-core/pkg/shards"
	"github.com/SneaksAndData/nexus-core/pkg/signals"
	"github.com/SneaksAndData/nexus-core/pkg/telemetry"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"time"
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	// set up signals so we handle the shutdown signal gracefully
	ctx := signals.SetupSignalHandler()
	appConfig := nexusconf.LoadConfig[models.AppConfig](ctx)

	appLogger, err := telemetry.ConfigureLogger(ctx, map[string]string{}, appConfig.LogLevel)
	ctx = telemetry.WithStatsd(ctx, "nexus_configuration_controller")
	klog.SetSlogLogger(appLogger)
	logger := klog.FromContext(ctx)

	if err != nil {
		logger.Error(err, "One of the logging handlers cannot be configured")
	}

	controllerCfg, err := clientcmd.BuildConfigFromFlags("", appConfig.ControllerConfigPath)
	if err != nil {
		logger.Error(err, "Error building in-cluster kubeconfig for the controller")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	controllerClient, err := kubernetes.NewForConfig(controllerCfg)
	if err != nil {
		logger.Error(err, "Error building in-cluster kubernetes clientset for the controller")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	controllerNexusClient, err := clientset.NewForConfig(controllerCfg)
	if err != nil {
		logger.Error(err, "Error building in-cluster nexus clientset")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	controllerKubeInformerFactory := kubeinformers.NewSharedInformerFactoryWithOptions(controllerClient, time.Second*30, kubeinformers.WithNamespace(appConfig.ControllerNamespace))
	controllerNexusInformerFactory := informers.NewSharedInformerFactoryWithOptions(controllerNexusClient, time.Second*30, informers.WithNamespace(appConfig.ControllerNamespace))

	shardClients, shardLoaderError := shards.LoadClients(appConfig.ShardConfigPath, appConfig.ControllerNamespace, logger)
	if shardLoaderError != nil {
		logger.Error(shardLoaderError, "unable to initialize shard clients")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}
	connectedShards := []*shards.Shard{}

	// only load kubeconfig files in the provided location
	for _, shardClient := range shardClients {
		connectedShards = append(connectedShards, shardClient.ToShard(controllerCfg.ServerName, ctx))
	}

	controller, controllerCreationErr := NewController(
		ctx,
		appConfig.ControllerNamespace,
		controllerClient,
		controllerNexusClient,
		connectedShards,
		controllerKubeInformerFactory.Core().V1().Secrets(),
		controllerKubeInformerFactory.Core().V1().ConfigMaps(),
		controllerNexusInformerFactory.Science().V1().NexusAlgorithmTemplates(),
		controllerNexusInformerFactory.Science().V1().NexusAlgorithmWorkgroups(),
		appConfig.FailureRateBaseDelay,
		appConfig.FailureRateMaxDelay,
		appConfig.RateLimitElementsPerSecond,
		appConfig.RateLimitElementsBurst,
	)

	// notice that there is no need to run Start methods in a separate goroutine. (i.e. go kubeInformerFactory.Start(ctx.done())
	// Start method is non-blocking and runs all registered informers in a dedicated goroutine.
	controllerKubeInformerFactory.Start(ctx.Done())
	controllerNexusInformerFactory.Start(ctx.Done())

	if controllerCreationErr != nil {
		logger.Error(controllerCreationErr, "Error creating a controller instance")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	if err = controller.Run(ctx, appConfig.Workers); err != nil {
		logger.Error(err, "Error running controller")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}
}
