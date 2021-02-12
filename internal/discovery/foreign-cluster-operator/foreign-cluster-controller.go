/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package foreign_cluster_operator

import (
	"context"
	goerrors "errors"
	"fmt"
	discoveryv1alpha1 "github.com/liqotech/liqo/apis/discovery/v1alpha1"
	nettypes "github.com/liqotech/liqo/apis/net/v1alpha1"
	advtypes "github.com/liqotech/liqo/apis/sharing/v1alpha1"
	"github.com/liqotech/liqo/internal/crdReplicator"
	"github.com/liqotech/liqo/internal/discovery"
	"github.com/liqotech/liqo/internal/discovery/kubeconfig"
	"github.com/liqotech/liqo/internal/discovery/utils"
	"github.com/liqotech/liqo/internal/monitoring"
	"github.com/liqotech/liqo/pkg/clusterID"
	"github.com/liqotech/liqo/pkg/crdClient"
	discoveryPkg "github.com/liqotech/liqo/pkg/discovery"
	apiv1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog"
	"k8s.io/kubernetes/pkg/util/slice"
	"os"
	ctrl "sigs.k8s.io/controller-runtime"
	"strings"
	"time"
)

const FinalizerString = "foreigncluster.discovery.liqo.io/peered"

// ForeignClusterReconciler reconciles a ForeignCluster object
type ForeignClusterReconciler struct {
	Scheme *runtime.Scheme

	Namespace           string
	crdClient           *crdClient.CRDClient
	advertisementClient *crdClient.CRDClient
	networkClient       *crdClient.CRDClient
	clusterID           clusterID.ClusterID
	RequeueAfter        time.Duration

	ConfigProvider discovery.ConfigProvider

	// testing
	ForeignConfig *rest.Config
}

// +kubebuilder:rbac:groups=discovery.liqo.io,resources=foreignclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=discovery.liqo.io,resources=foreignclusters/status,verbs=get;update;patch

