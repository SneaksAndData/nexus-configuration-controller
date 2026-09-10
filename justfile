set shell := ["bash", "-c"]
# helm for Nexus
NEXUS_CRD_VERSION := "v1.0.0-6-ge0cf63e"
APP_VERSION := "0.0.0"
BUILD_NUMBER := "1"

# helm for NCC
NCC_CHART_NAME := "ncc"
NCC_CHART_PATH := "./.helm"
NCC_IMAGE_NAME := "ncc-dev"
NCC_IMAGE_TAG  := "latest"

# controller cluster
NEXUS_CLUSTER_NAME := "nexus-controller-0"
# shard cluster
NEXUS_SHARD_CLUSTER_NAME := "nexus-shard-0"

# Default recipe
fresh: stop up

# Start CI environment
up: start-controller-cluster start-shard-cluster build-image load-image create-namespace crd deploy-chart

start-controller-cluster:
    kind create cluster --config=test-resources/kind.yaml --name {{NEXUS_CLUSTER_NAME}}
start-shard-cluster:
    kind create cluster --config=test-resources/kind.yaml --name {{NEXUS_SHARD_CLUSTER_NAME}}

# Run all tests
test-all:
    APPLICATION_ENVIRONMENT=units go test -v ./...

# Cleanup CI environment
stop:
    @echo "🧹 Cleaning up..."
    kind delete cluster --name {{NEXUS_CLUSTER_NAME}}
    kind delete cluster --name {{NEXUS_SHARD_CLUSTER_NAME}}
    rm -f cover-indexed.out cover-bare.out cover.out

# View logs
logs name="":
    docker logs -f {{ name }}

# build the local Docker image
build-image:
    docker build \
        --build-arg APPVERSION={{APP_VERSION}} \
        --build-arg BUILDNUMBER={{BUILD_NUMBER}} \
        -t {{NCC_IMAGE_NAME}}:{{NCC_IMAGE_TAG}} \
        -f .container/Dockerfile .

# load image into the cluster
load-image:
    kind load docker-image {{NCC_IMAGE_NAME}}:{{NCC_IMAGE_TAG}} --name  {{NEXUS_CLUSTER_NAME}}
    kind load docker-image {{NCC_IMAGE_NAME}}:{{NCC_IMAGE_TAG}} --name  {{NEXUS_SHARD_CLUSTER_NAME}}

create-shard-config:
    kind export kubeconfig --name {{NEXUS_SHARD_CLUSTER_NAME}} --kubeconfig ./test-resources/kind-{{NEXUS_SHARD_CLUSTER_NAME}}.kubeconfig
    kubectl create secret generic ncc-shards \
        --namespace nexus \
        --from-literal={{NEXUS_SHARD_CLUSTER_NAME}}.kubeconfig="$(cat ./test-resources/kind-{{NEXUS_SHARD_CLUSTER_NAME}}.kubeconfig)" --dry-run=client -o yaml | kubectl apply -f -

create-namespace:
    kubectl --context kind-{{NEXUS_CLUSTER_NAME}} create namespace nexus --dry-run=client -o yaml | kubectl apply -f -
    kubectl --context kind-{{NEXUS_SHARD_CLUSTER_NAME}} create namespace nexus --dry-run=client -o yaml | kubectl apply -f -

# install chart
deploy-chart:
    helm upgrade --kube-context kind-{{NEXUS_CLUSTER_NAME}} --install -namespace nexus {{NCC_CHART_NAME}} {{NCC_CHART_PATH}} \
        --set image.repository={{NCC_IMAGE_NAME}} \
        --set image.tag={{NCC_IMAGE_TAG}} \
        --set image.pullPolicy=Never \
        --set controller.alias={{NEXUS_CLUSTER_NAME}} \
        --set controller.namespace=nexus \
        --set controller.shardsConfigSecretName=""


# cleanup
remove-chart:
    helm uninstall -n nexus {{NCC_CHART_NAME}}

crd:
    helm upgrade --install --namespace nexus nexus-crd  oci://ghcr.io/sneaksanddata/helm/nexus-crd --version v1.0.0-6-ge0cf63e
