/*
 * Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
 * SPDX-License-Identifier: Apache-2.0
 */

package sessiontemplate

import (
	"bytes"
	"text/template"

	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/game"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/types/events"
	"github.com/pkg/errors"
)

// Execute renders a template string using session data.
func Execute(name, templateStr string, data any) (string, error) {
	if err := ensureGamePropertiesMap(data); err != nil {
		return "", errors.Wrap(err, "failed to parse game properties")
	}

	t, err := template.New(name).Parse(templateStr)
	if err != nil {
		return "", errors.Wrapf(err, "failed to parse template for %s", name)
	}

	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", errors.Wrapf(err, "failed to execute template for %s", name)
	}

	return b.String(), nil
}

func ensureGamePropertiesMap(data any) error {
	switch v := data.(type) {
	case *events.HostingStart:
		return v.EnsureGamePropertiesMap()
	case *game.StartArgs:
		if v.HostingStart != nil {
			return v.HostingStart.EnsureGamePropertiesMap()
		}
	default:
		if preparer, ok := data.(interface{ PrepareSessionTemplateData() error }); ok {
			return preparer.PrepareSessionTemplateData()
		}
	}

	return nil
}