func (r *ForeignClusterReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	_ = context.Background()

	monitoring.PeeringProcessExecutionStarted()

	klog.V(4).Infof("Reconciling ForeignCluster %s", req.Name)

	tmp, err := r.crdClient.Resource("foreignclusters").Get(req.Name, metav1.GetOptions{})
	if err != nil {
		return ctrl.Result{}, nil
	}
	fc, ok := tmp.(*discoveryv1alpha1.ForeignCluster)
	if !ok {
		klog.Error("created object is not a ForeignCluster")
		return ctrl.Result{
			Requeue:      true,
			RequeueAfter: r.RequeueAfter,
		}, goerrors.New("created object is not a ForeignCluster")
	}

	startJoinProcess := false
	// check if the Join flag is set true and the Status of the join process is false
	if fc.Spec.Join && !fc.Status.Outgoing.Joined {
		// expose the metric to monitor the Begin of the Creation of the Reering Request
		monitoring.PeeringProcessEventRegister(monitoring.ForeignClusterOperator, monitoring.CreatePeeringRequest, monitoring.Start)
		startJoinProcess = true
	}

	requireUpdate := false

	if r.needsClusterIdentityDefaulting(fc) {
		// this ForeignCluster has not all the required fields, probably it has been added manually, so default to exposed values
		if err = r.clusterIdentityDefaulting(fc); err != nil {
			klog.Error(err)
			return ctrl.Result{
				Requeue:      true,
				RequeueAfter: r.RequeueAfter,
			}, err
		} else {
			// the resource has been updated, no need to requeue
			return ctrl.Result{}, nil
		}
	}

	// set trust property
	// This will only be executed in the ForeignCluster CR has been added in a manual way,
	// if it was discovered this field is set by the discovery process.
	// We can consider to move it in a mutating webhook
	if fc.Spec.TrustMode == discoveryPkg.TrustModeUnknown || fc.Spec.TrustMode == "" {
		trust, err := fc.CheckTrusted()
		if err != nil {
			klog.Error(err)
			return ctrl.Result{
				Requeue:      true,
				RequeueAfter: r.RequeueAfter,
			}, err
		}
		if trust {
			fc.Spec.TrustMode = discoveryPkg.TrustModeTrusted
		} else {
			fc.Spec.TrustMode = discoveryPkg.TrustModeUntrusted
		}
		// set join flag
		// if it was discovery with WAN discovery, this value is overwritten by SearchDomain value
		if fc.Spec.DiscoveryType != discoveryPkg.WanDiscovery && fc.Spec.DiscoveryType != discoveryPkg.IncomingPeeringDiscovery && fc.Spec.DiscoveryType != discoveryPkg.ManualDiscovery {
			fc.Spec.Join = (r.getAutoJoin(fc) && fc.Spec.TrustMode == discoveryPkg.TrustModeTrusted) || (r.getAutoJoinUntrusted(fc) && fc.Spec.TrustMode == discoveryPkg.TrustModeUntrusted)
		}

		requireUpdate = true
	}

	// if it has no discovery type label, add it
	if fc.ObjectMeta.Labels == nil {
		fc.ObjectMeta.Labels = map[string]string{}
	}
	if fc.ObjectMeta.Labels[discoveryPkg.DiscoveryTypeLabel] == "" || fc.ObjectMeta.Labels[discoveryPkg.DiscoveryTypeLabel] != string(fc.Spec.DiscoveryType) {
		fc.ObjectMeta.Labels[discoveryPkg.DiscoveryTypeLabel] = string(fc.Spec.DiscoveryType)
		requireUpdate = true
	}
	// set cluster-id label to easy retrieve ForeignClusters by ClusterId,
	// if it is added manually, the name maybe not coincide with ClusterId
	if fc.ObjectMeta.Labels[discoveryPkg.ClusterIdLabel] == "" {
		fc.ObjectMeta.Labels[discoveryPkg.ClusterIdLabel] = fc.Spec.ClusterIdentity.ClusterID
		requireUpdate = true
	}

	// check for NetworkConfigs
	if startJoinProcess {
		monitoring.PeeringProcessEventRegister(monitoring.ForeignClusterOperator, monitoring.CheckNetworkConfigs, monitoring.Start)
	}
	err = r.checkNetwork(fc, &requireUpdate)
	if err != nil {
		klog.Error(err)
		return ctrl.Result{
			Requeue:      true,
			RequeueAfter: r.RequeueAfter,
		}, err
	}
	if startJoinProcess {
		monitoring.PeeringProcessEventRegister(monitoring.ForeignClusterOperator, monitoring.CheckNetworkConfigs, monitoring.End)
	}

	// check for TunnelEndpoints
	if startJoinProcess {
		monitoring.PeeringProcessEventRegister(monitoring.ForeignClusterOperator, monitoring.CheckTunnelEndpoints, monitoring.Start)
	}
	err = r.checkTEP(fc, &requireUpdate)
	if err != nil {
		klog.Error(err)
		return ctrl.Result{
			Requeue:      true,
			RequeueAfter: r.RequeueAfter,
		}, err
	}
	if startJoinProcess {
		monitoring.PeeringProcessEventRegister(monitoring.ForeignClusterOperator, monitoring.CheckTunnelEndpoints, monitoring.End)
	}

	// check if linked advertisement exists
	if startJoinProcess {
		monitoring.PeeringProcessEventRegister(monitoring.ForeignClusterOperator, monitoring.CheckAdvertisement, monitoring.Start)
	}
	if fc.Status.Outgoing.Advertisement != nil {
		tmp, err = r.advertisementClient.Resource("advertisements").Get(fc.Status.Outgoing.Advertisement.Name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			fc.Status.Outgoing.Advertisement = nil
			fc.Status.Outgoing.AvailableIdentity = false
			fc.Status.Outgoing.IdentityRef = nil
			fc.Status.Outgoing.AdvertisementStatus = ""
			fc.Status.Outgoing.Joined = false
			fc.Status.Outgoing.RemotePeeringRequestName = ""
			fc.Spec.Join = false
			requireUpdate = true
		} else if err == nil {
			// check if kubeconfig secret exists
			adv, ok := tmp.(*advtypes.Advertisement)
			if !ok {
				err = goerrors.New("retrieved object is not an Advertisement")
				klog.Error(err)
				return ctrl.Result{
					Requeue:      true,
					RequeueAfter: r.RequeueAfter,
				}, err
			}
			if adv.Spec.KubeConfigRef.Name != "" && adv.Spec.KubeConfigRef.Namespace != "" {
				_, err = r.crdClient.Client().CoreV1().Secrets(adv.Spec.KubeConfigRef.Namespace).Get(context.TODO(), adv.Spec.KubeConfigRef.Name, metav1.GetOptions{})
				available := err == nil
				if fc.Status.Outgoing.AvailableIdentity != available {
					fc.Status.Outgoing.AvailableIdentity = available
					if available {
						fc.Status.Outgoing.IdentityRef = &apiv1.ObjectReference{
							Kind:       "Secret",
							Namespace:  adv.Spec.KubeConfigRef.Namespace,
							Name:       adv.Spec.KubeConfigRef.Name,
							APIVersion: "v1",
						}
					}
					requireUpdate = true
				}
			}

			// update advertisement status
			status := adv.Status.AdvertisementStatus
			if status != fc.Status.Outgoing.AdvertisementStatus {
				fc.Status.Outgoing.AdvertisementStatus = status
				requireUpdate = true
			}
		}
	}
	if startJoinProcess {
		monitoring.PeeringProcessEventRegister(monitoring.ForeignClusterOperator, monitoring.CheckAdvertisement, monitoring.End)
	}

	// check if linked peeringRequest exists
	if startJoinProcess {
		monitoring.PeeringProcessEventRegister(monitoring.ForeignClusterOperator, monitoring.CheckPeeringRequest, monitoring.Start)
	}
	if fc.Status.Incoming.PeeringRequest != nil {
		tmp, err = r.crdClient.Resource("peeringrequests").Get(fc.Status.Incoming.PeeringRequest.Name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			fc.Status.Incoming.PeeringRequest = nil
			fc.Status.Incoming.AvailableIdentity = false
			fc.Status.Incoming.IdentityRef = nil
			fc.Status.Incoming.AdvertisementStatus = ""
			fc.Status.Incoming.Joined = false
			requireUpdate = true
		} else if err == nil {
			pr, ok := tmp.(*discoveryv1alpha1.PeeringRequest)
			if !ok {
				err = goerrors.New("retrieved object is not a PeeringRequest")
				klog.Error(err)
				return ctrl.Result{
					Requeue:      true,
					RequeueAfter: r.RequeueAfter,
				}, err
			}

			if !fc.Status.Incoming.Joined {
				// PeeringRequest exists, set flag to true
				fc.Status.Incoming.Joined = true
				requireUpdate = true
			}

			// check if kubeconfig secret exists
			if pr.Spec.KubeConfigRef != nil && pr.Spec.KubeConfigRef.Name != "" && pr.Spec.KubeConfigRef.Namespace != "" {
				_, err = r.crdClient.Client().CoreV1().Secrets(pr.Spec.KubeConfigRef.Namespace).Get(context.TODO(), pr.Spec.KubeConfigRef.Name, metav1.GetOptions{})
				available := err == nil
				if fc.Status.Incoming.AvailableIdentity != available {
					fc.Status.Incoming.AvailableIdentity = available
					if available {
						fc.Status.Incoming.IdentityRef = pr.Spec.KubeConfigRef
					}
					requireUpdate = true
				}
			}

			// update advertisement status
			status := pr.Status.AdvertisementStatus
			if status != fc.Status.Incoming.AdvertisementStatus {
				fc.Status.Incoming.AdvertisementStatus = status
				requireUpdate = true
			}
		}
	}
	if startJoinProcess {
		monitoring.PeeringProcessEventRegister(monitoring.ForeignClusterOperator, monitoring.CheckPeeringRequest, monitoring.End)
	}

	// if it has been discovered thanks to incoming peeringRequest and it has no active connections, delete it
	if fc.Spec.DiscoveryType == discoveryPkg.IncomingPeeringDiscovery && fc.DeletionTimestamp.IsZero() && fc.Status.Incoming.PeeringRequest == nil && fc.Status.Outgoing.Advertisement == nil {
		err = r.crdClient.Resource("foreignclusters").Delete(fc.Name, metav1.DeleteOptions{})
		if err != nil {
			klog.Error(err, err.Error())
			return ctrl.Result{
				Requeue:      true,
				RequeueAfter: r.RequeueAfter,
			}, err
		}
		klog.Infof("%s deleted, discovery type %s has no active connections", fc.Name, fc.Spec.DiscoveryType)
		klog.V(4).Infof("ForeignCluster %s successfully reconciled", fc.Name)
		return ctrl.Result{}, nil
	}

	foreignDiscoveryClient, err := r.getRemoteClient(fc, &discoveryv1alpha1.GroupVersion)
	if err != nil {
		klog.Error(err)
		return ctrl.Result{
			Requeue:      true,
			RequeueAfter: r.RequeueAfter,
		}, nil
	} else if foreignDiscoveryClient == nil {
		requireUpdate = true
	}

	// if join is required (both automatically or by user) and status is not set to joined
	// create new peering request
	if foreignDiscoveryClient != nil && fc.Spec.Join && !fc.Status.Outgoing.Joined {
		fc, err = r.Peer(fc, foreignDiscoveryClient)
		if err != nil {
			return ctrl.Result{
				Requeue:      true,
				RequeueAfter: r.RequeueAfter,
			}, err
		}

		monitoring.PeeringProcessExecutionCompleted(monitoring.ForeignClusterOperator)

		// expose the metric to monitor the End of the creation of the Peering Request
		monitoring.PeeringProcessEventRegister(monitoring.ForeignClusterOperator, monitoring.CreatePeeringRequest, monitoring.End)

		requireUpdate = true
	}

	// if join is no more required and status is set to joined
	// or if this foreign cluster is being deleted
	// delete peering request
	if foreignDiscoveryClient != nil && (!fc.Spec.Join || !fc.DeletionTimestamp.IsZero()) && fc.Status.Outgoing.Joined {
		fc, err = r.Unpeer(fc, foreignDiscoveryClient)
		if err != nil {
			return ctrl.Result{
				Requeue:      true,
				RequeueAfter: r.RequeueAfter,
			}, err
		}
		requireUpdate = true
	}

	if !fc.Spec.Join && !fc.Status.Outgoing.Joined && slice.ContainsString(fc.Finalizers, FinalizerString, nil) {
		fc.Finalizers = slice.RemoveString(fc.Finalizers, FinalizerString, nil)
		requireUpdate = true
	}

	if requireUpdate {
		_, err = r.Update(fc)
		if err != nil {
			klog.Error(err, err.Error())
			return ctrl.Result{
				Requeue:      true,
				RequeueAfter: r.RequeueAfter,
			}, err
		}
		klog.V(4).Infof("ForeignCluster %s successfully reconciled", fc.Name)
		return ctrl.Result{
			Requeue:      true,
			RequeueAfter: r.RequeueAfter,
		}, nil
	}

	// check if peering request really exists on foreign cluster
	if foreignDiscoveryClient != nil && fc.Spec.Join && fc.Status.Outgoing.Joined {
		_, err = r.checkJoined(fc, foreignDiscoveryClient)
		if err != nil {
			klog.Error(err, err.Error())
			return ctrl.Result{
				Requeue:      true,
				RequeueAfter: r.RequeueAfter,
			}, err
		}
	}

	klog.V(4).Infof("ForeignCluster %s successfully reconciled", fc.Name)
	return ctrl.Result{
		Requeue:      true,
		RequeueAfter: r.RequeueAfter,
	}, nil
}

