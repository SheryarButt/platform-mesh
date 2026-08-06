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
	"testing"

	"github.com/stretchr/testify/assert"
	"go.platform-mesh.io/golang-commons/logger"
)

func TestObjectMerge(t *testing.T) {
	// Given
	original := map[string]interface{}{
		"kcp": map[string]interface{}{
			"enabled": true,
			"url":     "https://kcp.example.com",
			"domains": []string{"example.com", "example.org"},
		},
		"logLevel": "info",
	}

	overwrite := map[string]interface{}{
		"kcp": map[string]interface{}{
			"enabled": false,
			"domains": []string{"example.com", "example2.org"},
		},
	}
	log, _ := logger.New(logger.DefaultConfig())
	res, err := MergeMaps(original, overwrite, log)
	assert.NoError(t, err)
	assert.False(t, res["kcp"].(map[string]interface{})["enabled"].(bool))
	assert.NotNil(t, res["kcp"].(map[string]interface{})["url"])
	assert.Equal(t, "https://kcp.example.com", res["kcp"].(map[string]interface{})["url"].(string))
	assert.Len(t, res["kcp"].(map[string]interface{})["domains"].([]string), 2)
	assert.Equal(t, "example.com", res["kcp"].(map[string]interface{})["domains"].([]string)[0])
	assert.Equal(t, "example2.org", res["kcp"].(map[string]interface{})["domains"].([]string)[1])
}
