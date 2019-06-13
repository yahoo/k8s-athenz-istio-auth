package onboarding

import (
	"errors"
	"log"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"istio.io/api/rbac/v1alpha1"
	"istio.io/istio/pilot/pkg/model"
)

const (
	queueNumRetries        = 3
	authzEnabled           = "true"
	authzEnabledAnnotation = "authz.istio.io/enabled"
	queueKey               = model.DefaultRbacConfigName + "/" + v1.NamespaceDefault
)

type Controller struct {
	store                model.ConfigStoreCache
	dnsSuffix            string
	serviceIndexInformer cache.SharedIndexInformer
	queue                workqueue.RateLimitingInterface
}

// NewController initializes the Controller object and its dependencies
func NewController(store model.ConfigStoreCache, dnsSuffix string, serviceIndexInformer cache.SharedIndexInformer) *Controller {
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	c := &Controller{
		store:                store,
		dnsSuffix:            dnsSuffix,
		serviceIndexInformer: serviceIndexInformer,
		queue:                queue,
	}

	serviceIndexInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(_ interface{}) {
			c.queue.Add(queueKey)
		},
		UpdateFunc: func(_ interface{}, _ interface{}) {
			c.queue.Add(queueKey)
		},
		DeleteFunc: func(_ interface{}) {
			c.queue.Add(queueKey)
		},
	})

	return c
}

// Run starts the worker thread
func (c *Controller) Run(stop chan struct{}) {
	defer c.queue.ShutDown()
	go wait.Until(c.runWorker, 0, stop)
	<-stop
}

// runWorker calls processNextItem to process events of the work queue
func (c *Controller) runWorker() {
	for c.processNextItem() {
	}
}

// processNextItem takes an item off the queue and calls the controllers sync
// function, handles the logic of requeuing in case any errors occur
func (c *Controller) processNextItem() bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}

	defer c.queue.Done(key)

	err := c.sync()
	if err != nil {
		log.Printf("Error syncing cluster rbac config for key %s: %s", key, err)
		if c.queue.NumRequeues(key) < queueNumRetries {
			log.Printf("Retrying key %s due to sync error", key)
			c.queue.AddRateLimited(key)
			return true
		}
	}

	c.queue.Forget(key)
	return true
}

// addService will add a service to the ClusterRbacConfig object
func addServices(services []string, clusterRbacConfig *v1alpha1.RbacConfig) {
	if clusterRbacConfig == nil || clusterRbacConfig.Inclusion == nil {
		return
	}

	for _, service := range services {
		clusterRbacConfig.Inclusion.Services = append(clusterRbacConfig.Inclusion.Services, service)
	}
}

// deleteService will delete a service from the ClusterRbacConfig object
func deleteServices(services []string, clusterRbacConfig *v1alpha1.RbacConfig) {
	if clusterRbacConfig == nil || clusterRbacConfig.Inclusion == nil {
		return
	}

	for _, service := range services {
		var indexToRemove = -1
		for i, svc := range clusterRbacConfig.Inclusion.Services {
			if svc == service {
				indexToRemove = i
				break
			}
		}

		if indexToRemove != -1 {
			clusterRbacConfig.Inclusion.Services = removeIndexElement(clusterRbacConfig.Inclusion.Services, indexToRemove)
		}
	}
}

// createClusterRbacSpec creates the rbac config object with the inclusion field
func createClusterRbacSpec(services []string) *v1alpha1.RbacConfig {
	return &v1alpha1.RbacConfig{
		Mode: v1alpha1.RbacConfig_ON_WITH_INCLUSION,
		Inclusion: &v1alpha1.RbacConfig_Target{
			Services: services,
		},
		Exclusion: nil,
	}
}

// createClusterRbacConfig creates the ClusterRbacConfig model config object
func createClusterRbacConfig(services []string) model.Config {
	return model.Config{
		ConfigMeta: model.ConfigMeta{
			Type:    model.ClusterRbacConfig.Type,
			Name:    model.DefaultRbacConfigName,
			Group:   model.ClusterRbacConfig.Group + model.IstioAPIGroupDomain,
			Version: model.ClusterRbacConfig.Version,
		},
		Spec: createClusterRbacSpec(services),
	}
}

