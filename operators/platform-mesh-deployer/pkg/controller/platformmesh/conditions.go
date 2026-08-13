/*
Copyright The Platform Mesh Authors.

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

package platformmesh

import (
	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types reported on a PlatformMesh.
const (
	ConditionPreTopologyModulesReady  = "PreTopologyModulesReady"
	ConditionTopologyReady            = "TopologyReady"
	ConditionExposureReady            = "ExposureReady"
	ConditionRootStructureProvisioned = "RootStructureProvisioned"
	// ConditionReady aggregates the chain. The module controller gates its
	// post-topology modules on it.
	ConditionReady = "Ready"
)

// chainConditions are the step conditions Ready is derived from, in the order
// the steps run, so the first unmet one is the reason reported.
var chainConditions = []string{
	ConditionPreTopologyModulesReady,
	ConditionTopologyReady,
	ConditionExposureReady,
	ConditionRootStructureProvisioned,
}

func preTopologyReady(generation int64) metav1.Condition {
	return metav1.Condition{
		Type:               ConditionPreTopologyModulesReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Ready",
		Message:            "pre-topology modules are ready",
		ObservedGeneration: generation,
	}
}

func preTopologyPending(generation int64, message string) metav1.Condition {
	return metav1.Condition{
		Type:               ConditionPreTopologyModulesReady,
		Status:             metav1.ConditionFalse,
		Reason:             "WaitingForModules",
		Message:            message,
		ObservedGeneration: generation,
	}
}

func topologyReady(generation int64) metav1.Condition {
	return metav1.Condition{
		Type:               ConditionTopologyReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Rendered",
		Message:            "the kcp topology is rendered",
		ObservedGeneration: generation,
	}
}

func topologyFailed(generation int64, err error) metav1.Condition {
	return metav1.Condition{
		Type:               ConditionTopologyReady,
		Status:             metav1.ConditionFalse,
		Reason:             "RenderFailed",
		Message:            err.Error(),
		ObservedGeneration: generation,
	}
}

func exposureReady(generation int64, message string) metav1.Condition {
	return metav1.Condition{
		Type:               ConditionExposureReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Exposed",
		Message:            message,
		ObservedGeneration: generation,
	}
}

func exposureFailed(generation int64, err error) metav1.Condition {
	return metav1.Condition{
		Type:               ConditionExposureReady,
		Status:             metav1.ConditionFalse,
		Reason:             "ExposeFailed",
		Message:            err.Error(),
		ObservedGeneration: generation,
	}
}

func rootStructureProvisioned(generation int64) metav1.Condition {
	return metav1.Condition{
		Type:               ConditionRootStructureProvisioned,
		Status:             metav1.ConditionTrue,
		Reason:             "Provisioned",
		Message:            "kcp root structure exists",
		ObservedGeneration: generation,
	}
}

func rootStructurePending(generation int64, reason, message string) metav1.Condition {
	return metav1.Condition{
		Type:               ConditionRootStructureProvisioned,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	}
}

// readyCondition aggregates the pass. A failed step is reported even when it
// never reached the condition it would have written, so a stale True cannot
// survive a broken pass.
func readyCondition(pm *pmdeployv1alpha1.PlatformMesh, err error) metav1.Condition {
	cond := metav1.Condition{
		Type:               ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Ready",
		Message:            "the installation is up",
		ObservedGeneration: pm.Generation,
	}
	if err != nil {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, "Error", err.Error()
		return cond
	}
	for _, name := range chainConditions {
		step := meta.FindStatusCondition(pm.Status.Conditions, name)
		// A step that never ran leaves no condition, which is not the same
		// as one that ran and failed; only the root structure is optional.
		if step == nil {
			if name == ConditionRootStructureProvisioned {
				continue
			}
			cond.Status, cond.Reason = metav1.ConditionFalse, "NotReconciled"
			cond.Message = "waiting for " + name
			return cond
		}
		if step.Status != metav1.ConditionTrue {
			cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, step.Reason, step.Message
			return cond
		}
	}
	return cond
}
