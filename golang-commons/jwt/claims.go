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

package jwt

// ClaimSet contains the claims parsed from a JWT.
type ClaimSet struct {
	values map[string]any
}

// String returns the value of a string claim by its exact key.
// Claims with a different type and missing claims are reported as not found.
func (c ClaimSet) String(name string) (string, bool) {
	value, found := c.values[name]
	if !found {
		return "", false
	}

	claim, ok := value.(string)
	return claim, ok
}
