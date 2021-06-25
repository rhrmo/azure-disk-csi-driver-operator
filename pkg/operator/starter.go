package operator

import (
	"context"
	"fmt"
	opCfgV1 "github.com/openshift/api/config/v1"
	cfgV1 "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resourcesynccontroller"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"time"

	"k8s.io/client-go/dynamic"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog"

	opv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/azure-disk-csi-driver-operator/assets"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/operator/csi/csicontrollerset"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivercontrollerservicecontroller"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivernodeservicecontroller"
	goc "github.com/openshift/library-go/pkg/operator/genericoperatorclient"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
)

const (
	defaultNamespace = "openshift-cluster-csi-drivers"
	operatorName     = "azure-disk-csi-driver-operator"
	operandName      = "azure-disk-csi-driver"
	infraConfigName  = "cluster"

	// ASH specific constants
	azureStackCloudFolderPrefix = "azureStackCloud/"
	openShiftConfigNamespace    = "openshift-config"
	configMapName               = "cloud-provider-config"
)

func RunOperator(ctx context.Context, controllerConfig *controllercmd.ControllerContext) error {
	// Create core clientset and informers
	kubeClient := kubeclient.NewForConfigOrDie(rest.AddUserAgent(controllerConfig.KubeConfig, operatorName))
	kubeInformersForNamespaces := v1helpers.NewKubeInformersForNamespaces(kubeClient, defaultNamespace, "", openShiftConfigNamespace)

	// Create config clientset and informer. This is used to get the cluster ID
	configClient := configclient.NewForConfigOrDie(rest.AddUserAgent(controllerConfig.KubeConfig, operatorName))
	configInformers := configinformers.NewSharedInformerFactory(configClient, 20*time.Minute)

	// Create GenericOperatorclient. This is used by the library-go controllers created down below
	gvr := opv1.SchemeGroupVersion.WithResource("clustercsidrivers")
	operatorClient, dynamicInformers, err := goc.NewClusterScopedOperatorClientWithConfigName(controllerConfig.KubeConfig, gvr, "disk.csi.azure.com")
	if err != nil {
		return err
	}

	dynamicClient, err := dynamic.NewForConfig(controllerConfig.KubeConfig)
	if err != nil {
		return err
	}

	runningOnAzureStackHub, err := runningOnAzureStackHub(ctx, configClient.ConfigV1())
	if err != nil {
		return err
	}
	if runningOnAzureStackHub {
		//controllerDeployment = azureStackCloudFolderPrefix + controllerDeployment
		//volumeSnapshotClass = azureStackCloudFolderPrefix + volumeSnapshotClass
		//nodeDaemonSet = azureStackCloudFolderPrefix + nodeDaemonSet

		klog.Infof("Detected AzureStackHub cloud infrastructure, starting endpoint config sync")
		azureStackConfigSyncer, err := newAzureStackConfigSyncer(
			operatorClient,
			kubeInformersForNamespaces,
			kubeClient,
			controllerConfig.EventRecorder)
		if err != nil {
			return err
		}
		go azureStackConfigSyncer.Run(ctx, 1)
	}

	csiControllerSet := csicontrollerset.NewCSIControllerSet(
		operatorClient,
		controllerConfig.EventRecorder,
	).WithLogLevelController().WithManagementStateController(
		operandName,
		false,
	).WithStaticResourcesController(
		"AzureDiskDriverStaticResourcesController",
		kubeClient,
		dynamicClient,
		kubeInformersForNamespaces,
		assets.ReadFile,
		[]string{
			"storageclass.yaml",
			"volumesnapshotclass.yaml",
			"controller_sa.yaml",
			"node_sa.yaml",
			"csidriver.yaml",
			"service.yaml",
			"rbac/attacher_role.yaml",
			"rbac/attacher_binding.yaml",
			"rbac/privileged_role.yaml",
			"rbac/controller_privileged_binding.yaml",
			"rbac/node_privileged_binding.yaml",
			"rbac/provisioner_role.yaml",
			"rbac/provisioner_binding.yaml",
			"rbac/resizer_role.yaml",
			"rbac/resizer_binding.yaml",
			"rbac/snapshotter_role.yaml",
			"rbac/snapshotter_binding.yaml",
			"rbac/kube_rbac_proxy_role.yaml",
			"rbac/kube_rbac_proxy_binding.yaml",
			"rbac/prometheus_role.yaml",
			"rbac/prometheus_rolebinding.yaml",
		},
	).WithCSIConfigObserverController(
		"AzureDiskDriverCSIConfigObserverController",
		configInformers,
	).WithCSIDriverControllerService(
		"AzureDiskDriverControllerServiceController",
		assets.ReadFile,
		"controller.yaml",
		kubeClient,
		kubeInformersForNamespaces.InformersFor(defaultNamespace),
		configInformers,
		nil,
		csidrivercontrollerservicecontroller.WithObservedProxyDeploymentHook(),
	).WithCSIDriverNodeService(
		"AzureDiskDriverNodeServiceController",
		assets.ReadFile,
		"node.yaml",
		kubeClient,
		kubeInformersForNamespaces.InformersFor(defaultNamespace),
		nil, // Node doesn't need to react to any changes
		csidrivernodeservicecontroller.WithObservedProxyDaemonSetHook(),
	).WithServiceMonitorController(
		"AzureDiskServiceMonitorController",
		dynamicClient,
		assets.ReadFile,
		"servicemonitor.yaml",
	)

	klog.Info("Starting the informers")
	go kubeInformersForNamespaces.Start(ctx.Done())
	go dynamicInformers.Start(ctx.Done())
	go configInformers.Start(ctx.Done())

	klog.Info("Starting controllerset")
	go csiControllerSet.Run(ctx, 1)

	<-ctx.Done()

	return fmt.Errorf("stopped")
}

func runningOnAzureStackHub(ctx context.Context, configClient cfgV1.ConfigV1Interface) (bool, error) {
	infrastructure, err := configClient.Infrastructures().Get(ctx, infraConfigName, v1.GetOptions{})
	if err != nil {
		return false, err
	}

	if infrastructure.Status.PlatformStatus != nil &&
		infrastructure.Status.PlatformStatus.Azure != nil &&
		infrastructure.Status.PlatformStatus.Azure.CloudName == opCfgV1.AzureStackCloud {
		return true, nil
	}

	return false, nil
}

func newAzureStackConfigSyncer(
	operatorClient v1helpers.OperatorClient,
	kubeInformers v1helpers.KubeInformersForNamespaces,
	kubeClient kubeclient.Interface,
	eventRecorder events.Recorder,
) (factory.Controller, error) {
	// sync config map with additional trust bundle to the operator namespace,
	// so the operator can get it as a ConfigMap volume.
	srcConfigMap := resourcesynccontroller.ResourceLocation{
		Namespace: openShiftConfigNamespace,
		Name:      configMapName,
	}
	dstConfigMap := resourcesynccontroller.ResourceLocation{
		Namespace: defaultNamespace,
		Name:      configMapName,
	}
	certController := resourcesynccontroller.NewResourceSyncController(
		operatorClient,
		kubeInformers,
		kubeClient.CoreV1(),
		kubeClient.CoreV1(),
		eventRecorder)
	err := certController.SyncConfigMap(dstConfigMap, srcConfigMap)
	if err != nil {
		return nil, err
	}
	return certController, nil
}
