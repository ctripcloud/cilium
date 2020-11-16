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

package ipam

import (
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/cilium/cilium/pkg/k8s"
	k8sConst "github.com/cilium/cilium/pkg/k8s/constants"
)

// submodule: IPAM state database
// This submodule extends the cilium IPAM by providing a persistent storage
// for storing IP address states and their metadata.
// The main purpose is to support fixed IP containers.

var EnableFixedIP bool = true

const (
	ENABLE_FIXED_IP   string = "ENABLE_FIXED_IP"
	IPAM_STATE_DIR    string = "/var/lib/ipamstate"
	LEAST_STS_POD_LEN int    = 9 // format: <ns>-<pod>:<ip>
)

// cleanStaleIpamStates cleans the stale IPAM states.
// This function is meant to be called on agent restart
func cleanStaleIpamStates(ipam *IPAM) error {
	pinnedIPs := []string{}
	pinnedOwners := []string{}
	log.Infof("Cleaning stale IPAM states")

	nodeName := os.Getenv(k8sConst.EnvNodeNameSpec)
	if nodeName == "" {
		err := fmt.Errorf("Get %s from ENV failed", k8sConst.EnvNodeNameSpec)
		log.Errorf("Clean stale IPAM states failed: %s, skip cleaning process", err)
		return err
	}

	podNames, err := k8s.GetNodeStsPods(nodeName)
	if err != nil {
		log.Errorf("Get node sts pod list from apiserver failed: %s, "+
			"skip cleaning process", err)
		return err
	}

	log.Infof("sts pods should on this node: %s", podNames)

	files, err := ioutil.ReadDir(IPAM_STATE_DIR)
	if err != nil {
		log.Errorf("Read ipam state dir failed: %s, skip cleaning process", err)
		return err
	}

	sort.Strings(podNames)
	for _, f := range files {
		name := f.Name()
		if len(name) < LEAST_STS_POD_LEN {
			log.Errorf("Clean stale IPAM states: unknown pin info %s, skip "+
				"this one", name)
			continue
		}

		podName := ""
		for _, pname := range podNames {
			// TODO: break early as we are sorted slice

			if strings.HasPrefix(name, strings.ReplaceAll(pname, "/", "-")) {
				podName = pname
				break
			}
		}

		if podName != "" {
			items := strings.Split(name, ":")
			if len(items) != 2 {
				log.Errorf("Unknown pin info: %s, skip this one", name)
				continue
			}

			// Note: better to read the owner name from file content
			// replace '-' with '/' is 100% correct for sts?
			pinnedOwners = append(pinnedOwners, strings.ReplaceAll(items[0], "-", "/"))
			pinnedIPs = append(pinnedIPs, items[1])
			log.Infof("Pod %s exists in k8s, keep state file %s", podName, name)
			continue
		}

		log.Warnf("Pod not exist in k8s, remove state file %s", name)
		if err := os.Remove(path.Join(IPAM_STATE_DIR, name)); err != nil {
			log.Errorf("Remove %s failed: %s", name, err)
		}
	}

	ipam.PinnedIPs = pinnedIPs
	ipam.PinnedOwners = pinnedOwners
	log.Infof("Clean stale IPAM states finish")
	return nil
}

func shouldPinIPForPod(fullPodName string) (bool, error) {
	items := strings.Split(fullPodName, "/")
	if len(items) != 2 {
		log.Infof("shouldPinIPForPod: false, reason: format mismatch of pod name %s", fullPodName)
		return false, nil
	}

	ns, podName := items[0], items[1]
	pod, err := k8s.GetPod(ns, podName)
	if err != nil {
		return false, fmt.Errorf("shouldPinIPForPod: false, reason: get pod failed: %v", err)
	}

	// Retrieve kind, api version
	kind := ""
	apiVersion := ""
	for _, ref := range pod.OwnerReferences {
		if *ref.Controller {
			kind = ref.Kind
			apiVersion = ref.APIVersion
			break
		}
	}

	// STS and ASTS both have kind=="StatefulSet", the APIVersion field separates them.
	if kind != k8s.KIND_STATEFULSET {
		log.Infof("shouldPinIPForPod: false, reason: %s is not STS, kind %s, APIVersion %s", fullPodName, kind, apiVersion)
		return false, nil
	}

	// Advanced statefulset
	if apiVersion == k8s.API_VERSION_ADVANCED_STS {
		if value, ok := pod.ObjectMeta.Labels[k8s.FIXED_IP_LABEL]; ok {
			if value == "true" {
				log.Infof("shouldPinIPForPod: true, reason: %s is ASTS and %s=%s", fullPodName, k8s.FIXED_IP_LABEL, value)
				return true, nil
			}

			log.Infof("shouldPinIPForPod: false, reason: %s is ASTS but %s=%s", fullPodName, k8s.FIXED_IP_LABEL, value)
			return false, nil
		}

		log.Infof("shouldPinIPForPod: false, reason: %s is ASTS but label %s not found", fullPodName, k8s.FIXED_IP_LABEL)
		return false, nil
	}

	// Vanilla statefulset
	log.Infof("shouldPinIPForPod: true, reason: %s is STS, APIVersion %s", fullPodName, apiVersion)
	return true, nil
}