func (r *ForeignClusterReconciler) Update(fc *discoveryv1alpha1.ForeignCluster) (*discoveryv1alpha1.ForeignCluster, error) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if tmp, err := r.crdClient.Resource("foreignclusters").Update(fc.Name, fc, metav1.UpdateOptions{}); err == nil {
			var ok bool
			fc, ok = tmp.(*discoveryv1alpha1.ForeignCluster)
			if !ok {
				err = goerrors.New("this object is not a ForeignCluster")
				klog.Error(err, tmp)
				return err
			}
			return nil
		} else if !errors.IsConflict(err) {
			return err
		}
		tmp, err := r.crdClient.Resource("foreignclusters").Get(fc.Name, metav1.GetOptions{})
		if err != nil {
			klog.Error(err)
			return err
		}
		fc2, ok := tmp.(*discoveryv1alpha1.ForeignCluster)
		if !ok {
			err = goerrors.New("this object is not a ForeignCluster")
			klog.Error(err, tmp)
			return err
		}
		fc.ResourceVersion = fc2.ResourceVersion
		fc.Generation = fc2.Generation
		return err
	})
	return fc, err
}

func (r *ForeignClusterReconciler) Peer(fc *discoveryv1alpha1.ForeignCluster, foreignDiscoveryClient *crdClient.CRDClient) (*discoveryv1alpha1.ForeignCluster, error) {
	// create PeeringRequest
	klog.Info("Creating PeeringRequest")
	pr, err := r.createPeeringRequestIfNotExists(fc.Name, fc, foreignDiscoveryClient)
	if err != nil {
		klog.Error(err)
		return nil, err
	}
	if pr != nil {
		fc.Status.Outgoing.Joined = true
		fc.Status.Outgoing.RemotePeeringRequestName = pr.Name
		// add finalizer
		if !slice.ContainsString(fc.Finalizers, FinalizerString, nil) {
			fc.Finalizers = append(fc.Finalizers, FinalizerString)
		}
	}
	return fc, nil
}

