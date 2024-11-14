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
	"flag"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"os"
	"path"
	clientset "science.sneaksanddata.com/nexus-configuration-controller/pkg/generated/clientset/versioned"
	informers "science.sneaksanddata.com/nexus-configuration-controller/pkg/generated/informers/externalversions"
	"science.sneaksanddata.com/nexus-configuration-controller/pkg/shards"
	"science.sneaksanddata.com/nexus-configuration-controller/pkg/signals"
	"strings"
	"time"
)

var (
	alias                      string
	controllerConfigPath       string
	shardConfigPath            string
	controllerNamespace        string
	workers                    int
	failureRateBaseDelay       time.Duration
	failureRateMaxDelay        time.Duration
	rateLimitElementsPerSecond int
	rateLimitElementsBurst     int
)

func init() {
	flag.StringVar(&shardConfigPath, "shards_cfg", "", "Path to a directory containing *.kubeconfig files for Shards.")
	flag.StringVar(&controllerConfigPath, "controller_cfg", "", "Path to a kubeconfig file for the controller cluster.")
	flag.StringVar(&alias, "alias", "", "Alias for the controller cluster.")
	flag.StringVar(&controllerNamespace, "namespace", "", "Namespace the controller is deployed to.")
	flag.IntVar(&workers, "workers", 2, "Number of worker threads.")
	flag.DurationVar(&failureRateBaseDelay, "failure_rate_base_delay", 30*time.Millisecond, "Base delay for exponential failure backoff, milliseconds.")
	flag.DurationVar(&failureRateMaxDelay, "failure_rate_max_delay", 1000*time.Second, "Max delay for exponential failure backoff, seconds.")
	flag.IntVar(&rateLimitElementsPerSecond, "rate_limit_per_second", 50, "Max number of resources to process per second.")
	flag.IntVar(&rateLimitElementsBurst, "rate_limit_burst", 300, "Burst this number of elements before rate limit kicks in.")
}

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	// set up signals so we handle the shutdown signal gracefully
	ctx := signals.SetupSignalHandler()
	logger := klog.FromContext(ctx)

	controllerCfg, err := clientcmd.BuildConfigFromFlags("", controllerConfigPath)
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

	controllerKubeInformerFactory := kubeinformers.NewSharedInformerFactoryWithOptions(controllerClient, time.Second*30, kubeinformers.WithNamespace(controllerNamespace))
	controllerNexusInformerFactory := informers.NewSharedInformerFactoryWithOptions(controllerNexusClient, time.Second*30, informers.WithNamespace(controllerNamespace))

	files, err := os.ReadDir(shardConfigPath)
	if err != nil {
		logger.Error(err, "Error opening kubeconfig files for Shards")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}
	connectedShards := make([]*shards.Shard, 0, len(files))

	// only load kubeconfig files in the provided location
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".kubeconfig") {
			logger.Info("Loading Shard kubeconfig file", "file", file.Name())

			cfg, err := clientcmd.BuildConfigFromFlags("", path.Join(shardConfigPath, file.Name()))
			if err != nil {
				logger.Error(err, "Error building kubeconfig for shard {shard}", file.Name())
				klog.FlushAndExit(klog.ExitFlushTimeout, 1)
			}

			kubeClient, err := kubernetes.NewForConfig(cfg)
			if err != nil {
				logger.Error(err, "Error building kubernetes clientset for shard {shard}", file.Name())
				klog.FlushAndExit(klog.ExitFlushTimeout, 1)
			}

			nexusClient, err := clientset.NewForConfig(cfg)
			if err != nil {
				logger.Error(err, "Error building kubernetes clientset for MachineLearningAlgorithm API for shard {shard}", file.Name())
				klog.FlushAndExit(klog.ExitFlushTimeout, 1)
			}

			shardKubeInformerFactory := kubeinformers.NewSharedInformerFactoryWithOptions(kubeClient, time.Second*30, kubeinformers.WithNamespace(controllerNamespace))
			shardNexusInformerFactory := informers.NewSharedInformerFactoryWithOptions(nexusClient, time.Second*30, informers.WithNamespace(controllerNamespace))

			connectedShards = append(connectedShards, shards.NewShard(
				alias,
				strings.Split(file.Name(), ".")[0],
				kubeClient,
				nexusClient,
				shardNexusInformerFactory.Science().V1().MachineLearningAlgorithms(),
				shardKubeInformerFactory.Core().V1().Secrets(),
				shardKubeInformerFactory.Core().V1().ConfigMaps()))

			shardKubeInformerFactory.Start(ctx.Done())
			shardNexusInformerFactory.Start(ctx.Done())
		}
	}

	controller, controllerCreationErr := NewController(
		ctx,
		controllerNamespace,
		controllerClient,
		controllerNexusClient,
		connectedShards,
		controllerKubeInformerFactory.Core().V1().Secrets(),
		controllerKubeInformerFactory.Core().V1().ConfigMaps(),
		controllerNexusInformerFactory.Science().V1().MachineLearningAlgorithms(),
		failureRateBaseDelay,
		failureRateMaxDelay,
		rateLimitElementsPerSecond,
		rateLimitElementsBurst,
	)

	// notice that there is no need to run Start methods in a separate goroutine. (i.e. go kubeInformerFactory.Start(ctx.done())
	// Start method is non-blocking and runs all registered informers in a dedicated goroutine.
	controllerKubeInformerFactory.Start(ctx.Done())
	controllerNexusInformerFactory.Start(ctx.Done())

	if controllerCreationErr != nil {
		logger.Error(controllerCreationErr, "Error creating a controller instance")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	if err = controller.Run(ctx, workers); err != nil {
		logger.Error(err, "Error running controller")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}
}
