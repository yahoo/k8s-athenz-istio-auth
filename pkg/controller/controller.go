// Copyright 2018, Oath Inc.
// Licensed under the terms of the 3-Clause BSD license. See LICENSE file in github.com/yahoo/k8s-athenz-istio-auth
// for terms.
package controller

import (
	"log"
	"strings"
	"time"

	"istio.io/istio/pilot/pkg/config/kube/crd"
	"istio.io/istio/pilot/pkg/model"
	"istio.io/istio/pilot/pkg/serviceregistry/kube"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/yahoo/k8s-athenz-istio-auth/pkg/istio/onboarding"
	"github.com/yahoo/k8s-athenz-istio-auth/pkg/istio/servicerole"
	"github.com/yahoo/k8s-athenz-istio-auth/pkg/istio/servicerolebinding"
	"github.com/yahoo/k8s-athenz-istio-auth/pkg/util"
	"github.com/yahoo/k8s-athenz-istio-auth/pkg/zms"
)

const (
	queueNumRetries = 3
	queueKey        = "sync"
)

type Controller struct {
	pollInterval         time.Duration
	dnsSuffix            string
	srMgr                *servicerole.ServiceRoleMgr
	srbMgr               *servicerolebinding.ServiceRoleBindingMgr
	namespaceIndexer     cache.Indexer
	namespaceInformer    cache.Controller
	store                model.ConfigStoreCache
	crcController        *onboarding.Controller
	serviceIndexInformer cache.SharedIndexInformer
	queue                workqueue.RateLimitingInterface
}

// getNamespaces is responsible for retrieving the namespaces currently in the indexer
func (c *Controller) getNamespaces() *v1.NamespaceList {
	namespaceList := v1.NamespaceList{}
	nList := c.namespaceIndexer.List()

	for _, n := range nList {
		namespace, ok := n.(*v1.Namespace)
		if !ok {
			log.Println("Namespace cast failed")
			continue
		}

		namespaceList.Items = append(namespaceList.Items, *namespace)
	}

	return &namespaceList
}

// sync will be ran at every poll interval and will be responsible for the following:
// 1. Get the current ServiceRoles and ServiceRoleBindings on the cluster.
// 2. Call every Athenz domain which has a corresponding namespace in the cluster.
// 3. For every role name prefixed with service.role, call its corresponding policy in order to get the actions defined.
// 4. Each role / policy combo will create or update the associated ServiceRole if there were any changes.
// 5. The members of the role will be used to create or update the ServiceRoleBindings if there were any changes.
// 6. Delete any ServiceRoles or ServiceRoleBindings which do not have a corresponding Athenz mapping.
func (c *Controller) sync() error {
	serviceRoleMap, err := c.srMgr.GetServiceRoleMap()
	if err != nil {
		return err
	}
	log.Println("serviceRoleMap:", serviceRoleMap)

	serviceRoleBindingMap, err := c.srbMgr.GetServiceRoleBindingMap()
	if err != nil {
		return err
	}
	log.Println("serviceRoleBindingMap:", serviceRoleBindingMap)

	domainMap := make(map[string]*zms.Domain)
	errDomainMap := make(map[string]bool)
	namespaceList := c.getNamespaces()
	for _, namespace := range namespaceList.Items {
		domainName := util.NamespaceToDomain(namespace.Name)
		domain, err := zms.GetServiceMapping(domainName)
		if err != nil {
			log.Println(err)
			errDomainMap[domainName] = true
			continue
		}
		domainMap[domainName] = domain
	}
	log.Println("domainMap:", domainMap)

	for domainName, domain := range domainMap {
		for _, role := range domain.Roles {
			// ex: service.role.domain.service
			namespace := util.DomainToNamespace(domainName)
			roleName := strings.TrimPrefix(string(role.Role.Name), domainName+":role.service.role.")
			serviceRole, exists := serviceRoleMap[roleName+"-"+namespace]
			if !exists {
				log.Println("Service role", roleName, "does not exist, creating...")
				err := c.srMgr.CreateServiceRole(namespace, c.dnsSuffix, roleName, role.Policy)
				if err != nil {
					log.Println("Error creating service role:", err)
					continue
				}
				log.Println("Created service role", roleName, "in namespace", namespace)
				continue
			}

			log.Println("Service role", roleName, "already exists, updating...")
			serviceRole.Processed = true
			updated, err := c.srMgr.UpdateServiceRole(serviceRole.ServiceRole, c.dnsSuffix, roleName, role.Policy)
			if err != nil {
				log.Println("Error updating service role:", err)
				continue
			}
			if updated {
				log.Println("Updated service role", roleName, "in namespace", namespace)
			} else {
				log.Println("No difference found for service role", roleName, "in namespace", namespace,
					"not updating")
			}

			if len(role.Role.Members) == 0 {
				log.Println("Role", roleName, "has no members, skipping service role binding creation")
				continue
			}

			serviceRoleBinding, exists := serviceRoleBindingMap[roleName+"-"+namespace]
			if !exists {
				err = c.srbMgr.CreateServiceRoleBinding(namespace, roleName, role.Role.Members)
				if err != nil {
					log.Println("Error creating service role binding:", err)
					continue
				}
				log.Println("Created service role binding", roleName, "in namespace", namespace)
				continue
			}
			log.Println("Service role binding", roleName, "already exists, updating...")
			serviceRoleBinding.Processed = true
			updated, err = c.srbMgr.UpdateServiceRoleBinding(serviceRoleBinding.ServiceRoleBinding,
				namespace, roleName, role.Role.Members)
			if err != nil {
				log.Println("Error updating service role binding:", err)
				continue
			}

			if updated {
				log.Println("Updated service role binding", roleName, "in namespace", namespace)
			} else {
				log.Println("No difference found for service role binding", roleName, "in namespace", namespace,
					"not updating")
			}
		}
	}

	for _, serviceRole := range serviceRoleMap {
		domain := util.NamespaceToDomain(serviceRole.ServiceRole.Namespace)
		if _, exists := errDomainMap[domain]; exists {
			log.Println("Skipping delete for service role", serviceRole.ServiceRole.Name, "in namespace",
				serviceRole.ServiceRole.Namespace, "due to Athens error")
			continue
		}

		if !serviceRole.Processed {
			err := c.srMgr.DeleteServiceRole(serviceRole.ServiceRole.Name, serviceRole.ServiceRole.Namespace)
			if err != nil {
				log.Println("Error deleting service role:", err)
				continue
			}
			log.Println("Deleted service role", serviceRole.ServiceRole.Name, "in namespace",
				serviceRole.ServiceRole.Namespace)
		}
	}

	for _, serviceRoleBinding := range serviceRoleBindingMap {
		domain := util.NamespaceToDomain(serviceRoleBinding.ServiceRoleBinding.Namespace)
		if _, exists := errDomainMap[domain]; exists {
			log.Println("Skipping delete for service role binding", serviceRoleBinding.ServiceRoleBinding.Name,
				"in namespace", serviceRoleBinding.ServiceRoleBinding.Namespace, "due to Athens error")
			continue
		}

		if !serviceRoleBinding.Processed {
			err := c.srbMgr.DeleteServiceRoleBinding(serviceRoleBinding.ServiceRoleBinding.Name,
				serviceRoleBinding.ServiceRoleBinding.Namespace)
			if err != nil {
				log.Println("Error deleting service role binding:", err)
				continue
			}
			log.Println("Deleted service role binding", serviceRoleBinding.ServiceRoleBinding.Name, "in namespace",
				serviceRoleBinding.ServiceRoleBinding.Namespace)
		}
	}

	return nil
}

