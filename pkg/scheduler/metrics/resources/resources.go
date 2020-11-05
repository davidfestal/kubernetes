/*
Copyright 2020 The Kubernetes Authors.

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

// Package resources provides a metrics collector that reports the
// resource consumption (requests and limits) of the pods in the cluster
// as the scheduler and kubelet would interpret it.
package resources

import (
	"net/http"
	"strconv"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/component-base/metrics"
)

type resourceLifecycleDescriptors struct {
	total *metrics.Desc
}

func (d resourceLifecycleDescriptors) Describe(ch chan<- *metrics.Desc) {
	ch <- d.total
}

type resourceMetricsDescriptors struct {
	requests resourceLifecycleDescriptors
	limits   resourceLifecycleDescriptors
}

func (d resourceMetricsDescriptors) Describe(ch chan<- *metrics.Desc) {
	d.requests.Describe(ch)
	d.limits.Describe(ch)
}

var podResourceDesc = resourceMetricsDescriptors{
	requests: resourceLifecycleDescriptors{
		total: metrics.NewDesc("kube_pod_resource_request",
			"Resources requested by workloads on the cluster, broken down by pod. This shows the resource usage the scheduler and kubelet expect per pod for resources along with the unit for the resource if any.",
			[]string{"namespace", "pod", "node", "scheduler", "priority", "resource", "unit"},
			nil,
			metrics.ALPHA,
			""),
	},
	limits: resourceLifecycleDescriptors{
		total: metrics.NewDesc("kube_pod_resource_limit",
			"Resources limit for workloads on the cluster, broken down by pod. This shows the resource usage the scheduler and kubelet expect per pod for resources along with the unit for the resource if any.",
			[]string{"namespace", "pod", "node", "scheduler", "priority", "resource", "unit"},
			nil,
			metrics.ALPHA,
			""),
	},
}

// Handler creates a collector from the provided podLister and returns an http.Handler that
// will report the requested metrics in the prometheus format. It does not include any other
// metrics.
func Handler(podLister corelisters.PodLister) http.Handler {
	collector := NewPodResourcesMetricsCollector(podLister)
	registry := metrics.NewKubeRegistry()
	registry.CustomMustRegister(collector)
	return metrics.HandlerWithReset(registry, metrics.HandlerOpts{})
}

// Check if resourceMetricsCollector implements necessary interface
var _ metrics.StableCollector = &podResourceCollector{}

// NewPodResourcesMetricsCollector registers a O(pods) cardinality metric that
// reports the current resources requested by all pods on the cluster within
// the Kubernetes resource model. Metrics are broken down by pod, node, resource,
// and phase of lifecycle. Each pod returns two series per resource - one for
// their aggregate usage (required to schedule) and one for their phase specific
// usage. This allows admins to assess the cost per resource at different phases
// of startup and compare to actual resource usage.
func NewPodResourcesMetricsCollector(podLister corelisters.PodLister) metrics.StableCollector {
	return &podResourceCollector{
		lister: podLister,
	}
}

type podResourceCollector struct {
	metrics.BaseStableCollector
	lister corelisters.PodLister
}

func (c *podResourceCollector) DescribeWithStability(ch chan<- *metrics.Desc) {
	podResourceDesc.Describe(ch)
}

func (c *podResourceCollector) CollectWithStability(ch chan<- metrics.Metric) {
	pods, err := c.lister.List(labels.Everything())
	if err != nil {
		return
	}
	reuseReqs, reuseLimits := make(v1.ResourceList, 4), make(v1.ResourceList, 4)
	for _, p := range pods {
		_, reqs, _, limits, _, terminal := podRequestsAndLimitsByLifecycle(p, reuseReqs, reuseLimits)
		if terminal {
			// terminal pods are excluded from resource usage calculations
			continue
		}
		for _, t := range []struct {
			desc    resourceLifecycleDescriptors
			total   v1.ResourceList
			running v1.ResourceList
		}{
			{
				desc:  podResourceDesc.requests,
				total: reqs,
			},
			{
				desc:  podResourceDesc.limits,
				total: limits,
			},
		} {
			for resourceName, val := range t.total {
				var unitName string
				switch resourceName {
				case v1.ResourceCPU:
					unitName = "cores"
				case v1.ResourceMemory:
					unitName = "bytes"
				case v1.ResourceStorage:
					unitName = "bytes"
				case v1.ResourceEphemeralStorage:
					unitName = "bytes"
				default:
					switch {
					case isHugePageResourceName(resourceName):
						unitName = "bytes"
					case isAttachableVolumeResourceName(resourceName):
						unitName = "integer"
					}
				}
				var priority string
				if p.Spec.Priority != nil {
					priority = strconv.FormatInt(int64(*p.Spec.Priority), 10)
				}
				recordMetricWithUnit(ch, t.desc.total, resourceName, unitName, val, p.Namespace, p.Name, p.Spec.NodeName, p.Spec.SchedulerName, priority)
			}
		}
	}
}

func recordMetricWithUnit(
	ch chan<- metrics.Metric,
	desc *metrics.Desc,
	resourceName v1.ResourceName,
	unit string,
	val resource.Quantity,
	namespace, name, nodeName, schedulerName, priority string,
) {
	if val.IsZero() {
		return
	}
	ch <- metrics.NewLazyConstMetric(desc, metrics.GaugeValue,
		val.AsApproximateFloat64(),
		namespace, name, nodeName, schedulerName, priority, string(resourceName), unit,
	)
}

// addResourceList adds the resources in newList to list
func addResourceList(list, newList v1.ResourceList) {
	for name, quantity := range newList {
		if value, ok := list[name]; !ok {
			list[name] = quantity.DeepCopy()
		} else {
			value.Add(quantity)
			list[name] = value
		}
	}
}

// maxResourceList sets list to the greater of list/newList for every resource
// either list
func maxResourceList(list, new v1.ResourceList) {
	for name, quantity := range new {
		if value, ok := list[name]; !ok {
			list[name] = quantity.DeepCopy()
			continue
		} else {
			if quantity.Cmp(value) > 0 {
				list[name] = quantity.DeepCopy()
			}
		}
	}
}

// podRequestsAndLimitsByLifecycle returns a dictionary of all defined resources summed up for all
// containers of the pod. If PodOverhead feature is enabled, pod overhead is added to the
// total container resource requests and to the total container limits which have a
// non-zero quantity. If the pod is assigned to a node and non-terminal, the current requests and
// limits are returned for the pod. The caller may avoid allocations of resource lists by passing
// a requests and limits list to the function, which will be cleared before use.
func podRequestsAndLimitsByLifecycle(pod *v1.Pod, reuseReqs, reuseLimits v1.ResourceList) (lifecycle string, reqs, currentReqs, limits, currentLimits v1.ResourceList, terminal bool) {
	var initializing, running bool
	switch {
	case len(pod.Spec.NodeName) == 0:
		lifecycle = "Pending"
	case pod.Status.Phase == v1.PodSucceeded, pod.Status.Phase == v1.PodFailed:
		lifecycle = "Completed"
		terminal = true
	default:
		if len(pod.Spec.InitContainers) > 0 && !hasConditionStatus(pod.Status.Conditions, v1.PodInitialized, v1.ConditionTrue) {
			lifecycle = "Initializing"
			initializing = true
		} else {
			lifecycle = "Running"
			running = true
		}
	}
	if terminal {
		return
	}

	// attempt to reuse the maps if passed, or allocate otherwise
	if reuseReqs != nil {
		reqs = reuseReqs
		for k := range reqs {
			delete(reqs, k)
		}
	} else {
		reqs = make(v1.ResourceList, 4)
	}
	if reuseLimits != nil {
		limits = reuseLimits
		for k := range limits {
			delete(limits, k)
		}
	} else {
		limits = make(v1.ResourceList, 4)
	}

	for _, container := range pod.Spec.Containers {
		addResourceList(reqs, container.Resources.Requests)
		addResourceList(limits, container.Resources.Limits)
	}
	// init containers define the minimum of any resource
	for _, container := range pod.Spec.InitContainers {
		maxResourceList(reqs, container.Resources.Requests)
		maxResourceList(limits, container.Resources.Limits)
	}

	// if PodOverhead feature is supported, add overhead for running a pod
	// to the sum of reqeuests and to non-zero limits:
	if pod.Spec.Overhead != nil {
		addResourceList(reqs, pod.Spec.Overhead)
		for name, quantity := range pod.Spec.Overhead {
			if value, ok := limits[name]; ok && !value.IsZero() {
				value.Add(quantity)
				limits[name] = value
			}
		}
	}

	if initializing || running {
		currentReqs = reqs
		currentLimits = limits
	}

	return
}

func hasConditionStatus(conditions []v1.PodCondition, name v1.PodConditionType, status v1.ConditionStatus) bool {
	for _, condition := range conditions {
		if condition.Type != name {
			continue
		}
		return condition.Status == status
	}
	return false
}

func isHugePageResourceName(name v1.ResourceName) bool {
	return strings.HasPrefix(string(name), v1.ResourceHugePagesPrefix)
}

func isAttachableVolumeResourceName(name v1.ResourceName) bool {
	return strings.HasPrefix(string(name), v1.ResourceAttachableVolumesPrefix)
}
