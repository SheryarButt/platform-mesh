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

package ocmmodule

import (
	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types reported on a OCMModule.
const (
	ConditionSpecValid = "SpecValid"
	ConditionGated     = "DependenciesReady"
	ConditionResolved  = "Resolved"
	ConditionDeployed  = "Deployed"
	// ConditionReady is what the PlatformMesh's pre-topology gate and other
	// modules' dependency gates read.
	ConditionReady = "Ready"
)

// chainConditions are the step conditions Ready is derived from, in the order
// the steps run, so the first unmet one is the reason reported.
var chainConditions = []string{
	ConditionSpecValid,
	ConditionGated,
	ConditionResolved,
	ConditionDeployed,
}

func setCondition(mod *pmdeployv1alpha1.OCMModule, cond metav1.Condition) {
	meta.SetStatusCondition(&mod.Status.Conditions, cond)
}

func cond(condType string, generation int64, status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	}
}

func specValid(generation int64) metav1.Condition {
	return cond(ConditionSpecValid, generation, metav1.ConditionTrue, "Valid", "the spec is valid")
}

func specInvalid(generation int64, err error) metav1.Condition {
	return cond(ConditionSpecValid, generation, metav1.ConditionFalse, "Invalid", err.Error())
}

func dependenciesReady(generation int64) metav1.Condition {
	return cond(ConditionGated, generation, metav1.ConditionTrue, "Ready", "dependencies ready")
}

func gatedOn(generation int64, reason, message string) metav1.Condition {
	return cond(ConditionGated, generation, metav1.ConditionFalse, reason, message)
}

func componentResolved(generation int64) metav1.Condition {
	return cond(ConditionResolved, generation, metav1.ConditionTrue, "Resolved", "component version resolved")
}

func resolveFailed(generation int64, err error) metav1.Condition {
	return cond(ConditionResolved, generation, metav1.ConditionFalse, "ResolveFailed", err.Error())
}

func deployed(generation int64) metav1.Condition {
	return cond(ConditionDeployed, generation, metav1.ConditionTrue, "Deployed", "payloads applied")
}

func deployPending(generation int64, reason, message string) metav1.Condition {
	return cond(ConditionDeployed, generation, metav1.ConditionFalse, reason, message)
}

func deployFailed(generation int64, err error) metav1.Condition {
	return cond(ConditionDeployed, generation, metav1.ConditionFalse, "DeployFailed", err.Error())
}

// readyCondition aggregates the pass. A failed step is reported even when it
// never reached the condition it would have written, so a stale True cannot
// survive a broken pass.
func readyCondition(mod *pmdeployv1alpha1.OCMModule, err error) metav1.Condition {
	cond := metav1.Condition{
		Type:               ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Deployed",
		Message:            "the module is deployed",
		ObservedGeneration: mod.Generation,
	}
	if err != nil {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, "Error", err.Error()
		return cond
	}
	for _, name := range chainConditions {
		step := meta.FindStatusCondition(mod.Status.Conditions, name)
		if step == nil {
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
