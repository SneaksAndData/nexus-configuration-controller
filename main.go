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
	alias                string
	controllerconfigpath string
	shardconfigpath      string
	controllerns         string
	workers              int
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	// set up signals so we handle the shutdown signal gracefully
	ctx := signals.SetupSignalHandler()
	logger := klog.FromContext(ctx)

	controllerCfg, err := clientcmd.BuildConfigFromFlags("", controllerconfigpath)
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

	controllerKubeInformerFactory := kubeinformers.NewSharedInformerFactory(controllerClient, time.Second*30)
	controllerNexusInformerFactory := informers.NewSharedInformerFactory(controllerNexusClient, time.Second*30)

	files, err := os.ReadDir(shardconfigpath)
	if err != nil {
		logger.Error(err, "Error opening kubeconfig files for Shards")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}
	connectedShards := make([]*shards.Shard, 0, len(files))

	// only load kubeconfig files in the provided location
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".kubeconfig") {
			logger.Info("Loading Shard kubeconfig file", "file", file.Name())

			cfg, err := clientcmd.BuildConfigFromFlags("", path.Join(shardconfigpath, file.Name()))
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

			shardKubeInformerFactory := kubeinformers.NewSharedInformerFactory(kubeClient, time.Second*30)
			shardNexusInformerFactory := informers.NewSharedInformerFactory(nexusClient, time.Second*30)

			shardKubeInformerFactory.Start(ctx.Done())
			shardNexusInformerFactory.Start(ctx.Done())

			connectedShards = append(connectedShards, shards.NewShard(
				alias,
				strings.Split(file.Name(), ".")[0],
				kubeClient,
				nexusClient,
				shardNexusInformerFactory.Science().V1().MachineLearningAlgorithms(),
				shardKubeInformerFactory.Core().V1().Secrets(),
				shardKubeInformerFactory.Core().V1().ConfigMaps()))
		}
	}

	// notice that there is no need to run Start methods in a separate goroutine. (i.e. go kubeInformerFactory.Start(ctx.done())
	// Start method is non-blocking and runs all registered informers in a dedicated goroutine.
	controllerKubeInformerFactory.Start(ctx.Done())
	controllerNexusInformerFactory.Start(ctx.Done())

	controller := NewController(
		ctx,
		controllerns,
		controllerClient,
		controllerNexusClient,
		connectedShards,
		controllerKubeInformerFactory.Core().V1().Secrets(),
		controllerKubeInformerFactory.Core().V1().ConfigMaps(),
		controllerNexusInformerFactory.Science().V1().MachineLearningAlgorithms())

	if err = controller.Run(ctx, workers); err != nil {
		logger.Error(err, "Error running controller")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}
}

func init() {
	flag.StringVar(&shardconfigpath, "shardscfg", "", "Path to a directory containing *.kubeconfig files for Shards.")
	flag.StringVar(&controllerconfigpath, "controllercfg", "", "Path to a kubeconfig file for the controller cluster.")
	flag.StringVar(&alias, "alias", "", "Alias for the controller cluster.")
	flag.StringVar(&controllerns, "namespace", "", "Path to a kubeconfig file for the controller cluster.")
	flag.IntVar(&workers, "workers", 2, "Number of worker threads.")
}
