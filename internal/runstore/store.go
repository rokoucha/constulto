package runstore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/rokoucha/constulto/internal/config"
	"github.com/rokoucha/constulto/internal/gitstate"
)

type Finding struct {
	ID           string   `json:"id"`
	Claim        string   `json:"claim"`
	Impact       string   `json:"impact"`
	Evidence     []string `json:"evidence"`
	Reproduction string   `json:"reproduction"`
	Suggestion   string   `json:"suggestion"`
	Unverified   []string `json:"unverified"`
}
type Decision struct {
	Question         string   `json:"question"`
	Options          []string `json:"options"`
	Recommendation   string   `json:"recommendation"`
	ChangeConditions []string `json:"changeConditions"`
}
type Scope struct {
	Reviewed    []string `json:"reviewed"`
	NotReviewed []string `json:"notReviewed"`
	TestsNotRun []string `json:"testsNotRun"`
}
type Review struct {
	Summary   string     `json:"summary"`
	Findings  []Finding  `json:"findings"`
	Decisions []Decision `json:"decisions"`
	Scope     Scope      `json:"scope"`
}
type RunResult struct {
	SchemaVersion     int         `json:"schemaVersion"`
	RunID             string      `json:"runId"`
	Kind              string      `json:"kind"`
	TargetFingerprint string      `json:"targetFingerprint"`
	ToolVersion       string      `json:"toolVersion"`
	ToolCommit        string      `json:"toolCommit"`
	SourceURL         string      `json:"sourceUrl"`
	TemplateVersion   string      `json:"templateVersion"`
	Agent             string      `json:"agent"`
	Adapter           string      `json:"adapter"`
	AdapterVersion    string      `json:"adapterVersion"`
	RequestedModel    string      `json:"requestedModel,omitempty"`
	Model             string      `json:"model"`
	ObservedModels    []string    `json:"observedModels,omitempty"`
	Restrictions      []string    `json:"restrictions,omitempty"`
	ProviderCostUSD   *float64    `json:"providerReportedCostUsd,omitempty"`
	Status            string      `json:"status"`
	Stale             bool        `json:"stale"`
	StartedAt         time.Time   `json:"startedAt"`
	FinishedAt        time.Time   `json:"finishedAt"`
	ErrorType         string      `json:"errorType,omitempty"`
	Error             string      `json:"error,omitempty"`
	Review            *Review     `json:"review,omitempty"`
	AgentResults      []RunResult `json:"agentResults,omitempty"`
}
type Manifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	ID            string          `json:"id"`
	Kind          string          `json:"kind"`
	Status        string          `json:"status"`
	StartedAt     time.Time       `json:"startedAt"`
	FinishedAt    *time.Time      `json:"finishedAt,omitempty"`
	Target        gitstate.Target `json:"target"`
	Agent         string          `json:"agent"`
	Adapter       string          `json:"adapter"`
	Agents        []string        `json:"agents,omitempty"`
	ParentRunID   string          `json:"parentRunId,omitempty"`
	ConfigSources []string        `json:"configSources"`
}
type Store struct{ Root string }

var runIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{16}$`)

func Default() (Store, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Store{}, err
		}
		root = filepath.Join(home, ".local", "state")
	}
	return Store{Root: filepath.Join(root, "constulto", "runs")}, nil
}
func (s Store) New(m Manifest, brief, diff, included []byte) (Manifest, string, error) {
	id, err := newID()
	if err != nil {
		return Manifest{}, "", err
	}
	m.ID, m.SchemaVersion, m.Status, m.StartedAt = id, 1, "running", time.Now().UTC()
	if m.ConfigSources == nil {
		m.ConfigSources = []string{}
	}
	dir := filepath.Join(s.Root, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Manifest{}, "", err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return Manifest{}, "", err
	}
	if err := writeValidatedJSON(filepath.Join(dir, "manifest.json"), m, "schema/manifest.json"); err != nil {
		return Manifest{}, "", err
	}
	if err := atomicWrite(filepath.Join(dir, "brief.md"), brief); err != nil {
		return Manifest{}, "", err
	}
	assessment := []byte("# 親エージェントの採否記録\n\n各指摘を `accepted`（採用）、`rejected`（却下）、`needs-decision`（人間の判断が必要）、`unverified`（未検証）のいずれかに分類し、検証根拠と理由を記録する。レビュアーの原文は上書きしない。\n")
	if err := atomicWrite(filepath.Join(dir, "assessment.md"), assessment); err != nil {
		return Manifest{}, "", err
	}
	if len(diff) > 0 {
		if err := atomicWrite(filepath.Join(dir, "diff.patch"), diff); err != nil {
			return Manifest{}, "", err
		}
	}
	if len(included) > 0 {
		if err := atomicWrite(filepath.Join(dir, "included-files.txt"), included); err != nil {
			return Manifest{}, "", err
		}
	}
	return m, dir, nil
}
func (s Store) Finish(m *Manifest, result RunResult, answer, diagnostics []byte) error {
	dir := filepath.Join(s.Root, m.ID)
	now := time.Now().UTC()
	m.FinishedAt = &now
	m.Status = result.Status
	if len(answer) > 0 {
		if err := atomicWrite(filepath.Join(dir, "answer.txt"), answer); err != nil {
			return err
		}
	}
	if len(diagnostics) > 0 {
		if err := atomicWrite(filepath.Join(dir, "diagnostics.log"), diagnostics); err != nil {
			return err
		}
	}
	if err := writeValidatedJSON(filepath.Join(dir, "result.json"), result, "schema/run-result.json"); err != nil {
		return err
	}
	return writeValidatedJSON(filepath.Join(dir, "manifest.json"), m, "schema/manifest.json")
}

func (s Store) SaveFollowupEvidence(runID string, evidence []byte) error {
	if !runIDPattern.MatchString(runID) {
		return fmt.Errorf("invalid run ID")
	}
	return atomicWrite(filepath.Join(s.Root, runID, "followup.md"), evidence)
}

func (s Store) FollowupCount(rootID string) (int, error) {
	if !runIDPattern.MatchString(rootID) {
		return 0, fmt.Errorf("invalid run ID")
	}
	entries, err := os.ReadDir(s.Root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	parents := make(map[string]string)
	for _, entry := range entries {
		if !entry.IsDir() || !runIDPattern.MatchString(entry.Name()) {
			continue
		}
		var manifest Manifest
		if err := readJSON(filepath.Join(s.Root, entry.Name(), "manifest.json"), &manifest); err != nil {
			return 0, fmt.Errorf("read run %s while counting followups: %w", entry.Name(), err)
		}
		parents[manifest.ID] = manifest.ParentRunID
	}
	count := 0
	for id := range parents {
		if id == rootID {
			continue
		}
		seen := map[string]bool{id: true}
		for cursor := parents[id]; cursor != "" && !seen[cursor]; cursor = parents[cursor] {
			if cursor == rootID {
				count++
				break
			}
			seen[cursor] = true
		}
	}
	return count, nil
}
func (s Store) SaveAgent(runID, name string, result RunResult, answer, diagnostics []byte) error {
	sum := sha256.Sum256([]byte(name))
	dir := filepath.Join(s.Root, runID, "agents", hex.EncodeToString(sum[:8]))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	if len(answer) > 0 {
		if err := atomicWrite(filepath.Join(dir, "answer.txt"), answer); err != nil {
			return err
		}
	}
	if len(diagnostics) > 0 {
		if err := atomicWrite(filepath.Join(dir, "diagnostics.log"), diagnostics); err != nil {
			return err
		}
	}
	return writeValidatedJSON(filepath.Join(dir, "result.json"), result, "schema/run-result.json")
}
func (s Store) Load(id string) (Manifest, RunResult, string, error) {
	if !runIDPattern.MatchString(id) || strings.ContainsAny(id, `/\\`) {
		return Manifest{}, RunResult{}, "", fmt.Errorf("invalid run ID")
	}
	dir := filepath.Join(s.Root, id)
	var m Manifest
	if err := readJSON(filepath.Join(dir, "manifest.json"), &m); err != nil {
		return m, RunResult{}, dir, err
	}
	var r RunResult
	err := readJSON(filepath.Join(dir, "result.json"), &r)
	if errors.Is(err, os.ErrNotExist) {
		r = RunResult{SchemaVersion: 1, Agent: m.Agent, Adapter: m.Adapter, Status: m.Status}
	} else if err != nil {
		return m, r, dir, err
	}
	return m, r, dir, nil
}
func writeJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(b, '\n'))
}
func writeValidatedJSON(path string, value any, schema string) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := config.Validate(schema, b); err != nil {
		return fmt.Errorf("validate %s: %w", filepath.Base(path), err)
	}
	return atomicWrite(path, b)
}
func readJSON(path string, dst any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}
func atomicWrite(path string, b []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102T150405Z-") + hex.EncodeToString(b), nil
}
