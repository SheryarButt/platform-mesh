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

// Package transfer copies objects between clusters.
//
// Every function reads from one [Ref] and writes to another, so a controller
// mirroring an object downwards and its status back upwards spells both
// directions out at the call site:
//
//	transfer.Spec(ctx, gvk, consumer, provider)   // spec travels downwards
//	transfer.Status(ctx, gvk, provider, consumer) // status travels back up
//
// [Resource] combines the two.
package transfer
