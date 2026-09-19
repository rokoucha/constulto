package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rokoucha/constulto/assets"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type Defaults struct {
	Agent          string `json:"agent,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
	MaxConcurrency int    `json:"maxConcurrency,omitempty"`
	MaxFollowups   int    `json:"maxFollowups,omitempty"`
}

type Agent struct {
	Adapter string `json:"adapter"`
	Command string `json:"command"`
	Model   string `json:"model,omitempty"`
}

type Config struct {
	Version  int              `json:"version"`
	Defaults Defaults         `json:"defaults"`
	Agents   map[string]Agent `json:"agents"`
}

type projectConfig struct {
	Version  int             `json:"version"`
	Defaults defaultsOverlay `json:"defaults"`
	Exclude  []string        `json:"exclude,omitempty"`
}

type defaultsOverlay struct {
	Agent          *string `json:"agent"`
	TimeoutSeconds *int    `json:"timeoutSeconds"`
	MaxConcurrency *int    `json:"maxConcurrency"`
	MaxFollowups   *int    `json:"maxFollowups"`
}

type Loaded struct {
	Config  Config
	Exclude []string
	Sources []string
}

func builtIn() Config {
	return Config{
		Version:  1,
		Defaults: Defaults{Agent: "claude", TimeoutSeconds: 600, MaxConcurrency: 2, MaxFollowups: 1},
		Agents:   map[string]Agent{"claude": {Adapter: "claude", Command: "claude"}},
	}
}

func Load(explicit, projectDir string) (Loaded, error) {
	result := Loaded{Config: builtIn(), Sources: []string{"built-in"}}
	userPath := explicit
	if userPath == "" {
		var err error
		userPath, err = defaultConfigPath()
		if err != nil {
			return Loaded{}, err
		}
	}
	if err := loadOptional(userPath, explicit != "", "schema/config.json", &result.Config); err != nil {
		return Loaded{}, fmt.Errorf("user config: %w", err)
	} else if fileExists(userPath) {
		result.Sources = append(result.Sources, userPath)
	}

	projectPath := filepath.Join(projectDir, "constulto.json")
	var project projectConfig
	if err := loadOptional(projectPath, false, "schema/project-config.json", &project); err != nil {
		return Loaded{}, fmt.Errorf("project config: %w", err)
	} else if fileExists(projectPath) {
		applyDefaults(&result.Config.Defaults, project.Defaults)
		result.Exclude = append([]string(nil), project.Exclude...)
		result.Sources = append(result.Sources, projectPath)
	}
	if _, ok := result.Config.Agents[result.Config.Defaults.Agent]; !ok {
		return Loaded{}, fmt.Errorf("default agent %q is not configured", result.Config.Defaults.Agent)
	}
	for name, agent := range result.Config.Agents {
		if agent.Adapter == "opencode" && agent.Model == "" {
			return Loaded{}, fmt.Errorf("agent %q: model is required for the opencode adapter", name)
		}
	}
	return result, nil
}

func defaultConfigPath() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "constulto", "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".config", "constulto", "config.json"), nil
}

func loadOptional(path string, required bool, schemaPath string, dst any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && !required {
		return nil
	}
	if err != nil {
		return err
	}
	if err := Validate(schemaPath, b); err != nil {
		return err
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	if c, ok := dst.(*Config); ok {
		base := builtIn()
		mergeDefaults(&base.Defaults, c.Defaults)
		var raw struct {
			Defaults map[string]json.RawMessage `json:"defaults"`
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			return err
		}
		if _, present := raw.Defaults["maxFollowups"]; present {
			base.Defaults.MaxFollowups = c.Defaults.MaxFollowups
		}
		for name, agent := range c.Agents {
			base.Agents[name] = agent
		}
		*c = base
	}
	return nil
}

func applyDefaults(dst *Defaults, src defaultsOverlay) {
	if src.Agent != nil {
		dst.Agent = *src.Agent
	}
	if src.TimeoutSeconds != nil {
		dst.TimeoutSeconds = *src.TimeoutSeconds
	}
	if src.MaxConcurrency != nil {
		dst.MaxConcurrency = *src.MaxConcurrency
	}
	if src.MaxFollowups != nil {
		dst.MaxFollowups = *src.MaxFollowups
	}
}

func Validate(schemaPath string, document []byte) error {
	compiler := jsonschema.NewCompiler()
	entries, err := assets.FS.ReadDir("schema")
	if err != nil {
		return err
	}
	selectedURL := ""
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		b, err := assets.FS.ReadFile("schema/" + entry.Name())
		if err != nil {
			return err
		}
		var doc map[string]any
		if err := json.Unmarshal(b, &doc); err != nil {
			return err
		}
		url, _ := doc["$id"].(string)
		if url == "" {
			url = "https://constulto.local/" + entry.Name()
		}
		if err := compiler.AddResource(url, doc); err != nil {
			return err
		}
		if entry.Name() == filepath.Base(schemaPath) {
			selectedURL = url
		}
	}
	if selectedURL == "" {
		return fmt.Errorf("schema not found: %s", schemaPath)
	}
	schema, err := compiler.Compile(selectedURL)
	if err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(document, &value); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if err := schema.Validate(value); err != nil {
		return fmt.Errorf("schema validation: %w", err)
	}
	return nil
}

func mergeDefaults(dst *Defaults, src Defaults) {
	if src.Agent != "" {
		dst.Agent = src.Agent
	}
	if src.TimeoutSeconds != 0 {
		dst.TimeoutSeconds = src.TimeoutSeconds
	}
	if src.MaxConcurrency != 0 {
		dst.MaxConcurrency = src.MaxConcurrency
	}
	if src.MaxFollowups != 0 {
		dst.MaxFollowups = src.MaxFollowups
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