func (r *ForeignClusterReconciler) Unpeer(fc *discoveryv1alpha1.ForeignCluster, foreignDiscoveryClient *crdClient.CRDClient) (*discoveryv1alpha1.ForeignCluster, error) {
	// peering request has to be removed
	klog.Info("Deleting PeeringRequest")
	err := r.deletePeeringRequest(foreignDiscoveryClient, fc)
	if err != nil && !errors.IsNotFound(err) {
		klog.Error(err)
		return nil, err
	}
	// local advertisement has to be removed
	err = r.deleteAdvertisement(fc)
	if err != nil && !errors.IsNotFound(err) {
		klog.Error(err)
		return nil, err
	}
	fc.Status.Outgoing.Joined = false
	fc.Status.Outgoing.RemotePeeringRequestName = ""
	if slice.ContainsString(fc.Finalizers, FinalizerString, nil) {
		fc.Finalizers = slice.RemoveString(fc.Finalizers, FinalizerString, nil)
	}
	return fc, nil
}

func (r *ForeignClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&discoveryv1alpha1.ForeignCluster{}).
		Owns(&advtypes.Advertisement{}).
		Owns(&discoveryv1alpha1.PeeringRequest{}).
		Complete(r)
}

func (r *ForeignClusterReconciler) checkJoined(fc *discoveryv1alpha1.ForeignCluster, foreignDiscoveryClient *crdClient.CRDClient) (*discoveryv1alpha1.ForeignCluster, error) {
	_, err := foreignDiscoveryClient.Resource("peeringrequests").Get(fc.Status.Outgoing.RemotePeeringRequestName, metav1.GetOptions{})
	if err != nil {
		fc.Status.Outgoing.Joined = false
		fc.Status.Outgoing.RemotePeeringRequestName = ""
		if slice.ContainsString(fc.Finalizers, FinalizerString, nil) {
			fc.Finalizers = slice.RemoveString(fc.Finalizers, FinalizerString, nil)
		}
		fc, err = r.Update(fc)
		if err != nil {
			return nil, err
		}
	}
	return fc, nil
}

