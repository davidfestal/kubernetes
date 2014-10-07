/*
Copyright 2014 Google Inc. All rights reserved.

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

package controller

import (
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/client"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/labels"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/util"
	"github.com/golang/glog"
)

// ReplicationManager is responsible for synchronizing ReplicationController objects stored
// in the system with actual running pods.
type ReplicationManager struct {
	kubeClient client.Interface
	podControl PodControlInterface
	syncTime   <-chan time.Time

	// To allow injection of syncReplicationController for testing.
	syncHandler func(controllerSpec api.ReplicationController) error
}

// PodControlInterface is an interface that knows how to add or delete pods
// created as an interface to allow testing.
type PodControlInterface interface {
	// createReplica creates new replicated pods according to the spec.
	createReplica(ctx api.Context, controllerSpec api.ReplicationController)
	// deletePod deletes the pod identified by podID.
	deletePod(ctx api.Context, podID string) error
}

// RealPodControl is the default implementation of PodControllerInterface.
type RealPodControl struct {
	kubeClient client.Interface
}

func (r RealPodControl) createReplica(ctx api.Context, controllerSpec api.ReplicationController) {
	labels := controllerSpec.Spec.PodTemplate.Labels
	// TODO: don't fail to set this label just because the map isn't created.
	if labels != nil {
		labels["replicationController"] = controllerSpec.ID
	}
	pod := &api.Pod{
		Spec:   controllerSpec.Spec.PodTemplate.Spec,
		Labels: controllerSpec.Spec.PodTemplate.Labels,
	}
	_, err := r.kubeClient.CreatePod(ctx, pod)
	if err != nil {
		glog.Errorf("%#v\n", err)
	}
}

func (r RealPodControl) deletePod(ctx api.Context, podID string) error {
	return r.kubeClient.DeletePod(ctx, podID)
}

// NewReplicationManager creates a new ReplicationManager.
func NewReplicationManager(kubeClient client.Interface) *ReplicationManager {
	rm := &ReplicationManager{
		kubeClient: kubeClient,
		podControl: RealPodControl{
			kubeClient: kubeClient,
		},
	}
	rm.syncHandler = rm.syncReplicationController
	return rm
}

// Run begins watching and syncing.
func (rm *ReplicationManager) Run(period time.Duration) {
	rm.syncTime = time.Tick(period)
	resourceVersion := uint64(0)
	go util.Forever(func() { rm.watchControllers(&resourceVersion) }, period)
}

// resourceVersion is a pointer to the resource version to use/update.
func (rm *ReplicationManager) watchControllers(resourceVersion *uint64) {
	ctx := api.NewContext()
	watching, err := rm.kubeClient.WatchReplicationControllers(
		ctx,
		labels.Everything(),
		labels.Everything(),
		*resourceVersion,
	)
	if err != nil {
		glog.Errorf("Unexpected failure to watch: %v", err)
		time.Sleep(5 * time.Second)
		return
	}

	for {
		select {
		case <-rm.syncTime:
			rm.synchronize()
		case event, open := <-watching.ResultChan():
			if !open {
				// watchChannel has been closed, or something else went
				// wrong with our etcd watch call. Let the util.Forever()
				// that called us call us again.
				return
			}
			glog.V(4).Infof("Got watch: %#v", event)
			rc, ok := event.Object.(*api.ReplicationController)
			if !ok {
				glog.Errorf("unexpected object: %#v", event.Object)
				continue
			}
			// If we get disconnected, start where we left off.
			*resourceVersion = rc.ResourceVersion + 1
			// Sync even if this is a deletion event, to ensure that we leave
			// it in the desired state.
			glog.V(4).Infof("About to sync from watch: %v", rc.ID)
			rm.syncHandler(*rc)
		}
	}
}

func (rm *ReplicationManager) filterActivePods(pods []api.Pod) []api.Pod {
	var result []api.Pod
	for _, value := range pods {
		if api.PodTerminated != value.Status.Condition {
			result = append(result, value)
		}
	}
	return result
}

func (rm *ReplicationManager) syncReplicationController(controllerSpec api.ReplicationController) error {
	s := labels.Set(controllerSpec.Spec.Selector).AsSelector()
	ctx := api.WithNamespace(api.NewContext(), controllerSpec.Namespace)
	podList, err := rm.kubeClient.ListPods(ctx, s)
	if err != nil {
		return err
	}
	filteredList := rm.filterActivePods(podList.Items)
	diff := len(filteredList) - controllerSpec.Spec.Replicas
	if diff < 0 {
		diff *= -1
		wait := sync.WaitGroup{}
		wait.Add(diff)
		glog.V(2).Infof("Too few replicas, creating %d\n", diff)
		for i := 0; i < diff; i++ {
			go func() {
				defer wait.Done()
				rm.podControl.createReplica(ctx, controllerSpec)
			}()
		}
		wait.Wait()
	} else if diff > 0 {
		glog.V(2).Infof("Too many replicas, deleting %d\n", diff)
		wait := sync.WaitGroup{}
		wait.Add(diff)
		for i := 0; i < diff; i++ {
			go func(ix int) {
				defer wait.Done()
				rm.podControl.deletePod(ctx, filteredList[ix].ID)
			}(i)
		}
		wait.Wait()
	}
	return nil
}

func (rm *ReplicationManager) synchronize() {
	// TODO: remove this method completely and rely on the watch.
	// Add resource version tracking to watch to make this work.
	var controllerSpecs []api.ReplicationController
	ctx := api.NewContext()
	list, err := rm.kubeClient.ListReplicationControllers(ctx, labels.Everything())
	if err != nil {
		glog.Errorf("Synchronization error: %v (%#v)", err, err)
		return
	}
	controllerSpecs = list.Items
	wg := sync.WaitGroup{}
	wg.Add(len(controllerSpecs))
	for ix := range controllerSpecs {
		go func(ix int) {
			defer wg.Done()
			glog.V(4).Infof("periodic sync of %v", controllerSpecs[ix].ID)
			err := rm.syncHandler(controllerSpecs[ix])
			if err != nil {
				glog.Errorf("Error synchronizing: %#v", err)
			}
		}(ix)
	}
	wg.Wait()
}
