// SPDX-FileCopyrightText: 2019 SAP SE or an SAP affiliate company and Gardener contributors.
//
// SPDX-License-Identifier: Apache-2.0

package scaler

import (
	"sync"

	"github.com/gardener/dependency-watchdog/pkg/multicontext"
	"github.com/gardener/dependency-watchdog/pkg/scaler/api"
	gardnerinformer "github.com/gardener/gardener/pkg/client/extensions/informers/externalversions"
	gardenerlisterv1alpha1 "github.com/gardener/gardener/pkg/client/extensions/listers/extensions/v1alpha1"
	"github.com/prometheus/client_golang/prometheus"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listerappsv1 "k8s.io/client-go/listers/apps/v1"
	listerv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/scale"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	componentbaseconfig "k8s.io/component-base/config/v1alpha1"
)

// Controller looks at ServiceDependants and reconciles the dependantPods once the service becomes available.
type Controller struct {
	client                 kubernetes.Interface
	mapper                 apimeta.RESTMapper
	scalesGetter           scale.ScalesGetter
	informerFactory        informers.SharedInformerFactory
	secretsInformer        cache.SharedIndexInformer
	secretsLister          listerv1.SecretLister
	clusterInformerFactory gardnerinformer.SharedInformerFactory
	clusterInformer        cache.SharedIndexInformer
	clusterLister          gardenerlisterv1alpha1.ClusterLister
	deploymentsInformer    cache.SharedIndexInformer
	deploymentsLister      listerappsv1.DeploymentLister
	workqueue              workqueue.RateLimitingInterface
	hasSecretsSynced       cache.InformerSynced
	hasClustersSynced      cache.InformerSynced
	hasDeploymentsSynced   cache.InformerSynced
	stopCh                 <-chan struct{}
	probeDependantsList    *api.ProbeDependantsList
	probers                map[string]*prober // the key is <namespace>/<probeDependents.Name>
	mux                    sync.Mutex
	*multicontext.Multicontext
	// LeaderElection defines the configuration of leader election client.
	LeaderElection componentbaseconfig.LeaderElectionConfiguration
}

const (
	dwdNamespace        = "dwd"
	subsystemAggregate  = "aggr"
	labelResult         = "result"
	resultSuccess       = "success"
	resultFailure       = "failure"
	labelResource       = "resource"
	resourceSecrets     = "secrets"
	resourceDeployments = "deployments"
	labelVerb           = "verb"
	verbDiscovery       = "discovery"
	verbGet             = "GET"
	verbUpdate          = "UPDATE"
)

var (
	dwdProbersTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: dwdNamespace,
			Subsystem: subsystemAggregate,
			Name:      "probers_total",
			Help:      "The accumulated total number of probers started by the dependency-watchdog.",
		},
		nil,
	)

	dwdGetTargetFromCacheTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: dwdNamespace,
			Subsystem: subsystemAggregate,
			Name:      "get_from_cache_total",
			Help:      "The accumulated total number get calls done by the dependency-watchdog on the local cache.",
		},
		[]string{labelResource},
	)

	dwdInternalProbesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: dwdNamespace,
			Subsystem: subsystemAggregate,
			Name:      "internal_probes_total",
			Help:      "The accumulated total number of internal probes done by the dependency-watchdog.",
		},
		[]string{labelResult},
	)

	dwdExternalProbesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: dwdNamespace,
			Subsystem: subsystemAggregate,
			Name:      "external_probes_total",
			Help:      "The accumulated total number of external probes done by the dependency-watchdog.",
		},
		[]string{labelResult},
	)

	dwdScaleRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: dwdNamespace,
			Subsystem: subsystemAggregate,
			Name:      "scale_requests_total",
			Help:      "The accumulated total number of scale client requests made by the dependency-watchdog.",
		},
		[]string{labelVerb},
	)

	dwdThrottledScaleRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: dwdNamespace,
			Subsystem: subsystemAggregate,
			Name:      "throttled_scale_requests_total",
			Help:      "The accumulated total number of throttled scale client requests made by the dependency-watchdog.",
		},
		[]string{labelVerb},
	)
)

func init() {
	// Initialize labelled metrics
	for _, lr := range []string{resultSuccess, resultFailure} {
		dwdInternalProbesTotal.With(prometheus.Labels{labelResult: lr}).Add(0)
		dwdExternalProbesTotal.With(prometheus.Labels{labelResult: lr}).Add(0)
	}
	for _, lr := range []string{resourceSecrets, resourceDeployments} {
		dwdGetTargetFromCacheTotal.With(prometheus.Labels{labelResource: lr}).Add(0)
	}
	for _, lv := range []string{verbDiscovery, verbGet, verbUpdate} {
		dwdScaleRequestsTotal.With(prometheus.Labels{labelVerb: lv}).Add(0)
		dwdThrottledScaleRequestsTotal.With(prometheus.Labels{labelVerb: lv}).Add(0)
	}

	prometheus.MustRegister(dwdProbersTotal)
	prometheus.MustRegister(dwdGetTargetFromCacheTotal)
	prometheus.MustRegister(dwdInternalProbesTotal)
	prometheus.MustRegister(dwdExternalProbesTotal)
	prometheus.MustRegister(dwdScaleRequestsTotal)
	prometheus.MustRegister(dwdThrottledScaleRequestsTotal)
}