func (r *ForeignClusterReconciler) getHomeAuthUrl() (string, error) {
	address, _ := os.LookupEnv("AUTH_ADDR")

	if address == "" {
		nodes, err := r.crdClient.Client().CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			klog.Error(err)
			return "", err
		}
		if len(nodes.Items) == 0 {
			err = errors.NewNotFound(schema.GroupResource{
				Group:    apiv1.GroupName,
				Resource: "nodes",
			}, "")
			klog.Error(err)
			return "", err
		}

		node := nodes.Items[0]
		for _, addr := range node.Status.Addresses {
			if addr.Type == apiv1.NodeExternalIP || addr.Type == apiv1.NodeInternalIP {
				address = addr.Address
				break
			}
		}
	}

	if address == "" {
		err := errors.NewNotFound(schema.GroupResource{
			Group:    apiv1.GroupName,
			Resource: "nodes",
		}, "no valid ip")
		klog.Error(err)
		return "", err
	}

	port, _ := os.LookupEnv("AUTH_SVC_PORT")
	if port == "" {
		svc, err := r.crdClient.Client().CoreV1().Services(r.Namespace).Get(context.TODO(), "auth-service", metav1.GetOptions{})
		if err != nil {
			klog.Error(err)
			return "", err
		}
		if len(svc.Spec.Ports) == 0 {
			err = errors.NewNotFound(schema.GroupResource{
				Group:    apiv1.GroupName,
				Resource: string(apiv1.ResourceServices),
			}, "auth-service")
			klog.Error(err)
			return "", err
		}

		port = fmt.Sprintf("%v", svc.Spec.Ports[0].NodePort)
	}

	return fmt.Sprintf("https://%s:%v", address, port), nil
}

func (r *ForeignClusterReconciler) createPeeringRequestIfNotExists(clusterID string, owner *discoveryv1alpha1.ForeignCluster, foreignClient *crdClient.CRDClient) (*discoveryv1alpha1.PeeringRequest, error) {
	// get config to send to foreign cluster
	fConfig, err := r.getForeignConfig(clusterID, owner)
	if err != nil {
		return nil, err
	}

	localClusterID := r.clusterID.GetClusterID()

	// check if a peering request with our cluster id already exists on remote cluster
	tmp, err := foreignClient.Resource("peeringrequests").Get(localClusterID, metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) && !utils.IsUnknownAuthority(err) {
		return nil, err
	}
	if utils.IsUnknownAuthority(err) {
		klog.V(4).Info("unknown authority")
		owner.Spec.TrustMode = discoveryPkg.TrustModeUntrusted
		return nil, nil
	}
	pr, ok := tmp.(*discoveryv1alpha1.PeeringRequest)
	inf := errors.IsNotFound(err) || !ok // inf -> IsNotFound
	// if peering request does not exists or its secret was not created for some reason
	if inf || pr.Spec.KubeConfigRef == nil {
		if inf {
			// does not exist -> create new peering request
			authUrl, err := r.getHomeAuthUrl()
			if err != nil {
				return nil, err
			}
			pr = &discoveryv1alpha1.PeeringRequest{
				TypeMeta: metav1.TypeMeta{
					Kind:       "PeeringRequest",
					APIVersion: "discovery.liqo.io/v1alpha1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: localClusterID,
				},
				Spec: discoveryv1alpha1.PeeringRequestSpec{
					ClusterIdentity: discoveryv1alpha1.ClusterIdentity{
						ClusterID:   localClusterID,
						ClusterName: r.ConfigProvider.GetConfig().ClusterName,
					},
					Namespace:     r.Namespace,
					KubeConfigRef: nil,
					AuthUrl:       authUrl,
				},
			}
			tmp, err = foreignClient.Resource("peeringrequests").Create(pr, metav1.CreateOptions{})
			if err != nil {
				return nil, err
			}
			var ok bool
			pr, ok = tmp.(*discoveryv1alpha1.PeeringRequest)
			if !ok {
				return nil, goerrors.New("created object is not a ForeignCluster")
			}
		}
		secret := &apiv1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				// generate name will lead to different names every time, avoiding name collisions
				GenerateName: strings.Join([]string{"pr", r.clusterID.GetClusterID(), ""}, "-"),
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "discovery.liqo.io/v1alpha1",
						Kind:       "PeeringRequest",
						Name:       pr.Name,
						UID:        pr.UID,
					},
				},
			},
			StringData: map[string]string{
				"kubeconfig": fConfig,
			},
		}
		secret, err := foreignClient.Client().CoreV1().Secrets(r.Namespace).Create(context.TODO(), secret, metav1.CreateOptions{})
		if err != nil {
			klog.Error(err)
			// there was an error during secret creation, delete peering request
			err2 := foreignClient.Resource("peeringrequests").Delete(pr.Name, metav1.DeleteOptions{})
			if err2 != nil {
				klog.Error(err2)
				return nil, err2
			}
			return nil, err
		}
		pr.Spec.KubeConfigRef = &apiv1.ObjectReference{
			Kind:       "Secret",
			Namespace:  secret.Namespace,
			Name:       secret.Name,
			UID:        secret.UID,
			APIVersion: "v1",
		}
		pr.TypeMeta.Kind = "PeeringRequest"
		pr.TypeMeta.APIVersion = "discovery.liqo.io/v1alpha1"
		tmp, err = foreignClient.Resource("peeringrequests").Update(pr.Name, pr, metav1.UpdateOptions{})
		if err != nil {
			klog.Error(err)
			// delete peering request, also secret will be deleted by garbage collector
			err2 := foreignClient.Resource("peeringrequests").Delete(pr.Name, metav1.DeleteOptions{})
			if err2 != nil {
				klog.Error(err2)
				return nil, err2
			}
			return nil, err
		}
		pr, ok = tmp.(*discoveryv1alpha1.PeeringRequest)
		if !ok {
			return nil, goerrors.New("created object is not a PeeringRequest")
		}
		return pr, nil
	}
	// already exists
	return pr, nil
}