// getPinnedIP returns the IP info that pinned to the given owner
// Return: <*AllocationResult>, <error>
// If not found, return nil, nil
func getPinnedIP(owner string) (*AllocationResult, error) {
	files, err := ioutil.ReadDir(IPAM_STATE_DIR)
	if err != nil {
		return nil, fmt.Errorf("ReadDir failed: %s", err)
	}

	key := strings.ReplaceAll(owner, "/", "-")
	for _, f := range files {
		filename := f.Name() // <ns>-<podname>:<ipv4>
		if len(filename) < LEAST_STS_POD_LEN {
			log.Warnf("Get pinned IP: invalid state file detected: %s", filename)
			continue
		}

		items := strings.Split(filename, ":")
		if len(items) != 2 {
			return nil, fmt.Errorf("Unknown pin info: %s", filename)
		}

		if items[0] == key {
			fullPath := path.Join(IPAM_STATE_DIR, filename)
			if content, err := ioutil.ReadFile(fullPath); err != nil {
				return nil, fmt.Errorf("Read file %s failed: %s", fullPath, err)
			} else {
				_, result, err := deserializeIPState(string(content))
				if err != nil { // deserialize error
					return nil, err
				}

				if result == nil { // legacy format IPState
					if ipv4 := net.ParseIP(items[1]); ipv4 != nil {
						result := &AllocationResult{IP: ipv4}
						return result, nil
					} else {
						return nil, fmt.Errorf("ParseIP %s failed", items[1])
					}
				} else { // new format IPState
					return result, nil
				}
			}
		}
	}

	return nil, nil
}

// return Owner, *AllocationResult, error
func deserializeIPState(content string) (string, *AllocationResult, error) {
	owner := ""
	result := &AllocationResult{}

	items := strings.Split(content, "\n")
	switch len(items) {
	case 0:
		return "", nil, fmt.Errorf("deserialize IP state: content empty")
	case 1: // legacy IPState contains only owner name
		log.Infof("Deserialize IP state (old foramt): %v", items)
		owner = items[0]
		return owner, nil, nil
	default:
		log.Infof("Deserialize IP state: %v", items)
		for _, item := range items {
			a := strings.SplitAfterN(item, " ", 2)
			if len(a) != 2 {
				return "", nil, fmt.Errorf("mal-formed ip info %s", item)
			}

			k, v := a[0], a[1]
			switch k {
			case "Owner: ":
				owner = v
			case "IP: ":
				if ipv4 := net.ParseIP(v); ipv4 != nil {
					result.IP = ipv4
				} else {
					return "", nil, fmt.Errorf("ParseIP %s failed", v)
				}
			case "CIDRs: ":
				result.CIDRs = strings.Split(v, " ")
			case "Master: ":
				result.Master = v
			case "GatewayIP: ":
				result.GatewayIP = v
			default:
				log.Errorf("deserialize IP state: unknow field: %s%s", k, v)
				return "", nil, fmt.Errorf("unknown ip info %s", item)
			}
		}
		return owner, result, nil
	}
}

// pinIP pins the IP info to the given owner
// This will save the AllocationResult and owner info to a local file
//
// FORMAT :
//         Owner: <owner>
//         IP: <ipv4>
//         CIDRs: join(cidrs, " ")
//         Master: <master>
//         GatewayIP: <gatewayIP>
//
// EXAMPLE:
//        root@node:~ # cat /var/lib/ipamstate/default-web-0\:10.30.7.14
//        Owner: default/web-0
//        IP: 10.30.7.14
//        CIDRs: 10.31.8.0/22
//        Master: 0a:54:98:94:3a:e6
//        GatewayIP: 10.30.6.1
func pinIP(result *AllocationResult, owner string) error {
	if s := result.IP.To4(); s == nil {
		return fmt.Errorf("IPState currently only supports IPv4")
	}

	sanifiedOwner := strings.ReplaceAll(owner, "/", "-")
	ipv4 := result.IP.String()
	filename := path.Join(IPAM_STATE_DIR, sanifiedOwner+":"+ipv4)

	content, err := serializeIPState(owner, result)
	if err != nil {
		return err
	}

	if err := ioutil.WriteFile(filename, content, 0644); err != nil {
		return fmt.Errorf("create file %s failed: %s", filename, err)
	}

	log.Infof("Pin IP successful: IP %s, state file %s", ipv4, filename)
	return nil
}

func serializeIPState(owner string, result *AllocationResult) ([]byte, error) {
	if result == nil {
		return []byte{}, fmt.Errorf("provided AllocationResult is nil")
	}

	s := ""

	// Owner
	s += "Owner: " + owner

	// IP net.IP
	s += "\n" + "IP: " + result.IP.String()

	// CIDRs []string
	s += "\n" + "CIDRs: " + strings.Join(result.CIDRs, " ")

	// Master string
	s += "\n" + "Master: " + result.Master

	// GatewayIP string
	s += "\n" + "GatewayIP: " + result.GatewayIP

	log.Infof("serialize IP state successful: %s", s)
	return []byte(s), nil
}

