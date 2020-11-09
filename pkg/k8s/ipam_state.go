// Copyright 2019 Ctrip Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

const (
	KIND_STATEFULSET         = "StatefulSet"
	API_VERSION_STS          = "apps/v1"
	API_VERSION_ADVANCED_STS = "apps.kruise.io/v1alpha1"
)

func getPodInfo(ns, podName string) (string, string, string, error) {
	pod, err := Client().CoreV1().Pods(ns).Get(context.TODO(), podName, metav1.GetOptions{})
	if err != nil {
		return "", "", "", err
	}

	for _, ref := range pod.OwnerReferences {
		if *ref.Controller {
			return ref.Kind, ref.Name, ref.APIVersion, nil
		}
	}

	log.Infof("Get pod info: controller not found in pod.OwnerReferences, %s/%s", ns, podName)
	return "", "", "", nil
}

// IsStsPod determines if the given pod is a sts pod by retrieving the metadata
// in k8s API
func IsStsPod(fullPodName string) (bool, error) {
	items := strings.Split(fullPodName, "/")
	if len(items) != 2 {
		log.Infof("IsStsPod: false, pod %s contains more than 1 slashes", fullPodName)
		return false, nil
	}

	ns, podName := items[0], items[1]
	kind, _, apiVersion, err := getPodInfo(ns, podName)
	if err != nil {
		return false, nil
	}

	// STS and ASTS both have kind=="StatefulSet", the APIVersion field
	// separates them.
	if kind == KIND_STATEFULSET {
		log.Infof("IsStsPod: true, pod %s, APIVersion %s\n", fullPodName, apiVersion)
		return true, nil
	}

	log.Infof("IsStsPod: false, pod %s, Kind %s, APIVersion %s", fullPodName, kind, apiVersion)
	return false, nil
}

// IsStsPodDeleted determines if the pod is deleted from the node by retrieving
// the metadata in k8s API
// pod name format: <namespace>/<stsName>-<podIndex>
func IsStsPodDeleted(nodeName string, fullPodName string) (bool, error) {
	// extract sts name
	items := strings.Split(fullPodName, "/")
	if len(items) != 2 {
		return false, fmt.Errorf("unexpected fullPodName %s", fullPodName)
	}

	ns, podName := items[0], items[1]
	i := strings.LastIndex(podName, "-")
	if i < 1 {
		return false, fmt.Errorf("mal-formed pod name %s", podName)
	}

	podIndex, err := strconv.Atoi(podName[i+1:])
	if err != nil {
		return false, err
	}

	// Get pod info
	kind, name, apiVersion, err := getPodInfo(ns, podName)
	if err != nil {
		return false, nil
	}

	if kind != KIND_STATEFULSET {
		log.Infof("IsStsPodDeleted: %s is not sts/asts", fullPodName)
		return true, nil
	}

	// Get replicas
	replicas := -1
	switch apiVersion {
	case API_VERSION_STS:
		if replicas, err = getReplicasSts(ns, name); err != nil {
			return false, err
		}
	case API_VERSION_ADVANCED_STS:
		if replicas, err = getReplicasAdvancedSts(ns, name); err != nil {
			return false, err
		}
	default:
		return false, fmt.Errorf("unsupported STS APIVersion %s", apiVersion)
	}

	log.Infof("STS %s: replicas %d, pod index %d", kind, replicas, podIndex)
	if replicas > 0 && podIndex < replicas {
		return false, nil // valid index, pod still in sts replicas
	} else {
		return true, nil // pod truly deleted from sts replicas
	}
}

func getReplicasSts(ns, name string) (int, error) {
	log.Infof("Determine if STS %s/%s still exists in k8s", ns, name)

	sts, err := Client().AppsV1().StatefulSets(ns).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			log.Infof("Get STS %s: not found", name)
			return 0, nil
		}

		log.Errorf("Get STS %s failed: %v", name, err)
		return -1, err
	}

	// If the resource is being deleted with PropagationPolicy foreground,
	// the replicas field will remain unchanged, so we need to handle such cases here.
	finalizers := (*sts).ObjectMeta.Finalizers
	if len(finalizers) > 0 {
		propagationPolicy := finalizers[0]
		if propagationPolicy == "foregroundDeletion" {
			return 0, nil
		}
	}

	return int(*sts.Spec.Replicas), nil
}

type StsSpec struct {
	Replicas *int32 `json:"replicas,omitempty"`
}

type StatefulSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec StsSpec `json:"spec,omitempty"`
}

func getReplicasAdvancedSts(ns, name string) (int, error) {
	log.Infof("Determine if ASTS %s/%s still exists in k8s", ns, name)

	c, err := createDynamicClient()
	if err != nil {
		return -1, err
	}

	resource := schema.GroupVersionResource{
		Group:    "apps.kruise.io",
		Version:  "v1alpha1",
		Resource: "statefulsets",
	}

	asts, err := c.Resource(resource).Namespace(ns).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			log.Infof("Get ASTS %s: not found", name)
			return 0, nil
		}

		log.Errorf("Get ASTS %s failed: %v", name, err)
		return -1, err
	}

	data, err := asts.MarshalJSON()
	if err != nil {
		return -1, err
	}

	var sts StatefulSet
	if err := json.Unmarshal(data, &sts); err != nil {
		return -1, err
	}

	finalizers := sts.GetFinalizers()
	ts := sts.GetDeletionTimestamp()
	if len(finalizers) > 0 && !ts.IsZero() {
		log.Infof("ASTS %s/%s is being deleting: finalizers %v, deletion timestamp %v",
			ns, name, finalizers, ts)
		return 0, nil
	}

	return int(*sts.Spec.Replicas), nil
}

func createDynamicClient() (dynamic.Interface, error) {
	config, err := CreateConfig()
	if err != nil {
		return nil, fmt.Errorf("unable to create k8s client restConfig: %s", err)
	}

	config.ContentConfig.ContentType = `application/vnd.kubernetes.protobuf`

	return dynamic.NewForConfig(config)
}

// GetNodeStsPods returns all sts pods on this node by retrieving the metadata
// in k8s API
func GetNodeStsPods(nodeName string) ([]string, error) {
	log.Infof("Filtering sts pods on node %s", nodeName)

	podNames := []string{}
	options := metav1.ListOptions{FieldSelector: "spec.nodeName=" + nodeName}
	podList, err := Client().CoreV1().Pods("").List(context.TODO(), options)
	if err != nil {
		return podNames, err
	}

	for _, p := range podList.Items {
		ns := p.ObjectMeta.Namespace
		podName := p.ObjectMeta.Name
		owners := p.ObjectMeta.OwnerReferences

		isSts := false
		for _, r := range owners {
			if r.Kind == KIND_STATEFULSET {
				podNames = append(podNames, ns+"/"+podName)
				isSts = true
				break
			}
		}

		if isSts {
			log.Infof("Pod %s is sts pod", podName)
			continue
		}

		log.Infof("Pod %s is not sts pod, owners %v\n", podName, owners)
	}

	return podNames, nil
}