// getOnboardedServiceList extracts all services from the indexer with the authz
// annotation set to true.
func (c *Controller) getOnboardedServiceList() []string {
	cacheServiceList := c.serviceIndexInformer.GetIndexer().List()
	serviceList := make([]string, 0)

	for _, service := range cacheServiceList {
		svc, ok := service.(*v1.Service)
		if !ok {
			log.Println("Could not cast to service object, skipping service list addition...")
			continue
		}

		key, exists := svc.Annotations[authzEnabledAnnotation]
		if exists && key == authzEnabled {
			serviceName := svc.Name + "." + svc.Namespace + "." + c.dnsSuffix
			serviceList = append(serviceList, serviceName)
		}
	}

	return serviceList
}

// sync decides whether to create / update / delete the ClusterRbacConfig
// object based on the current onboarded services in the cluster
func (c *Controller) sync() error {
	serviceList := c.getOnboardedServiceList()
	config := c.store.Get(model.ClusterRbacConfig.Type, model.DefaultRbacConfigName, "")
	if config == nil && len(serviceList) == 0 {
		log.Println("Service list is empty and cluster rbac config does not exist, skipping sync...")
		return nil
	}

	if config == nil {
		log.Println("Creating cluster rbac config...")
		_, err := c.store.Create(createClusterRbacConfig(serviceList))
		return err
	}

	clusterRbacConfig, ok := config.Spec.(*v1alpha1.RbacConfig)
	if !ok {
		return errors.New("Could not cast to ClusterRbacConfig")
	}

	needsUpdate := false
	if clusterRbacConfig.Inclusion == nil || clusterRbacConfig.Mode != v1alpha1.RbacConfig_ON_WITH_INCLUSION {
		log.Println("ClusterRBacConfig inclusion field is nil or ON_WITH_INCLUSION mode is not set, syncing...")
		clusterRbacConfig = createClusterRbacSpec(serviceList)
		needsUpdate = true
	}

	newServices := compareServiceLists(serviceList, clusterRbacConfig.Inclusion.Services)
	if len(newServices) > 0 {
		addServices(newServices, clusterRbacConfig)
		needsUpdate = true
	}

	oldServices := compareServiceLists(clusterRbacConfig.Inclusion.Services, serviceList)
	if len(oldServices) > 0 {
		deleteServices(oldServices, clusterRbacConfig)
		needsUpdate = true
	}

	if len(clusterRbacConfig.Inclusion.Services) == 0 {
		log.Println("Deleting cluster rbac config...")
		return c.store.Delete(model.ClusterRbacConfig.Type, model.DefaultRbacConfigName, v1.NamespaceDefault)
	}

	if needsUpdate {
		log.Println("Updating cluster rbac config...")
		_, err := c.store.Update(model.Config{
			ConfigMeta: config.ConfigMeta,
			Spec:       clusterRbacConfig,
		})
		return err
	}

	log.Println("Sync state is current, no changes needed...")
	return nil
}

func (c *Controller) EventHandler(config model.Config, e model.Event) {
	log.Printf("Received %s event for cluster rbac config: %+v", e.String(), config)
	c.queue.Add(queueKey)
}

// compareServices returns a list of which items in list A are not in list B
func compareServiceLists(serviceListA, serviceListB []string) []string {
	serviceMapB := make(map[string]bool, len(serviceListB))
	for _, item := range serviceListB {
		serviceMapB[item] = true
	}

	serviceListDiff := make([]string, 0)
	for _, item := range serviceListA {
		if _, exists := serviceMapB[item]; !exists {
			serviceListDiff = append(serviceListDiff, item)
		}
	}

	return serviceListDiff
}

// removeIndexElement removes an element from an array at the given index
func removeIndexElement(serviceList []string, indexToRemove int) []string {
	if indexToRemove > len(serviceList) || indexToRemove < 0 {
		return serviceList
	}

	serviceList[len(serviceList)-1],
		serviceList[indexToRemove] = serviceList[indexToRemove],
		serviceList[len(serviceList)-1]
	return serviceList[:len(serviceList)-1]
}