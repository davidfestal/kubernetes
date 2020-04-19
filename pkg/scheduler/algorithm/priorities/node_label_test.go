/*
Copyright 2016 The Kubernetes Authors.

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

package priorities

import (
	"reflect"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	framework "k8s.io/kubernetes/pkg/scheduler/framework/v1alpha1"
	schedulernodeinfo "k8s.io/kubernetes/pkg/scheduler/nodeinfo"
)

func TestNewNodeLabelPriority(t *testing.T) {
	label1 := map[string]string{"foo": "bar"}
	label2 := map[string]string{"bar": "foo"}
	label3 := map[string]string{"bar": "baz"}
	tests := []struct {
		nodes        []*v1.Node
		label        string
		presence     bool
		expectedList framework.NodeScoreList
		name         string
	}{
		{
			nodes: []*v1.Node{
				{ObjectMeta: metav1.ObjectMeta{Name: "machine1", Labels: label1}},
				{ObjectMeta: metav1.ObjectMeta{Name: "machine2", Labels: label2}},
				{ObjectMeta: metav1.ObjectMeta{Name: "machine3", Labels: label3}},
			},
			expectedList: []framework.NodeScore{{Name: "machine1", Score: 0}, {Name: "machine2", Score: 0}, {Name: "machine3", Score: 0}},
			label:        "baz",
			presence:     true,
			name:         "no match found, presence true",
		},
		{
			nodes: []*v1.Node{
				{ObjectMeta: metav1.ObjectMeta{Name: "machine1", Labels: label1}},
				{ObjectMeta: metav1.ObjectMeta{Name: "machine2", Labels: label2}},
				{ObjectMeta: metav1.ObjectMeta{Name: "machine3", Labels: label3}},
			},
			expectedList: []framework.NodeScore{{Name: "machine1", Score: framework.MaxNodeScore}, {Name: "machine2", Score: framework.MaxNodeScore}, {Name: "machine3", Score: framework.MaxNodeScore}},
			label:        "baz",
			presence:     false,
			name:         "no match found, presence false",
		},
		{
			nodes: []*v1.Node{
				{ObjectMeta: metav1.ObjectMeta{Name: "machine1", Labels: label1}},
				{ObjectMeta: metav1.ObjectMeta{Name: "machine2", Labels: label2}},
				{ObjectMeta: metav1.ObjectMeta{Name: "machine3", Labels: label3}},
			},
			expectedList: []framework.NodeScore{{Name: "machine1", Score: framework.MaxNodeScore}, {Name: "machine2", Score: 0}, {Name: "machine3", Score: 0}},
			label:        "foo",
			presence:     true,
			name:         "one match found, presence true",
		},
		{
			nodes: []*v1.Node{
				{ObjectMeta: metav1.ObjectMeta{Name: "machine1", Labels: label1}},
				{ObjectMeta: metav1.ObjectMeta{Name: "machine2", Labels: label2}},
				{ObjectMeta: metav1.ObjectMeta{Name: "machine3", Labels: label3}},
			},
			expectedList: []framework.NodeScore{{Name: "machine1", Score: 0}, {Name: "machine2", Score: framework.MaxNodeScore}, {Name: "machine3", Score: framework.MaxNodeScore}},
			label:        "foo",
			presence:     false,
			name:         "one match found, presence false",
		},
		{
			nodes: []*v1.Node{
				{ObjectMeta: metav1.ObjectMeta{Name: "machine1", Labels: label1}},
				{ObjectMeta: metav1.ObjectMeta{Name: "machine2", Labels: label2}},
				{ObjectMeta: metav1.ObjectMeta{Name: "machine3", Labels: label3}},
			},
			expectedList: []framework.NodeScore{{Name: "machine1", Score: 0}, {Name: "machine2", Score: framework.MaxNodeScore}, {Name: "machine3", Score: framework.MaxNodeScore}},
			label:        "bar",
			presence:     true,
			name:         "two matches found, presence true",
		},
		{
			nodes: []*v1.Node{
				{ObjectMeta: metav1.ObjectMeta{Name: "machine1", Labels: label1}},
				{ObjectMeta: metav1.ObjectMeta{Name: "machine2", Labels: label2}},
				{ObjectMeta: metav1.ObjectMeta{Name: "machine3", Labels: label3}},
			},
			expectedList: []framework.NodeScore{{Name: "machine1", Score: framework.MaxNodeScore}, {Name: "machine2", Score: 0}, {Name: "machine3", Score: 0}},
			label:        "bar",
			presence:     false,
			name:         "two matches found, presence false",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nodeNameToInfo := schedulernodeinfo.CreateNodeNameToInfoMap(nil, test.nodes)
			labelPrioritizer := &NodeLabelPrioritizer{
				label:    test.label,
				presence: test.presence,
			}
			list, err := priorityFunction(labelPrioritizer.CalculateNodeLabelPriorityMap, nil, nil)(nil, nodeNameToInfo, test.nodes)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			// sort the two lists to avoid failures on account of different ordering
			sortNodeScoreList(test.expectedList)
			sortNodeScoreList(list)
			if !reflect.DeepEqual(test.expectedList, list) {
				t.Errorf("expected %#v, got %#v", test.expectedList, list)
			}
		})
	}
}