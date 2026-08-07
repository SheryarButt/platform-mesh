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

package merge

import (
	"github.com/mitchellh/copystructure"

	"go.platform-mesh.io/golang-commons/errors"
	"go.platform-mesh.io/golang-commons/logger"
)

func MergeMaps(base, overwriteMap map[string]any, log *logger.Logger) (map[string]any, error) {
	if overwriteMap == nil {
		return base, nil
	}
	overwriteCopy, err := copystructure.Copy(overwriteMap)
	if err != nil {
		return nil, err
	}
	result, ok := overwriteCopy.(map[string]any)
	if !ok {
		return nil, errors.New("failed to merge maps")
	}

	for key, val := range base {
		if value, ok := result[key]; ok {
			if dest, ok := value.(map[string]any); ok {
				// if result[key] is an object, merge overwriteMaps's val object into result[key].
				src, ok := val.(map[string]any)
				if !ok {
					// If the original value is nil, there is nothing to merge, so we don't print the warning
					if val != nil {
						log.Warn().Msgf("warning: skipped value for %s: Not a object.", key)
					}
				} else {
					mergeObject(dest, src, log)
				}
			}
		} else {
			// If the key is not in overwriteMap, copy it from base.
			result[key] = val
		}
	}
	return result, nil
}

func mergeObject(dst, src map[string]any, log *logger.Logger) map[string]any {
	if src == nil {
		return dst
	}
	if dst == nil {
		return src
	}
	// Because dst has higher precedence than src, dst values override src values.
	for key, val := range src {
		if dv, ok := dst[key]; !ok {
			// Key doesn't exist in dst, add it from src
			dst[key] = val
		} else if isObject(val) {
			if isObject(dv) {
				// Both are objects, recursively merge (dst has higher precedence)
				mergeObject(dv.(map[string]any), val.(map[string]any), log)
			} else {
				// src is object but dst is not, keep dst (dst has higher precedence)
				if val != nil {
					log.Debug().Msgf("keeping non-object value for %s from destination (destination has higher precedence)", key)
				}
			}
		} else if isObject(dv) {
			// dst is object but src is not, keep dst (dst has higher precedence)
			if val != nil {
				log.Debug().Msgf("keeping object value for %s from destination (destination has higher precedence)", key)
			}
		}
	}
	return dst
}

func isObject(v any) bool {
	_, ok := v.(map[string]any)
	return ok
}
