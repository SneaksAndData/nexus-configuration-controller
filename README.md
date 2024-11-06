# Introduction
Nexus Configuration Controller (NCC) provides synchronization capability for multi-cluster Nexus deployments. `MachineLearningAlgorithm` resources deployed to the *controller* cluster will be automatically synced to all connected *shard* clusters along with their dependent *secrets* and *configmaps*.

## Install
Nexus Configuration Controller installs via helm:
```shell
helm install nexus-configuration-controller oci://ghcr.io/sneaksanddata/helm/nexus-configuration-controller \
  --version v0.1.0 \
  --namespace nexus
```

## Operation
Once installed, NCC does not require any additional configuration. It will adopt existing resources in the *shard* clusters and synchronize them to the version in the controller cluster, as well as start propagating changes to secrets and configurations.
If you have secrets already present in the *shards* clusters, you must remove them or the sync will fail for the affected resources.
