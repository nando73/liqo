package auth_service

import (
	configv1alpha1 "github.com/liqotech/liqo/apis/config/v1alpha1"
	"github.com/liqotech/liqo/pkg/clusterConfig"
)

type authConfigProvider interface {
	GetConfig() *configv1alpha1.AuthConfig
}

func (authService *AuthServiceCtrl) GetAuthServiceConfig(kubeconfigPath string) {
	waitFirst := make(chan struct{})
	isFirst := true
	go clusterConfig.WatchConfiguration(func(configuration *configv1alpha1.ClusterConfig) {
		authService.handleConfiguration(configuration.Spec.AuthConfig)
		authService.handleDiscoveryConfiguration(configuration.Spec.DiscoveryConfig)
		if isFirst {
			isFirst = false
			close(waitFirst)
		}
	}, nil, kubeconfigPath)
	<-waitFirst
}

func (authService *AuthServiceCtrl) handleConfiguration(config configv1alpha1.AuthConfig) {
	authService.configMutex.Lock()
	defer authService.configMutex.Unlock()
	authService.config = config.DeepCopy()
}

func (authService *AuthServiceCtrl) GetConfig() *configv1alpha1.AuthConfig {
	authService.configMutex.RLock()
	defer authService.configMutex.RUnlock()
	return authService.config.DeepCopy()
}

func (authService *AuthServiceCtrl) handleDiscoveryConfiguration(config configv1alpha1.DiscoveryConfig) {
	authService.configMutex.Lock()
	defer authService.configMutex.Unlock()
	authService.discoveryConfig = config
}

func (authService *AuthServiceCtrl) GetDiscoveryConfig() configv1alpha1.DiscoveryConfig {
	authService.configMutex.RLock()
	defer authService.configMutex.RUnlock()
	return authService.discoveryConfig
}
