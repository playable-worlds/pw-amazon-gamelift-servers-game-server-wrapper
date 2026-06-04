/*
 * Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
 * SPDX-License-Identifier: Apache-2.0
 */

package otelcol

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/sessiontemplate"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

const defaultProcessorName = "attributes/gamesession"

// Config represents the OpenTelemetry Collector configuration.
// Using map[string]interface{} to preserve all configuration fields.
type Config map[string]interface{}

// Service handles otelcol-contrib configuration updates.
type Service struct {
	configPath    string
	processorName string
	tagTemplates  map[string]string
	reloadCmd     []string
	testMode      bool
	logger        *slog.Logger
}

// New creates a new otelcol-contrib configuration service.
func New(configPath, processorName string, tagTemplates map[string]string, logger *slog.Logger) *Service {
	if processorName == "" {
		processorName = defaultProcessorName
	}
	return &Service{
		configPath:    configPath,
		processorName: processorName,
		tagTemplates:  tagTemplates,
		reloadCmd:     []string{"sudo", "systemctl", "restart", "otelcol-contrib"},
		testMode:      false,
		logger:        logger,
	}
}

// NewForTesting creates a new otelcol-contrib configuration service in test mode.
func NewForTesting(configPath, processorName string, tagTemplates map[string]string, logger *slog.Logger) *Service {
	if processorName == "" {
		processorName = defaultProcessorName
	}
	return &Service{
		configPath:    configPath,
		processorName: processorName,
		tagTemplates:  tagTemplates,
		reloadCmd:     []string{"sudo", "systemctl", "restart", "otelcol-contrib"},
		testMode:      true,
		logger:        logger,
	}
}

// UpdateTags processes tag templates and updates the otelcol-contrib configuration.
// This method replaces any existing templated resource attributes with new ones.
func (s *Service) UpdateTags(ctx context.Context, data interface{}) error {
	s.logger.DebugContext(ctx, "Updating otelcol-contrib configuration with templated tags", "data", data)

	if err := s.checkWritePermissions(); err != nil {
		return errors.Wrap(err, "insufficient permissions to modify otelcol-contrib configuration")
	}

	config, err := s.readConfig()
	if err != nil {
		return errors.Wrap(err, "failed to read otelcol-contrib configuration")
	}

	if config == nil {
		config = make(Config)
	}

	if err := s.updateProcessorActions(config, ctx, data); err != nil {
		return err
	}

	s.ensureProcessorInPipelines(config)

	if err := s.writeConfig(config); err != nil {
		return errors.Wrap(err, "failed to write otelcol-contrib configuration")
	}

	if err := s.reloadCollector(ctx); err != nil {
		return errors.Wrap(err, "failed to reload otelcol-contrib")
	}

	s.logger.InfoContext(ctx, "Successfully updated otelcol-contrib configuration with templated tags and restarted collector")
	return nil
}

func (s *Service) updateProcessorActions(config Config, ctx context.Context, data interface{}) error {
	processors := stringMap(config["processors"])
	if processors == nil {
		processors = make(map[string]interface{})
		config["processors"] = processors
	}

	processor := stringMap(processors[s.processorName])
	if processor == nil {
		processor = make(map[string]interface{})
		processors[s.processorName] = processor
	}

	actions := s.removeTemplatedActions(processor["actions"])
	for tagName, templateStr := range s.tagTemplates {
		renderedValue, err := s.renderTemplate(templateStr, data)
		if err != nil {
			s.logger.WarnContext(ctx, "Failed to render tag template", "tag", tagName, "template", templateStr, "error", err)
			continue
		}

		actions = append(actions, map[string]interface{}{
			"key":    tagName,
			"value":  renderedValue,
			"action": "upsert",
		})
		s.logger.InfoContext(ctx, "Added templated resource attribute to otelcol-contrib configuration",
			"processor", s.processorName, "key", tagName, "value", renderedValue)
	}

	processor["actions"] = actions
	processors[s.processorName] = processor
	config["processors"] = processors

	return nil
}

func (s *Service) ensureProcessorInPipelines(config Config) {
	serviceSection := stringMap(config["service"])
	if serviceSection == nil {
		return
	}

	pipelines := stringMap(serviceSection["pipelines"])
	if pipelines == nil {
		return
	}

	for pipelineName, pipeline := range pipelines {
		pipelineMap := stringMap(pipeline)
		if pipelineMap == nil {
			continue
		}

		processors, ok := pipelineMap["processors"].([]interface{})
		if !ok {
			continue
		}

		if processorListed(processors, s.processorName) {
			continue
		}

		pipelineMap["processors"] = append(processors, s.processorName)
		pipelines[pipelineName] = pipelineMap
	}

	serviceSection["pipelines"] = pipelines
	config["service"] = serviceSection
}

