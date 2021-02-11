---
title: GKE
weight: 2
---

This guide will show you how to install Liqo on your GKE cluster

### Requirements

* a [Google Cloud account](https://cloud.google.com/?hl=it)

### Steps

* [Create a GKE Cluster](#create-a-gke-cluster)
* [Deploy Liqo](#deploy-liqo)
* [Check that Liqo is Running](#check-that-liqo-is-running)

## Create a GKE Cluster

### Access the Google Cloud Console

Access in the Google Cloud [Console](https://cloud.google.com/?hl=it) to the Kubernetes Service.

![](/images/install/gke/01.png)

### Create a new Cluster

Click on the `Create` button to create a new cluster. A new panel will appear. Select the desired name and a location type to your cluster.

__NOTE__: Liqo only supports a `Kubernetes version` >= 1.19.0

![](/images/install/gke/02.png)

#### Set the Node Poll

Select the default Node Poll and make sure that the "Image type" is set to Ubuntu.

> NOTE: Liqo is fully compliant with Google [Preemptible Nodes](https://cloud.google.com/kubernetes-engine/docs/how-to/preemptible-vms)

![](/images/install/gke/03.png)

Liqo does not require any other configurations to the cluster. You can click on the `Create` button.

Google Cloud will take some minutes to deploy your cluster.

## Deploy Liqo

### One Line installer

#### Export Variables

The Liqo one line installer needs some environment variables to know where the Liqo components are accessible from the
external world and how the local Kubernetes installation is configured.

In particular, we have to export the following environment variables:

| Variable               | Default | Description                                 |
| ---------------------- | ------- | ------------------------------------------- |
| `POD_CIDR`             |         | the cluster Pod CIDR                        |
| `SERVICE_CIDR`         |         | the cluster Service CIDR                    |
| `LIQO_INGRESS_CLASS`   |         | the [ingress class](https://kubernetes.io/docs/concepts/services-networking/ingress/#ingress-class) to be used by the Auth Service Ingress |
| `LIQO_APISERVER_ADDR`  |         | the hostname where to access the API server |
| `LIQO_APISERVER_PORT`  | `6443`  | the port where to access the API server     |
| `LIQO_AUTHSERVER_ADDR` |         | the hostname where to access the Auth Service, the one exposed with the ingress, if it is not set the service will be exposed with a [NodePort Service](https://kubernetes.io/docs/concepts/services-networking/service/#nodeport) instead of an [Ingress](https://kubernetes.io/docs/concepts/services-networking/ingress/) |
| `LIQO_AUTHSERVER_PORT` | `443`   | the port where to access the Auth Service   |

#### How can I know those variable values in GKE?

Some variables are the same for each GKE cluster.

| Variable               | Value                          | Notes                                  |
| ---------------------- | ------------------------------ | -------------------------------------- |
| `POD_CIDR`             | 10.124.0.0/14                  |                                        |
| `SERVICE_CIDR`         | 10.0.0.0/20                    |                                        |
| `LIQO_INGRESS_CLASS`   | \<YOUR INGRESS CLASS\>         | If you have an Ingress Controller. If you are using a [LoadBalancer Service](#expose-the-auth-service-with-a-loadbalancer-service) do not export it |
| `LIQO_APISERVER_PORT`  | 443                            |                                        |
| `LIQO_AUTHSERVER_PORT` | 443                            | If you have an Ingress Controller. If you are using a [LoadBalancer Service](#expose-the-auth-service-with-a-loadbalancer-service) do not export it |

The other values can be found in the Google Cloud Console.

The `LIQO_APISERVER_ADDR` con be found in our cluster details as __Endpoint__

![](/images/install/gke/04.png)

The `LIQO_AUTHSERVER_ADDR` is where the Liqo Auth Service will be reachable. If you are using an Ingress, you can set here
a hostname that you can manage. Another possible solution is to expose it as a `LoadBalancer` Service as described [below](#expose-the-auth-service-with-a-loadbalancer-service).

#### Install Liqo

After this configuration step we can [run the Liqo one line installer](/user/gettingstarted/install/#default-install)
as usual.

We need a last configuration step required. We have to change the `liqo-gateway-endpoint` service type from
`NodePort` to `LoadBalancer` to make it reachable.

```bash
kubectl patch service -n liqo liqo-gateway-endpoint \
    --patch '{"spec":{"type":"LoadBalancer"}}'
```

#### Expose the Auth Service with a LoadBalancer Service

To make the Auth Service reachable without the needing of an Ingress and a Domain Name, you can change the `auth-service`
Service type from `NodePort` to `LoadBalancer`.

```bash
kubectl patch service -n liqo auth-service \
    --patch '{"spec":{"type":"LoadBalancer"}}'
```

#### Example (with Ingress)

Export variables

```bash
export POD_CIDR=10.124.0.0/14
export SERVICE_CIDR=10.0.0.0/20
export LIQO_INGRESS_CLASS=<YOUR INGRESS CLASS>
export LIQO_APISERVER_PORT=443
export LIQO_APISERVER_ADDR=35.225.127.150
export LIQO_AUTHSERVER_ADDR=auth.example.com
```

Install Liqo

```bash
curl -sL https://get.liqo.io | bash
```

Make the gateway reachable

```bash
kubectl patch service -n liqo liqo-gateway-endpoint \
    --patch '{"spec":{"type":"LoadBalancer"}}'
```

#### Example (with LoadBalancer)

Export variables

```bash
export POD_CIDR=10.124.0.0/14
export SERVICE_CIDR=10.0.0.0/20
export LIQO_APISERVER_PORT=443
export LIQO_APISERVER_ADDR=35.225.127.150
```

Install Liqo

```bash
curl -sL https://get.liqo.io | bash
```

Make the gateway reachable

```bash
kubectl patch service -n liqo liqo-gateway-endpoint \
    --patch '{"spec":{"type":"LoadBalancer"}}'
```

Make the Auth Service reachable

```bash
kubectl patch service -n liqo auth-service \
    --patch '{"spec":{"type":"LoadBalancer"}}'
```

## Check that Liqo is Running

Wait that all Liqo pods and services are up and running

```bash
kubectl get pods -n liqo
```

```bash
kubectl get svc -n liqo
```

![](/images/install/gke/05.png)

### Access the cluster configurations

You can get the cluster configurations from the Auth Service endpoint to check that this service has been correctly deployed

```bash
curl --insecure https://34.71.59.19/ids
```

```json
{"clusterId":"0558de48-097b-4b7d-ba04-6bd2a0f9d24f","clusterName":"LiqoCluster0692","guestNamespace":"liqo"}
```

Congratulations! Liqo is now up and running on your GKE cluster, you can now peer with other Liqo instances!

### Establish a Peering

The Auth Service URL is the only required value to make this cluster peerable from the external world.

You can add a `ForeignCluster` resource in any other cluster where Liqo is installed to be able to join your cluster.

An example of this resource can be:

```yaml
apiVersion: discovery.liqo.io/v1alpha1
kind: ForeignCluster
metadata:
  name: my-gke-cluster
spec:
  authUrl: "https://34.71.59.19"
```

When the CR will be created the Liqo control plane will contact the URL shown in the step before with the curl command to
retrieve all the required cluster information.
