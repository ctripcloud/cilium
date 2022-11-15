# Design

## Design Doc

See proposal [KVStoreMesh: an alternative solution to ClusterMesh](https://docs.google.com/document/d/1Zc8Sdhp96yKSeC1-71_6qd97HPWQv-L4kiBZhl7swrg/edit#).

## Notes on implementation

To make the kvstoremesh changes ease-of-upgrade with the upstream,
it has been intentionally designed to just drag-and-drop source files into
the upstream directories then compile, which greatly decouples it from the
upstream changes, but inevidently introduces many code duplication.

The current organization of the code:

1. `kvstoremesh-operator/`: CLI and configurations;
2. `pkg/kvstoremesh-operator/`: internal stuffs.

    Many data structures are just copied or borrowed from ClusterMesh code.

As each kvstore now contains all entries of all clusters in the KVStoreMesh,
we need some terminologies to differentiate them:

* Local kvstore: kvstore of the local k8s cluster in which this kvstoremesh-operator is running
* Remote kvstore: kvstore of other k8s clusters in the kvstoremesh
* Native entries: entries in local kvstore that belongs to the local k8s clusters (created and maintained by the cilium-agents & cilium-operator in the local k8s cluster)
* Alient entries: entries in local kvstore that belongs to remote k8s clusters (synchronized from remote kvstores by kvstoremesh-operator and maintained by it)

# Deploy

Create/update kvstoremesh secrets first, which will be used by kvstoremesh-operator.
As an example, here we specify two remote clusters `cluster-1-k8s` and `cluster-2-k8s`:

```shell
$ kubectl create secret generic -n kube-system cilium-kvstoremesh-secrets \
  --from-file=cluster-1-k8s=cluster-1-k8s \
  --from-file=cluster-1-etcd.config=cluster-1-etcd.config \
  --from-file=cluster-1-etcd-client-ca.crt=cluster-1-etcd-client-ca.crt \
  --from-file=cluster-1-etcd-client.key=cluster-1-etcd-client.key \
  --from-file=cluster-1-etcd-client.crt=cluster-1-etcd-client.crt \
  --from-file=cluster-2-k8s=cluster-2-k8s \
  --from-file=cluster-2-etcd.config=cluster-2-etcd.config \
  --from-file=cluster-2-etcd-client-ca.crt=cluster-2-etcd-client-ca.crt \
  --from-file=cluster-2-etcd-client.key=cluster-2-etcd-client.key \
  --from-file=cluster-2-etcd-client.crt=cluster-2-etcd-client.crt \
  --dry-run -o yaml | kubectl apply -f -
```

## Run with all-ine-one deployment yaml

```shell
# Some changes to the yaml may be needed
$ kubectl apply -f kvstoremesh-operator.yaml
```

## Run with binary locally

### CLI + configmap

Configurations for local cluster:

```shell
user@node $ ls ./configmap/
cluster-id  cluster-name  k8s-kubeconfig-path kvstore-opt
```

Configurations for remote clusters:

```shell
user@node:/var/lib/kvstoremesh  $ ll
-rw-r--r--  1 1.3K Jun  25 11:51 cluster-1-etcd-client-ca.crt
-rw-r--r--  1 1.4K Jun  25 11:51 cluster-1-etcd-client.crt
-rw-r--r--  1 1.7K Jun  25 11:51 cluster-1-etcd-client.key
-rw-r--r--  1  284 Jun  25 11:51 cluster-1-etcd.config
-rw-r--r--  1  100 Jun  25 11:51 cluster-1-k8s
-rw-r--r--  1 1.3K Jun  20 17:28 cluster-2-etcd-client-ca.crt
-rw-r--r--  1 1.5K Jun  20 17:28 cluster-2-etcd-client.crt
-rw-r--r--  1 1.7K Jun  20 17:28 cluster-2-etcd-client.key
-rw-r--r--  1  280 Jun  20 18:25 cluster-2-etcd.config
-rw-r--r--  1  100 Jun  24 16:04 cluster-2-k8s

user@node:/var/lib/kvstoremesh  $ cat cluster-2-k8s
cluster-name: "cluster2-k8s"
cluster-id: "8"
etcd.config: "/var/lib/kvstoremesh/cluster-2-etcd.config"
etcd.qps: "20"
```

Start kvstoremesh-operator:

```shell
$ ./kvstoremesh-operator --config-dir ./configmap
```

### CLI only

```shell
$ ./kvstoremesh-operator --k8s-kubeconfig-path /etc/cilium/cilium.kubeconfig --cluster-id 8 --cluster-name "cluster-2-k8s"
```

# Build

## Prerequisites

1. This patch must be applied: https://github.com/cilium/cilium/issues/16378
1. This bugfix is needed: https://github.com/cilium/cilium/pull/18808

## Build binary

```shell
$ cd kvstoremesh-operator

$ make
```

## Build image

```shell
$ cd kvstoremesh-operator
$ make

$ sudo docker build . -t <your hub>/kvstoremesh-operator:20221114-rc1
```

# Current limitations

1. Global service is not supported yet, as not all global service info is stored in kvstore

    But, support is possible if some modifications are made to cilium-agent.