// this function return a kube-config file to send to foreign cluster and crate everything needed for it
func (r *ForeignClusterReconciler) getForeignConfig(clusterID string, owner *discoveryv1alpha1.ForeignCluster) (string, error) {
	_, err := r.createClusterRoleIfNotExists(clusterID, owner)
	if err != nil {
		return "", err
	}
	_, err = r.createRoleIfNotExists(clusterID, owner)
	if err != nil {
		return "", err
	}
	sa, err := r.createServiceAccountIfNotExists(clusterID, owner)
	if err != nil {
		return "", err
	}
	_, err = r.createClusterRoleBindingIfNotExists(clusterID, owner)
	if err != nil {
		return "", err
	}
	_, err = r.createRoleBindingIfNotExists(clusterID, owner)
	if err != nil {
		return "", err
	}

	// crdreplicator role binding
	err = r.setDispatcherRole(clusterID, sa)
	if err != nil {
		return "", err
	}

	// check if ServiceAccount already has a secret, wait if not
	if len(sa.Secrets) == 0 {
		wa, err := r.crdClient.Client().CoreV1().ServiceAccounts(r.Namespace).Watch(context.TODO(), metav1.ListOptions{
			FieldSelector: "metadata.name=" + clusterID,
		})
		if err != nil {
			return "", err
		}
		timeout := time.NewTimer(500 * time.Millisecond)
		ch := wa.ResultChan()
		defer timeout.Stop()
		defer wa.Stop()
		for iterate := true; iterate; {
			select {
			case s := <-ch:
				_sa := s.Object.(*apiv1.ServiceAccount)
				if _sa.Name == sa.Name && len(_sa.Secrets) > 0 {
					iterate = false
					break
				}
				break
			case <-timeout.C:
				// try to use default config
				if r.ForeignConfig != nil {
					klog.Warning("using default ForeignConfig")
					return r.ForeignConfig.String(), nil
				}
				// ServiceAccount not updated with secrets and no default config
				return "", errors.NewTimeoutError("ServiceAccount's Secret was not created", 0)
			}
		}
	}
	cnf, err := kubeconfig.CreateKubeConfig(r.crdClient.Client(), clusterID, r.Namespace)
	return cnf, err
}

func (r *ForeignClusterReconciler) createClusterRoleIfNotExists(clusterID string, owner *discoveryv1alpha1.ForeignCluster) (*rbacv1.ClusterRole, error) {
	role, err := r.crdClient.Client().RbacV1().ClusterRoles().Get(context.TODO(), clusterID, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		// does not exist
		role = &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterID,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "v1alpha1",
						Kind:       "ForeignCluster",
						Name:       owner.Name,
						UID:        owner.UID,
					},
				},
			},
			Rules: []rbacv1.PolicyRule{
				{
					Verbs:     []string{"get", "list", "create", "update", "delete", "watch"},
					APIGroups: []string{"sharing.liqo.io"},
					Resources: []string{"advertisements", "advertisements/status"},
				},
			},
		}
		return r.crdClient.Client().RbacV1().ClusterRoles().Create(context.TODO(), role, metav1.CreateOptions{})
	} else if err != nil {
		klog.Error(err)
		return nil, err
	} else {
		return role, nil
	}
}

