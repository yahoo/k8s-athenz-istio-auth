/*
Copyright The Kubernetes Authors.

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

// Code generated by informer-gen. DO NOT EDIT.

package v1

import (
	time "time"

	athenz_v1 "github.com/yahoo/k8s-athenz-istio-auth/pkg/apis/athenz/v1"
	versioned "github.com/yahoo/k8s-athenz-istio-auth/pkg/client/clientset/versioned"
	internalinterfaces "github.com/yahoo/k8s-athenz-istio-auth/pkg/client/informers/externalversions/internalinterfaces"
	v1 "github.com/yahoo/k8s-athenz-istio-auth/pkg/client/listers/athenz/v1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	watch "k8s.io/apimachinery/pkg/watch"
	cache "k8s.io/client-go/tools/cache"
)

// AthenzDomainInformer provides access to a shared informer and lister for
// AthenzDomains.
type AthenzDomainInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() v1.AthenzDomainLister
}

type athenzDomainInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
	namespace        string
}

// NewAthenzDomainInformer constructs a new informer for AthenzDomain type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewAthenzDomainInformer(client versioned.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredAthenzDomainInformer(client, namespace, resyncPeriod, indexers, nil)
}

// NewFilteredAthenzDomainInformer constructs a new informer for AthenzDomain type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredAthenzDomainInformer(client versioned.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options meta_v1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.AthenzV1().AthenzDomains(namespace).List(options)
			},
			WatchFunc: func(options meta_v1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.AthenzV1().AthenzDomains(namespace).Watch(options)
			},
		},
		&athenz_v1.AthenzDomain{},
		resyncPeriod,
		indexers,
	)
}

func (f *athenzDomainInformer) defaultInformer(client versioned.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredAthenzDomainInformer(client, f.namespace, resyncPeriod, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, f.tweakListOptions)
}

func (f *athenzDomainInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&athenz_v1.AthenzDomain{}, f.defaultInformer)
}

func (f *athenzDomainInformer) Lister() v1.AthenzDomainLister {
	return v1.NewAthenzDomainLister(f.Informer().GetIndexer())
}
