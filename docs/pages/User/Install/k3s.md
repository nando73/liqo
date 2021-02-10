
---
title: K3s 
weight: 3
---


## [About K3s](k3s.io)

K3s is a Kubernetes distribution packaged as a single binary. It is generally lighter than K8s: it can use sqlite3 as the default storage backend, it has no OS dependencies, etc. More information about [K3s](k3s.io) can be found on [its Github repository](https://github.com/k3s-io/k3s).

### Simple Installation

#### Parameters

When installing LIQO on K3s, you should explicitly define the parameters required by Liqo, by exporting the following variables **before** launching the installer:

| Variable               | Default             | Description                                 |
| ---------------------- | -------             | ------------------------------------------- |
| `POD_CIDR`             | 10.42.0.0/16        | the cluster Pod CIDR                        |
| `SERVICE_CIDR`         | 10.43.0.0/16        | the cluster Service CIDR                    |
| `CLUSTER_NAME`         |         | nickname for your cluster that will be seen by others. If you don't specify one, the installer will give you a cluster name in the form "LiqoClusterX", where X is a random number |

Your cluster name can be modified after installation as explained [here](/user/configure/cluster-config#modify-your-cluster-name).

#### Install

You can then run the Liqo installer script, which will use the above settings to configure your Liqo instance.

*N.B.* Please remember to export your K3s `kubeconfig` before launching the script, as presented in previous section. For K3s, the kubeconfig is normally stored in `/etc/rancher/k3s/k3s.yaml`

A possible example of installation is the following (please replace the IP addresses with the ones related to your Kubernetes instance):
```bash
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
export POD_CIDR=10.42.0.0/16
export SERVICE_CIDR=10.43.0.0/16
curl -sL https://get.liqo.io | bash
```

To correctly execute the installer, you should have enough privileges to read the K3s kubeconfig file.