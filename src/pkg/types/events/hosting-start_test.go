/*
 * Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
 * SPDX-License-Identifier: Apache-2.0
 */

package events

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostingStart_EnsureGamePropertiesMap(t *testing.T) {
	t.Run("parses json properties", func(t *testing.T) {
		h := &HostingStart{
			GameProperties: `{"mapName":"blood_gulch","mode":"ffa"}`,
		}

		err := h.EnsureGamePropertiesMap()
		require.NoError(t, err)
		assert.Equal(t, "blood_gulch", h.GamePropertiesMap["mapName"])
		assert.Equal(t, "ffa", h.GamePropertiesMap["mode"])
	})

	t.Run("returns error for invalid json", func(t *testing.T) {
		h := &HostingStart{
			GameProperties: `{invalid`,
		}

		err := h.EnsureGamePropertiesMap()
		assert.Error(t, err)
	})

	t.Run("uses existing map", func(t *testing.T) {
		h := &HostingStart{
			GameProperties:    `{"ignored":"value"}`,
			GamePropertiesMap: map[string]string{"mapName": "existing"},
		}

		err := h.EnsureGamePropertiesMap()
		require.NoError(t, err)
		assert.Equal(t, "existing", h.GamePropertiesMap["mapName"])
	})
}

func TestHostingStart_GameProperty(t *testing.T) {
	h := &HostingStart{
		GameProperties: `{"mapName":"blood_gulch"}`,
	}

	assert.Equal(t, "blood_gulch", h.GameProperty("mapName"))
	assert.Equal(t, "", h.GameProperty("missing"))
}
