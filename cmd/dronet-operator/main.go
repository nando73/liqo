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

package main

import (
	"flag"
	"fmt"
	"github.com/netgroup-polito/dronev2/api/tunnel-endpoint/v1"
	dronetOperator "github.com/netgroup-polito/dronev2/pkg/dronet-operator"
	"github.com/vishvananda/netlink"
	"k8s.io/client-go/kubernetes"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/netgroup-polito/dronev2/internal/dronet-operator"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	defaultConfig = dronetOperator.VxlanNetConfig{
		Network:    "192.168.200.0/24",
		DeviceName: "dronet",
		Port:       "4789", //IANA assigned
		Vni:        "200",
	}
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)

	_ = v1.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var runAsRouteOperator bool

	flag.StringVar(&metricsAddr, "metrics-addr", ":0", "The address the metric endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&runAsRouteOperator, "run-as-route-operator", false,
		"Runs the controller as Route-Operator, the default value is false and will run as Tunnel-Operator")
	flag.Parse()

	ctrl.SetLogger(zap.New(func(o *zap.Options) {
		o.Development = true
	}))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:             scheme,
		MetricsBindAddress: metricsAddr,
		LeaderElection:     enableLeaderElection,
		Port:               9443,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// creates the in-cluster config or uses the .kube/config file
	config := ctrl.GetConfigOrDie()

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	clientset.CoreV1().Nodes()
	// +kubebuilder:scaffold:builder
	if runAsRouteOperator {

		vxlanConfig, err := dronetOperator.ReadVxlanNetConfig(defaultConfig)
		if err != nil {
			setupLog.Error(err, "an error occured while getting the vxlan network configuration")
		}
		vxlanPort, err := strconv.Atoi(vxlanConfig.Port)
		if err != nil {
			setupLog.Error(err, "unable to convert vxlan port "+vxlanConfig.Port+" from string to int.")
		}
		err = dronetOperator.CreateVxLANInterface(clientset, vxlanConfig)
		if err != nil {
			setupLog.Error(err, "an error occurred while creating vxlan interface")
		}
		//Enable loose mode reverse path filtering on the vxlan interfaces
		err = dronetOperator.Enable_rp_filter()
		if err != nil {
			setupLog.Error(err, "an error occured while enablig loose mode reverse path filtering")
			os.Exit(3)
		}
		isGatewayNode, err := dronetOperator.IsGatewayNode(clientset)
		if err != nil {
			setupLog.Error(err, "an error occured while checking if the node is the gatewaynode")
			os.Exit(2)
		}
		//get node name
		nodeName, err := dronetOperator.GetNodeName()
		if err != nil {
			setupLog.Error(err, "an error occured while retrieving node name")
			os.Exit(4)
		}
		//get node name
		podCIDR, err := dronetOperator.GetClusterPodCIDR()
		if err != nil {
			setupLog.Error(err, "an error occured while retrieving cluster pod cidr")
			os.Exit(6)
		}
		gatewayVxlanIP, err := dronetOperator.GetGatewayVxlanIP(clientset)
		if err != nil {
			setupLog.Error(err, "unable to derive gatewayVxlanIP")
			os.Exit(5)
		}
		r := &controllers.RouteController{
			Client:                             mgr.GetClient(),
			Log:                                ctrl.Log.WithName("controllers").WithName("Route"),
			Scheme:                             mgr.GetScheme(),
			RouteOperator:                      runAsRouteOperator,
			ClientSet:                          clientset,
			RoutesPerRemoteCluster:             make(map[string][]netlink.Route),
			IsGateway:                          isGatewayNode,
			VxlanNetwork:                       vxlanConfig.Network,
			VxlanIfaceName:                     vxlanConfig.DeviceName,
			VxlanPort:                          vxlanPort,
			IPTablesRuleSpecsReferencingChains: make(map[string]dronetOperator.IPtableRule),
			IPTablesChains:                     make(map[string]dronetOperator.IPTableChain),
			IPtablesRuleSpecsPerRemoteCluster:  make(map[string][]dronetOperator.IPtableRule),
			NodeName:                           nodeName,
			GatewayVxlanIP:                     gatewayVxlanIP,
			ClusterPodCIDR:                     podCIDR,
		}
		if err = r.SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Route")
			os.Exit(1)
		}
		setupLog.Info("Starting manager as Route-Operator")
		if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
			setupLog.Error(err, "problem running manager")
			os.Exit(1)
		}
	} else {
		if err = (&controllers.TunnelController{
			Client:        mgr.GetClient(),
			Log:           ctrl.Log.WithName("controllers").WithName("TunnelEndpoint"),
			Scheme:        mgr.GetScheme(),
			RouteOperator: runAsRouteOperator,
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "TunnelEndpoint")
			os.Exit(1)
		}
		setupLog.Info("Starting manager as Tunnel-Operator")
		if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
			setupLog.Error(err, "problem running manager")
			os.Exit(1)
		}
	}
}

var shutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// SetupSignalHandler registers for SIGTERM, SIGINT. A stop channel is returned
// which is closed on one of these signals. If a second signal is caught, the program
// is terminated with exit code 1.
func SetupSignalHandler(r *controllers.RouteController) (stopCh <-chan struct{}) {
	fmt.Printf("Entering signal handler")
	stop := make(chan struct{})
	c := make(chan os.Signal, 2)
	signal.Notify(c, shutdownSignals...)
	go func() {
		<-c
		close(stop)
		<-c
		os.Exit(1) // second signal. Exit directly.
	}()
	fmt.Printf("signal intercepded")
	r.DeleteAllIPTablesChains()
	return stop
}