// unpinIP deletes the record in IPAM state cache which contains the given IP
func unpinIP(ip net.IP) error {
	if s := ip.To4(); s == nil {
		return fmt.Errorf("IPState currently only supports IPv4")
	}

	files, err := ioutil.ReadDir(IPAM_STATE_DIR)
	if err != nil {
		return fmt.Errorf("ReadDir failed: %s", err)
	}

	ipv4 := ip.String()
	fnames := []string{}
	for _, f := range files {
		name := f.Name()
		if len(name) < LEAST_STS_POD_LEN {
			log.Warnf("Get pinned IP: invalid state file detected: %s", name)
			continue
		}

		if strings.HasSuffix(name, ipv4) {
			fnames = append(fnames, name)
		}
	}

	n := len(fnames)
	switch n {
	case 0: // file not found, nothing to do
		log.Warnf("UnpinIP %s: state file not found", ipv4)
		return nil
	case 1: // exactly one file matched, this is what we expect
		fullPath := path.Join(IPAM_STATE_DIR, fnames[0])
		if err := os.Remove(fullPath); err != nil {
			return fmt.Errorf("remove %s failed: %s", fullPath, err)
		}

		log.Infof("UnpinIP successful: IP %s, removed file %s", ipv4, fullPath)
		return nil
	default: // more than one file matched, unexpected case
		return fmt.Errorf("%d files found with IP %s: %s, please check "+
			"and delete the conflicted files manually", n, ipv4, fnames)
	}
}

// getPodWithPinnedIP returns the pod name which currently pins to the given IP
func getPodWithPinnedIP(ip net.IP) (string, error) {
	files, err := ioutil.ReadDir(IPAM_STATE_DIR)
	if err != nil {
		return "", fmt.Errorf("ReadDir failed: %s", err)
	}

	ipv4 := ip.String()
	fnames := []string{}
	for _, f := range files {
		name := f.Name()
		if len(name) < LEAST_STS_POD_LEN {
			return "", fmt.Errorf("unknown ip info: %s", name)
		}

		if strings.HasSuffix(name, ipv4) {
			fnames = append(fnames, name)
		}
	}

	n := len(fnames)
	switch n {
	case 0: // file not found
		return "", fmt.Errorf("state file not found for IP %s", ipv4)
	case 1: // exactly one file matched, this is what we expect
		filename := fnames[0]
		items := strings.Split(filename, ":")
		if len(items) != 2 {
			return "", fmt.Errorf("filename pattern unexpected: %s", filename)
		}

		fullPath := path.Join(IPAM_STATE_DIR, filename)
		if content, err := ioutil.ReadFile(fullPath); err != nil {
			return "", fmt.Errorf("read file %s failed: %s", fullPath, err)
		} else {
			if owner, _, err := deserializeIPState(string(content)); err != nil {
				return "", err
			} else {
				return owner, nil
			}
		}
	default: // more than one file matched, unexpected case
		return "", fmt.Errorf("%d files found with IP %s: %s, please check "+
			"and delete the conflicted files manually", n, ipv4, fnames)
	}
}

// Should unpin IP if
// 1. Pod label k8s.FIXED_IP_LABEL value changed to non-true, or
// 2. The pod is scaled down
func shouldUnpinIPForPod(pod string) (bool, error) {
	return k8s.ShouldUnpinIP(pod)
}

// reservePinnedIPs reserves some IPs from IPAM CIDR to avoid being
// allocated out again, which will cause IP conflicts
func (ipam *IPAM) ReservePinnedIPs() {
	if !EnableFixedIP {
		log.Infof("EnabledFixedIP unset, skip reserving pinned IPs")
		return
	}

	log.Infof("Reserving pinned IPs: %s, %s", ipam.PinnedIPs, ipam.PinnedOwners)

	n1 := len(ipam.PinnedIPs)
	n2 := len(ipam.PinnedOwners)
	if n1 != n2 {
		log.Errorf("Reserve IPs failed: IP and owner list mismatch: %s, %s",
			ipam.PinnedIPs, ipam.PinnedOwners)
		return
	}

	for i := 0; i < n1; i++ {
		ip := ipam.PinnedIPs[i]
		owner := ipam.PinnedOwners[i]
		if err := ipam.AllocateIPString(ip, owner); err != nil {
			if strings.Contains(err.Error(), "already allocated") {
				log.Infof("Reserve IP %s for %s: already reserved", ip, owner)
			} else {
				log.Errorf("Reserve IP %s for %s failed: %s", ip, owner, err)
			}
			continue
		}

		log.Infof("Reserve IP %s for %s successful", ip, owner)
	}

	log.Infof("Reserve pinned IPs finish")
}