func processorListed(processors []interface{}, name string) bool {
	for _, processor := range processors {
		if processorStr, ok := processor.(string); ok && processorStr == name {
			return true
		}
	}
	return false
}

func (s *Service) readConfig() (Config, error) {
	if _, err := os.Stat(s.configPath); os.IsNotExist(err) {
		return Config{
			"processors": map[string]interface{}{
				s.processorName: map[string]interface{}{
					"actions": []interface{}{},
				},
			},
			"service": map[string]interface{}{
				"pipelines": map[string]interface{}{},
			},
		}, nil
	}

	data, err := os.ReadFile(s.configPath)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read otelcol-contrib config file")
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, errors.Wrap(err, "failed to parse otelcol-contrib config file")
	}

	return normalizeConfig(config), nil
}

func normalizeConfig(config Config) Config {
	if config == nil {
		return Config{}
	}

	normalized := make(Config, len(config))
	for key, value := range config {
		normalized[key] = normalizeValue(value)
	}

	return normalized
}

func stringMap(value interface{}) map[string]interface{} {
	normalized, ok := normalizeValue(value).(map[string]interface{})
	if !ok {
		return nil
	}
	return normalized
}

func normalizeValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case Config:
		normalized := make(map[string]interface{}, len(typed))
		for key, nested := range typed {
			normalized[key] = normalizeValue(nested)
		}
		return normalized
	case map[string]interface{}:
		normalized := make(map[string]interface{}, len(typed))
		for key, nested := range typed {
			normalized[key] = normalizeValue(nested)
		}
		return normalized
	case map[interface{}]interface{}:
		normalized := make(map[string]interface{}, len(typed))
		for key, nested := range typed {
			normalized[fmt.Sprint(key)] = normalizeValue(nested)
		}
		return normalized
	case []interface{}:
		normalized := make([]interface{}, len(typed))
		for i, nested := range typed {
			normalized[i] = normalizeValue(nested)
		}
		return normalized
	default:
		return value
	}
}

func (s *Service) writeConfig(config Config) error {
	dir := filepath.Dir(s.configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return errors.Wrap(err, "failed to create otelcol-contrib config directory")
	}

	data, err := yaml.Marshal(config)
	if err != nil {
		return errors.Wrap(err, "failed to marshal otelcol-contrib config")
	}

	if err := os.WriteFile(s.configPath, data, 0644); err != nil {
		return errors.Wrap(err, "failed to write otelcol-contrib config file")
	}

	return nil
}

func (s *Service) reloadCollector(ctx context.Context) error {
	if s.testMode {
		cmdStr := strings.Join(s.reloadCmd, " ")
		s.logger.InfoContext(ctx, "Test mode: Would execute reload command", "command", cmdStr)
		return nil
	}

	cmd := exec.CommandContext(ctx, s.reloadCmd[0], s.reloadCmd[1:]...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		s.logger.ErrorContext(ctx, "Failed to restart otelcol-contrib", "error", err, "output", string(output))
		return errors.Wrap(err, "otelcol-contrib restart failed")
	}

	s.logger.DebugContext(ctx, "otelcol-contrib restarted successfully", "output", string(output))
	return nil
}

func (s *Service) renderTemplate(templateStr string, data interface{}) (string, error) {
	return sessiontemplate.Execute("tag", templateStr, data)
}

func (s *Service) removeTemplatedActions(actionsInterface interface{}) []interface{} {
	actions, ok := actionsInterface.([]interface{})
	if !ok {
		return []interface{}{}
	}

	var result []interface{}
	for _, actionInterface := range actions {
		action := stringMap(actionInterface)
		if action == nil {
			result = append(result, actionInterface)
			continue
		}

		key, ok := action["key"].(string)
		if !ok {
			result = append(result, actionInterface)
			continue
		}

		if _, isTemplated := s.tagTemplates[key]; isTemplated {
			continue
		}

		result = append(result, actionInterface)
	}

	return result
}

func (s *Service) checkWritePermissions() error {
	if _, err := os.Stat(s.configPath); os.IsNotExist(err) {
		dir := filepath.Dir(s.configPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return errors.Wrap(err, "failed to create otelcol-contrib config directory")
		}
	}

	file, err := os.OpenFile(s.configPath, os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return errors.Wrap(err, "cannot open otelcol-contrib config file for writing")
	}
	defer file.Close()

	return nil
}
