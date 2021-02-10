---
title: Install 
weight: 3
---

## Support Matrix

| K8s Distribution/Service                        | Supported                      | Notes                                  |
| ----------------------------------------------- | ------------------------------ | -------------------------------------- |
| [Azure Kubernetes Service (AKS)](./aks)         | :heavy_check_mark:             |                                        |
| [Google Kubernetes Engine (GKE)](./gke)         | In Progress                    | N.B. Requires Ubuntu as worker node OS |
| [AWS Elastic Kubernetes Engine (EKS)](./eks)    | In Progress                    |                                        |
| [K3s](./k3s)                                    | :heavy_check_mark:             |                                        |
| [Kubeadm Kubernetes](./kubeadm)                 | :heavy_check_mark:             |                                        |

**N.B.** Liqo supports K8s >= 1.19.0

### Installation

#### Simple Installation (One-liner)

If your cluster has been installed via `kubeadm`, the Liqo Installer can automatically retrieve the parameters required by Liqo to start.
Before installing, you have to properly set the `kubeconfig` for your cluster. The Liqo installer leverages `kubectl`: by default kubectl refers to the default identity in `~/.kube/config` but you can override this configuration by exporting a `KUBECONFIG` variable.

For example:
```
export KUBECONFIG=my-kubeconfig.yaml
```

You can find more details about configuring `kubectl` in the [official documentation](https://kubernetes.io/docs/concepts/configuration/organize-cluster-access-kubeconfig/).

Similarly to WiFi SSID, you can specify a nickname for your cluster by exporting the variable `CLUSTER_NAME`. 
If you don't specify one, the installer will give you a cluster name in the form "LiqoClusterX", where X is a random number.
Your cluster name can be modified after installation as explained [here](/user/configure/cluster-config#modify-your-cluster-name).

Now, you can install Liqo by launching:

```bash
curl -sL https://get.liqo.io | bash
```

If you want to know more about possible customizations, you can show the help message:
```bash
curl -sL https://get.liqo.io | bash -s -- --help
```

#### Advanced Installation (Helm 3)

Liqo can also be deployed directly using Helm 3. Helm installation should be preferred for customize the Liqo configuration in order to cope with your environment.

Firstly, you should add the official Liqo repository to your Helm Configuration

```bash
helm repo add liqo-helm https://helm.liqo.io/charts
```

To configure the Liqo chart input variables, you have to modify the values.yaml file of the Liqo chart.
If you are installing Liqo for the first time, you can download the default values.yaml file from the chart.

```bash
helm fetch liqo-helm/liqo --untar
less ./liqo/values.yaml
```

You can modify the ```./liqo/values.yaml``` to obtain you desired configuration and then install Liqo.

```bash
helm install test liqo-helm/liqo -f ./liqo/values.yaml
```