func (r *ForeignClusterReconciler) createRoleIfNotExists(clusterID string, owner *discoveryv1alpha1.ForeignCluster) (*rbacv1.Role, error) {
	role, err := r.crdClient.Client().RbacV1().Roles(r.Namespace).Get(context.TODO(), clusterID, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		// does not exist
		role = &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterID,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "v1alpha1",
						Kind:       "ForeignCluster",
						Name:       owner.Name,
						UID:        owner.UID,
					},
				},
			},
			Rules: []rbacv1.PolicyRule{
				{
					Verbs:     []string{"get", "list", "create", "update", "delete", "watch"},
					APIGroups: []string{""},
					Resources: []string{"secrets"},
				},
			},
		}
		return r.crdClient.Client().RbacV1().Roles(r.Namespace).Create(context.TODO(), role, metav1.CreateOptions{})
	} else if err != nil {
		klog.Error(err)
		return nil, err
	} else {
		return role, nil
	}
}

func (r *ForeignClusterReconciler) createServiceAccountIfNotExists(clusterID string, owner *discoveryv1alpha1.ForeignCluster) (*apiv1.ServiceAccount, error) {
	sa, err := r.crdClient.Client().CoreV1().ServiceAccounts(r.Namespace).Get(context.TODO(), clusterID, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		// does not exist
		sa = &apiv1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterID,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "v1alpha1",
						Kind:       "ForeignCluster",
						Name:       owner.Name,
						UID:        owner.UID,
					},
				},
			},
		}
		return r.crdClient.Client().CoreV1().ServiceAccounts(r.Namespace).Create(context.TODO(), sa, metav1.CreateOptions{})
	} else if err != nil {
		klog.Error(err)
		return nil, err
	} else {
		return sa, nil
	}
}

func (r *ForeignClusterReconciler) createClusterRoleBindingIfNotExists(clusterID string, owner *discoveryv1alpha1.ForeignCluster) (*rbacv1.ClusterRoleBinding, error) {
	rb, err := r.crdClient.Client().RbacV1().ClusterRoleBindings().Get(context.TODO(), clusterID, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		// does not exist
		rb = &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterID,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "v1alpha1",
						Kind:       "ForeignCluster",
						Name:       owner.Name,
						UID:        owner.UID,
					},
				},
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      clusterID,
					Namespace: r.Namespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     clusterID,
			},
		}
		return r.crdClient.Client().RbacV1().ClusterRoleBindings().Create(context.TODO(), rb, metav1.CreateOptions{})
	} else if err != nil {
		klog.Error(err)
		return nil, err
	} else {
		return rb, nil
	}
}

func (r *ForeignClusterReconciler) createRoleBindingIfNotExists(clusterID string, owner *discoveryv1alpha1.ForeignCluster) (*rbacv1.RoleBinding, error) {
	rb, err := r.crdClient.Client().RbacV1().RoleBindings(r.Namespace).Get(context.TODO(), clusterID, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		// does not exist
		rb = &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterID,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "v1alpha1",
						Kind:       "ForeignCluster",
						Name:       owner.Name,
						UID:        owner.UID,
					},
				},
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      clusterID,
					Namespace: r.Namespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     clusterID,
			},
		}
		return r.crdClient.Client().RbacV1().RoleBindings(r.Namespace).Create(context.TODO(), rb, metav1.CreateOptions{})
	} else if err != nil {
		klog.Error(err)
		return nil, err
	} else {
		return rb, nil
	}
}

func (r *ForeignClusterReconciler) setDispatcherRole(clusterID string, sa *apiv1.ServiceAccount) error {
	_, err := r.crdClient.Client().RbacV1().ClusterRoleBindings().Get(context.TODO(), clusterID+"-crdreplicator", metav1.GetOptions{})
	if errors.IsNotFound(err) {
		// does not exist
		rb := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterID + "-crdreplicator",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "v1",
						Kind:       "ServiceAccount",
						Name:       sa.Name,
						UID:        sa.UID,
					},
				},
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      sa.Name,
					Namespace: sa.Namespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     "crdreplicator-role",
			},
		}
		_, err = r.crdClient.Client().RbacV1().ClusterRoleBindings().Create(context.TODO(), rb, metav1.CreateOptions{})
		if err != nil {
			klog.Error(err)
		}
		return err
	} else if err != nil {
		klog.Error(err)
		return err
	} else {
		return nil
	}
}

