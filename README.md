![coverage](https://raw.githubusercontent.com/SneaksAndData/nexus-configuration-controller/badges/.badges/main/coverage.svg)

# Introduction
Nexus Configuration Controller (NCC) provides synchronization capability for multi-cluster Nexus deployments. `MachineLearningAlgorithm` resources deployed to the *controller* cluster will be automatically synced to all connected *shard* clusters along with their dependent *secrets* and *configmaps*.

## Install
Nexus Configuration Controller installs via helm:
```shell
helm install nexus-configuration-controller oci://ghcr.io/sneaksanddata/helm/nexus-configuration-controller \
  --version v0.1.1 \
  --namespace nexus \
  --controller.shardsConfigSecretName ncc-shards
```

Controller expects the cluster to provide a secret that contains kubeconfig data for each shard cluster. For each shard, there must be a separate entry named `<cluster_name>.kubeconfig` with relevant kubeconfig data as a value. Example:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: ncc-shards
  namespace: nexus
immutable: true
data:
  my-k8s-cluster-0.kubeconfig: base64(kubeconfig for my-k8s-cluster-0)
type: Opaque
```

Additionally, if you run on EKS, you need to either set explicit AWS credentials or set up node role or IRSA and map service account role to a respective role in the shard cluster.

## Operation
Once installed, NCC does not require any additional configuration. It will adopt existing resources in the *shard* clusters and synchronize them to the version in the controller cluster, as well as start propagating changes to secrets and configurations.
If you have secrets already present in the *shards* clusters, you must remove them or the sync will fail for the affected resources.