// NewController is responsible for creating the main controller object and
// all of its dependencies:
// 1. Istio custom resource client for service role, service role bindings, and clusterrbacconfig
// 2. Istio custom resource store for service role, service role bindings, and clusterrbacconfig
// 3. srMgr responsible for creating / updating / deleting service role objects based on Athenz data
// 4. srbMgr responsible for creating / updating / deleting service role binding objects based on Athenz data
// 5. crcMgr responsible creating / updating / deleting the clusterrbacconfig object based on a service label
// 6. Kubernetes clientset
// 7. Namespace informer / indexer
// 8. Service informer
func NewController(pollInterval time.Duration, dnsSuffix string, istioClient *crd.Client, k8sClient kubernetes.Interface) *Controller {
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	store := crd.NewController(istioClient, kube.ControllerOptions{})
	srMgr := servicerole.NewServiceRoleMgr(store)
	srbMgr := servicerolebinding.NewServiceRoleBindingMgr(store)

	// TODO, handle resync if object gets modified
	store.RegisterEventHandler(model.ServiceRole.Type, srMgr.EventHandler)
	store.RegisterEventHandler(model.ServiceRoleBinding.Type, srbMgr.EventHandler)

	namespaceListWatch := cache.NewListWatchFromClient(k8sClient.CoreV1().RESTClient(), "namespaces",
		v1.NamespaceAll, fields.Everything())
	namespaceIndexer, namespaceInformer := cache.NewIndexerInformer(namespaceListWatch, &v1.Namespace{}, 0,
		cache.ResourceEventHandlerFuncs{}, cache.Indexers{})

	serviceListWatch := cache.NewListWatchFromClient(k8sClient.CoreV1().RESTClient(), "services", v1.NamespaceAll, fields.Everything())
	serviceIndexInformer := cache.NewSharedIndexInformer(serviceListWatch, &v1.Service{}, 0, nil)
	crcController := onboarding.NewController(store, dnsSuffix, serviceIndexInformer)

	return &Controller{
		pollInterval:         pollInterval,
		dnsSuffix:            dnsSuffix,
		srMgr:                srMgr,
		srbMgr:               srbMgr,
		namespaceIndexer:     namespaceIndexer,
		namespaceInformer:    namespaceInformer,
		serviceIndexInformer: serviceIndexInformer,
		store:                store,
		crcController:        crcController,
		queue:                queue,
	}
}

// Run starts the main controller loop running sync at every poll interval. It
// also starts the following controller dependencies:
// 1. Service informer
// 2. Namespace informer
// 3. Istio custom resource informer
func (c *Controller) Run(stop chan struct{}) {
	go c.serviceIndexInformer.Run(stop)
	go c.crcController.Run(stop)
	go c.namespaceInformer.Run(stop)
	go c.store.Run(stop)

	if !cache.WaitForCacheSync(stop, c.store.HasSynced, c.namespaceInformer.HasSynced, c.serviceIndexInformer.HasSynced) {
		log.Panicln("Timed out waiting for namespace cache to sync.")
	}

	defer c.queue.ShutDown()
	go wait.Until(c.runWorker, 0, stop)

	// TODO, change once athenz domain cr watch is in place, add individual
	// items to queue
	for {
		c.queue.Add(queueKey)
		log.Println("Sleeping for", c.pollInterval)
		time.Sleep(c.pollInterval)
	}
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

	// TODO, change to new sync function
	err := c.sync()
	if err != nil {
		log.Printf("Error syncing athenz state for key %s: %s", key, err)
		if c.queue.NumRequeues(key) < queueNumRetries {
			log.Printf("Retrying key %s due to sync error", key)
			c.queue.AddRateLimited(key)
			return true
		}
	}

	c.queue.Forget(key)
	return true
}