func (r *ForeignClusterReconciler) deleteAdvertisement(fc *discoveryv1alpha1.ForeignCluster) error {
	return fc.DeleteAdvertisement(r.advertisementClient)
}

func (r *ForeignClusterReconciler) deletePeeringRequest(foreignClient *crdClient.CRDClient, fc *discoveryv1alpha1.ForeignCluster) error {
	return foreignClient.Resource("peeringrequests").Delete(fc.Status.Outgoing.RemotePeeringRequestName, metav1.DeleteOptions{})
}

func (r *ForeignClusterReconciler) getAutoJoin(fc *discoveryv1alpha1.ForeignCluster) bool {
	if r.ConfigProvider == nil || r.ConfigProvider.GetConfig() == nil {
		klog.Warning("Discovery Config is not set, using default value")
		return fc.Spec.Join
	}
	return r.ConfigProvider.GetConfig().AutoJoin
}

func (r *ForeignClusterReconciler) getAutoJoinUntrusted(fc *discoveryv1alpha1.ForeignCluster) bool {
	if r.ConfigProvider == nil || r.ConfigProvider.GetConfig() == nil {
		klog.Warning("Discovery Config is not set, using default value")
		return fc.Spec.Join
	}
	return r.ConfigProvider.GetConfig().AutoJoinUntrusted
}

func (r *ForeignClusterReconciler) checkNetwork(fc *discoveryv1alpha1.ForeignCluster, requireUpdate *bool) error {
	// local NetworkConfig
	labelSelector := strings.Join([]string{crdReplicator.DestinationLabel, fc.Spec.ClusterIdentity.ClusterID}, "=")
	if err := r.updateNetwork(labelSelector, &fc.Status.Network.LocalNetworkConfig, requireUpdate); err != nil {
		klog.Error(err)
		return err
	}

	// remote NetworkConfig
	labelSelector = strings.Join([]string{crdReplicator.RemoteLabelSelector, fc.Spec.ClusterIdentity.ClusterID}, "=")
	return r.updateNetwork(labelSelector, &fc.Status.Network.RemoteNetworkConfig, requireUpdate)
}

func (r *ForeignClusterReconciler) updateNetwork(labelSelector string, resourceLink *discoveryv1alpha1.ResourceLink, requireUpdate *bool) error {
	tmp, err := r.networkClient.Resource("networkconfigs").List(metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		klog.Error(err)
		return err
	}
	ncfs, ok := tmp.(*nettypes.NetworkConfigList)
	if !ok {
		err = goerrors.New("retrieved object is not a NetworkConfig")
		klog.Error(err)
		return err
	}
	if len(ncfs.Items) == 0 && resourceLink.Available {
		// no NetworkConfigs found
		resourceLink.Available = false
		resourceLink.Reference = nil
		*requireUpdate = true
	} else if len(ncfs.Items) > 0 && !resourceLink.Available {
		// there are NetworkConfigs
		ncf := &ncfs.Items[0]
		resourceLink.Available = true
		resourceLink.Reference = &apiv1.ObjectReference{
			Kind:       "NetworkConfig",
			Name:       ncf.Name,
			UID:        ncf.UID,
			APIVersion: "v1alpha1",
		}
		*requireUpdate = true
	}
	return nil
}

func (r *ForeignClusterReconciler) checkTEP(fc *discoveryv1alpha1.ForeignCluster, requireUpdate *bool) error {
	tmp, err := r.networkClient.Resource("tunnelendpoints").List(metav1.ListOptions{
		LabelSelector: strings.Join([]string{"clusterID", fc.Spec.ClusterIdentity.ClusterID}, "="),
	})
	if err != nil {
		klog.Error(err)
		return err
	}
	teps, ok := tmp.(*nettypes.TunnelEndpointList)
	if !ok {
		err = goerrors.New("retrieved object is not a TunnelEndpoint")
		klog.Error(err)
		return err
	}
	if len(teps.Items) == 0 && fc.Status.Network.TunnelEndpoint.Available {
		// no TEP found
		fc.Status.Network.TunnelEndpoint.Available = false
		fc.Status.Network.TunnelEndpoint.Reference = nil
		*requireUpdate = true
	} else if len(teps.Items) > 0 && !fc.Status.Network.TunnelEndpoint.Available {
		// there are TEPs
		tep := &teps.Items[0]
		fc.Status.Network.TunnelEndpoint.Available = true
		fc.Status.Network.TunnelEndpoint.Reference = &apiv1.ObjectReference{
			Kind:       "TunnelEndpoints",
			Name:       tep.Name,
			UID:        tep.UID,
			APIVersion: "v1alpha1",
		}
		*requireUpdate = true
	}
	return nil
}
