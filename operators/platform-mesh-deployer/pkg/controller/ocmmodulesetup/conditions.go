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

package ocmmodulesetup

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types reported on a OCMModuleSetup.
const (
	ConditionWorkspacesProvisioned = "WorkspacesProvisioned"
	// ConditionReady is the handshake the module controller waits on.
	ConditionReady = "Ready"
)

func workspacesProvisioned(generation int64) metav1.Condition {
	return metav1.Condition{
		Type:               ConditionWorkspacesProvisioned,
		Status:             metav1.ConditionTrue,
		Reason:             "Provisioned",
		Message:            "workspaces and content applied",
		ObservedGeneration: generation,
	}
}

func workspacesPending(generation int64, reason, message string) metav1.Condition {
	return metav1.Condition{
		Type:               ConditionWorkspacesProvisioned,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	}
}

// readyCondition aggregates the pass into the one condition other controllers
// read. A failed step is reported even when it never reached the condition it
// would have written, so a stale True cannot survive a broken pass.
func readyCondition(generation int64, provisioned *metav1.Condition, err error) metav1.Condition {
	cond := metav1.Condition{
		Type:               ConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             "NotProvisioned",
		Message:            "workspaces are not provisioned yet",
		ObservedGeneration: generation,
	}
	switch {
	case err != nil:
		cond.Reason, cond.Message = "Error", err.Error()
	case provisioned == nil:
	case provisioned.Status != metav1.ConditionTrue:
		cond.Reason, cond.Message = provisioned.Reason, provisioned.Message
	default:
		cond.Status = metav1.ConditionTrue
		cond.Reason = "Provisioned"
		cond.Message = "the kcp side of the module is provisioned"
	}
	return cond
}
