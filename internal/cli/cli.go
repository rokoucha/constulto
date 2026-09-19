package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alecthomas/kong"
	"github.com/rokoucha/constulto/assets"
	"github.com/rokoucha/constulto/internal/config"
	"github.com/rokoucha/constulto/internal/gitstate"
	"github.com/rokoucha/constulto/internal/reviewer"
	"github.com/rokoucha/constulto/internal/runstore"
)

var (
	Version   = "dev"
	Commit    = "unknown"
	SourceURL = "https://github.com/rokoucha/constulto"
)

type App struct {
	Stdout, Stderr io.Writer
	Getwd          func() (string, error)
}

func New() App { return App{Stdout: os.Stdout, Stderr: os.Stderr, Getwd: os.Getwd} }

type commandLine struct {
	Doctor   doctorCommand   `cmd:"" help:"設定とエージェントCLIの対応状況を確認します。"`
	Review   reviewCommand   `cmd:"" help:"独立した構造化レビューを実行します。"`
	Show     showCommand     `cmd:"" help:"保存済みの実行結果を表示します。"`
	Followup followupCommand `cmd:"" help:"同じ対象へ新しい根拠を追加して再レビューします。"`
	Skill    skillCommand    `cmd:"" help:"同梱Agent Skillを表示またはインストールします。"`
	Version  struct{}        `cmd:"" help:"バージョンを表示します。"`
}

type doctorCommand struct {
	Config string   `name:"config" help:"ユーザー設定ファイル。" type:"path"`
	JSON   bool     `name:"json" help:"JSONで表示します。"`
	Agents []string `name:"agent" help:"確認するエージェント名。複数指定できます。省略時は全件です。"`
}

type reviewCommand struct {
	Kind     string   `name:"kind" required:"" enum:"design,implementation" help:"レビュー種別。"`
	Brief    string   `name:"brief" required:"" help:"レビュー依頼書。" type:"path"`
	Base     string   `name:"base" help:"実装レビューのGit基準ref。"`
	Config   string   `name:"config" help:"ユーザー設定ファイル。" type:"path"`
	DryRun   bool     `name:"dry-run" help:"エージェントを起動せず実行計画を表示します。"`
	JSON     bool     `name:"json" help:"JSONで表示します。"`
	Timeout  int      `name:"timeout" help:"タイムアウト秒数。"`
	Agents   []string `name:"agent" help:"エージェント名。複数指定で独立レビューを実行します。"`
	Includes []string `name:"include" help:"対象へ追加するリポジトリ相対パス。複数指定できます。"`
}

type showCommand struct {
	RunID string `arg:"" name:"run-id" help:"実行ID。"`
	JSON  bool   `name:"json" help:"JSONで表示します。"`
}

type followupCommand struct {
	RunID   string `arg:"" name:"run-id" help:"元の実行ID。"`
	Brief   string `name:"brief" required:"" help:"反証または追加資料。" type:"path"`
	Config  string `name:"config" help:"ユーザー設定ファイル。" type:"path"`
	Agent   string `name:"agent" help:"複数担当runから選ぶエージェント名。"`
	JSON    bool   `name:"json" help:"JSONで表示します。"`
	Timeout int    `name:"timeout" help:"タイムアウト秒数。"`
}

type skillCommand struct {
	Show    struct{}            `cmd:"" help:"同梱Skillを表示します。"`
	Install skillInstallCommand `cmd:"" help:"Codex用Skillをインストールします。"`
}

type skillInstallCommand struct {
	Agent  string `name:"agent" required:"" enum:"codex" help:"親エージェント。"`
	DryRun bool   `name:"dry-run" help:"書き込まず差分と配置先を表示します。"`
	Force  bool   `name:"force" help:"内容が異なる既存Skillを置換します。"`
}

func (a App) Run(ctx context.Context, args []string) int {
	noArgs := len(args) == 0
	if len(args) == 0 {
		args = []string{"--help"}
	}
	if len(args) > 0 && (args[0] == "review" || args[0] == "followup") && os.Getenv("CONSTULTO_DEPTH") != "" && os.Getenv("CONSTULTO_DEPTH") != "0" {
		fmt.Fprintln(a.Stderr, "recursive constulto review invocation refused")
		return 2
	}
	if args[0] == "help" {
		if len(args) == 2 {
			args = []string{args[1], "--help"}
		} else {
			args = []string{"--help"}
		}
	}
	if args[0] == "--version" {
		args = []string{"version"}
	}
	var cli commandLine
	exitCode := -1
	parser, err := kong.New(&cli,
		kong.Name("constulto"),
		kong.Description("独立したコーディングエージェントへ設計・実装レビューを依頼します。"),
		kong.Writers(a.Stdout, a.Stderr),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true}),
		kong.Exit(func(code int) { exitCode = code }),
	)
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 2
	}
	parsed, err := parser.Parse(args)
	if exitCode >= 0 {
		if noArgs {
			return 2
		}
		return exitCode
	}
	if err != nil {
		fmt.Fprintf(a.Stderr, "constulto: %v\n", err)
		return 2
	}
	switch parsed.Command() {
	case "version":
		fmt.Fprintf(a.Stdout, "%s (%s) %s\n", Version, Commit, SourceURL)
		return 0
	case "doctor":
		return a.doctor(ctx, cli.Doctor)
	case "review":
		return a.review(ctx, cli.Review)
	case "show <run-id>":
		return a.show(cli.Show)
	case "followup <run-id>":
		return a.followup(ctx, cli.Followup)
	case "skill show":
		return a.skillShow()
	case "skill install":
		return a.skillInstall(cli.Skill.Install)
	default:
		fmt.Fprintf(a.Stderr, "unknown command %q\n", parsed.Command())
		return 2
	}
}

func (a App) load(path string) (config.Loaded, string, error) {
	cwd, err := a.Getwd()
	if err != nil {
		return config.Loaded{}, "", err
	}
	root, err := gitstate.Root(cwd)
	if err != nil {
		root = cwd
	}
	cfg, err := config.Load(path, root)
	return cfg, root, err
}

func (a App) doctor(ctx context.Context, args doctorCommand) int {
	loaded, _, err := a.load(args.Config)
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 2
	}
	type check struct {
		Agent   string         `json:"agent"`
		Adapter string         `json:"adapter"`
		Command string         `json:"command"`
		Probe   reviewer.Probe `json:"probe"`
	}
	names := append([]string(nil), args.Agents...)
	if len(names) == 0 {
		for name := range loaded.Config.Agents {
			names = append(names, name)
		}
		sort.Strings(names)
	}
	checks := make([]check, 0, len(names))
	failed := false
	for _, name := range names {
		agent, ok := loaded.Config.Agents[name]
		if !ok {
			fmt.Fprintf(a.Stderr, "agent %q is not configured\n", name)
			return 2
		}
		probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		probe := reviewer.ProbeAgent(probeCtx, agent)
		cancel()
		checks = append(checks, check{Agent: name, Adapter: agent.Adapter, Command: agent.Command, Probe: probe})
		failed = failed || !probe.Supported
	}
	result := struct {
		Checks        []check    `json:"checks"`
		ConfigSources []string   `json:"configSources"`
		Skill         skillCheck `json:"skill"`
	}{checks, loaded.Sources, inspectSkill()}
	if args.JSON {
		b, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.Stdout, string(b))
	} else {
		fmt.Fprintf(a.Stdout, "configuration: valid (%s)\n", strings.Join(loaded.Sources, " -> "))
		for _, item := range result.Checks {
			fmt.Fprintf(a.Stdout, "agent: %s (%s)\ncommand: %s\nversion: %s\n", item.Agent, item.Adapter, item.Command, item.Probe.Version)
			if item.Probe.Supported {
				fmt.Fprintln(a.Stdout, "capabilities: supported")
			} else {
				fmt.Fprintf(a.Stdout, "capabilities: unsupported (%s)\n", item.Probe.Error)
			}
		}
		fmt.Fprintf(a.Stdout, "skill: %s", result.Skill.Status)
		if result.Skill.Path != "" {
			fmt.Fprintf(a.Stdout, " (%s)", result.Skill.Path)
		}
		fmt.Fprintln(a.Stdout)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return 130
	}
	if failed {
		return 1
	}
	return 0
}

func (a App) review(ctx context.Context, args reviewCommand) int {
	if args.Timeout < 0 || args.Timeout > 86400 {
		fmt.Fprintln(a.Stderr, "--timeout must be between 1 and 86400 seconds")
		return 2
	}
	brief, err := os.ReadFile(args.Brief)
	if err != nil {
		fmt.Fprintf(a.Stderr, "read brief: %v\n", err)
		return 2
	}
	loaded, root, err := a.load(args.Config)
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 2
	}
	if len(args.Agents) > 1 {
		return a.reviewMany(ctx, args.Kind, args.Base, brief, args.Timeout, args.JSON, args.Agents, args.Includes, loaded, root, args.DryRun)
	}
	name := loaded.Config.Defaults.Agent
	if len(args.Agents) == 1 {
		name = args.Agents[0]
	}
	agent, ok := loaded.Config.Agents[name]
	if !ok {
		fmt.Fprintf(a.Stderr, "agent %q is not configured\n", name)
		return 2
	}
	target, err := gitstate.Collect(root, args.Kind, args.Base, args.Includes, loaded.Exclude, brief)
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 2
	}
	if args.DryRun {
		a.printPlans([]reviewPlan{makePlan(name, agent, args.Kind, target, loaded.Sources)}, 1, args.JSON)
		return 0
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, 10*time.Second)
	probe := reviewer.ProbeAgent(probeCtx, agent)
	probeCancel()
	if !probe.Supported {
		if errors.Is(ctx.Err(), context.Canceled) {
			return 130
		}
		fmt.Fprintf(a.Stderr, "agent is not runnable: %s\n", probe.Error)
		return 2
	}
	prompt, err := buildPrompt(args.Kind, target, brief)
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 2
	}
	store, err := runstore.Default()
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 1
	}
	manifest := runstore.Manifest{Kind: args.Kind, Target: target, Agent: name, Adapter: agent.Adapter, ConfigSources: loaded.Sources}
	manifest, dir, err := store.New(manifest, brief, target.Diff, target.IncludedText)
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 1
	}
	fmt.Fprintf(a.Stderr, "run: %s\nrecords: %s\n", manifest.ID, dir)
	seconds := loaded.Config.Defaults.TimeoutSeconds
	if args.Timeout > 0 {
		seconds = args.Timeout
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	started := time.Now().UTC()
	review, metadata, raw, diagnostics, runErr := reviewer.Run(runCtx, agent, root, prompt)
	status := "completed"
	if runErr != nil {
		status = "failed"
		if errors.Is(runErr, context.Canceled) {
			status = "cancelled"
		}
	}
	current, staleErr := gitstate.Collect(root, args.Kind, args.Base, args.Includes, loaded.Exclude, brief)
	stale := staleErr != nil || current.Fingerprint != target.Fingerprint
	model := metadata.Model
	if model == "" {
		model = "unknown"
	}
	result := runstore.RunResult{SchemaVersion: 1, RunID: manifest.ID, Kind: args.Kind, TargetFingerprint: target.Fingerprint, ToolVersion: Version, ToolCommit: Commit, SourceURL: SourceURL, TemplateVersion: promptTemplateVersion(), Agent: name, Adapter: agent.Adapter, AdapterVersion: probe.Version, RequestedModel: agent.Model, Model: model, ObservedModels: metadata.ObservedModels, Restrictions: reviewer.Restrictions(agent.Adapter), ProviderCostUSD: metadata.ProviderCostUSD, Status: status, Stale: stale, StartedAt: started, FinishedAt: time.Now().UTC(), Review: review}
	if runErr != nil {
		result.ErrorType = reviewer.ErrorKind(runErr)
		result.Error = runErr.Error()
	}
	saveErr := store.Finish(&manifest, result, raw, diagnostics)
	if saveErr != nil {
		fmt.Fprintf(a.Stderr, "save result: %v\n", saveErr)
	}
	if args.JSON {
		b, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.Stdout, string(b))
	} else {
		render(a.Stdout, manifest, result, dir)
	}
	if saveErr != nil || runErr != nil {
		if errors.Is(runErr, context.Canceled) && !reviewer.IsTimeout(runErr) {
			return 130
		}
		return 1
	}
	return 0
}

func (a App) reviewMany(ctx context.Context, kind, base string, brief []byte, timeout int, jsonOut bool, names, includes []string, loaded config.Loaded, root string, dryRun bool) int {
	seen := map[string]bool{}
	profiles := make([]config.Agent, len(names))
	probes := make([]reviewer.Probe, len(names))
	for i, name := range names {
		if seen[name] {
			fmt.Fprintf(a.Stderr, "agent %q was specified more than once\n", name)
			return 2
		}
		seen[name] = true
		agent, ok := loaded.Config.Agents[name]
		if !ok {
			fmt.Fprintf(a.Stderr, "agent %q is not configured\n", name)
			return 2
		}
		profiles[i] = agent
	}
	target, err := gitstate.Collect(root, kind, base, includes, loaded.Exclude, brief)
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 2
	}
	if dryRun {
		plans := make([]reviewPlan, len(names))
		for i, name := range names {
			plans[i] = makePlan(name, profiles[i], kind, target, loaded.Sources)
		}
		a.printPlans(plans, loaded.Config.Defaults.MaxConcurrency, jsonOut)
		return 0
	}
	for i, agent := range profiles {
		probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		probes[i] = reviewer.ProbeAgent(probeCtx, agent)
		cancel()
		if !probes[i].Supported {
			if errors.Is(ctx.Err(), context.Canceled) {
				return 130
			}
			fmt.Fprintf(a.Stderr, "agent %s is not runnable: %s\n", names[i], probes[i].Error)
			return 2
		}
	}
	prompt, err := buildPrompt(kind, target, brief)
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 2
	}
	store, err := runstore.Default()
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 1
	}
	manifest := runstore.Manifest{Kind: kind, Target: target, Agent: "multiple", Adapter: "multiple", Agents: append([]string(nil), names...), ConfigSources: loaded.Sources}
	manifest, dir, err := store.New(manifest, brief, target.Diff, target.IncludedText)
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 1
	}
	fmt.Fprintf(a.Stderr, "run: %s\nrecords: %s\n", manifest.ID, dir)
	seconds := loaded.Config.Defaults.TimeoutSeconds
	if timeout > 0 {
		seconds = timeout
	}
	results := make([]runstore.RunResult, len(names))
	rawAnswers := make([][]byte, len(names))
	diagnosticsLogs := make([][]byte, len(names))
	saveErrors := make([]error, len(names))
	sem := make(chan struct{}, loaded.Config.Defaults.MaxConcurrency)
	var wg sync.WaitGroup
	for i := range names {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			started := time.Now().UTC()
			runCtx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
			defer cancel()
			review, metadata, raw, diagnostics, runErr := reviewer.Run(runCtx, profiles[i], root, prompt)
			status := "completed"
			if runErr != nil {
				status = "failed"
				if errors.Is(runErr, context.Canceled) {
					status = "cancelled"
				}
			}
			model := metadata.Model
			if model == "" {
				model = "unknown"
			}
			r := runstore.RunResult{SchemaVersion: 1, RunID: manifest.ID, Kind: kind, TargetFingerprint: target.Fingerprint, ToolVersion: Version, ToolCommit: Commit, SourceURL: SourceURL, TemplateVersion: promptTemplateVersion(), Agent: names[i], Adapter: profiles[i].Adapter, AdapterVersion: probes[i].Version, RequestedModel: profiles[i].Model, Model: model, ObservedModels: metadata.ObservedModels, Restrictions: reviewer.Restrictions(profiles[i].Adapter), ProviderCostUSD: metadata.ProviderCostUSD, Status: status, StartedAt: started, FinishedAt: time.Now().UTC(), Review: review}
			if runErr != nil {
				r.ErrorType = reviewer.ErrorKind(runErr)
				r.Error = runErr.Error()
			}
			results[i] = r
			rawAnswers[i] = raw
			diagnosticsLogs[i] = diagnostics
			saveErrors[i] = store.SaveAgent(manifest.ID, names[i], r, raw, diagnostics)
		}(i)
	}
	wg.Wait()
	current, staleErr := gitstate.Collect(root, kind, base, includes, loaded.Exclude, brief)
	stale := staleErr != nil || current.Fingerprint != target.Fingerprint
	completed := 0
	cancelled := false
	var cost float64
	hasCost := false
	for i := range results {
		results[i].Stale = stale
		if saveErrors[i] == nil {
			saveErrors[i] = store.SaveAgent(manifest.ID, names[i], results[i], rawAnswers[i], diagnosticsLogs[i])
		}
		if saveErr := saveErrors[i]; saveErr != nil && results[i].Error == "" {
			results[i].Status = "failed"
			results[i].ErrorType = "storage"
			results[i].Error = saveErr.Error()
		}
		if results[i].Status == "completed" {
			completed++
		}
		if results[i].Status == "cancelled" {
			cancelled = true
		}
		if results[i].ProviderCostUSD != nil {
			cost += *results[i].ProviderCostUSD
			hasCost = true
		}
	}
	status := "failed"
	if completed == len(results) {
		status = "completed"
	} else if completed > 0 {
		status = "partial"
	} else if cancelled {
		status = "cancelled"
	}
	started := manifest.StartedAt
	parent := runstore.RunResult{SchemaVersion: 1, RunID: manifest.ID, Kind: kind, TargetFingerprint: target.Fingerprint, ToolVersion: Version, ToolCommit: Commit, SourceURL: SourceURL, TemplateVersion: promptTemplateVersion(), Agent: "multiple", Adapter: "multiple", AdapterVersion: "multiple", Model: "multiple", Status: status, Stale: stale, StartedAt: started, FinishedAt: time.Now().UTC(), AgentResults: results}
	if hasCost {
		parent.ProviderCostUSD = &cost
	}
	finishErr := store.Finish(&manifest, parent, nil, nil)
	if finishErr != nil {
		fmt.Fprintf(a.Stderr, "save result: %v\n", finishErr)
	}
	if jsonOut {
		b, _ := json.MarshalIndent(parent, "", "  ")
		fmt.Fprintln(a.Stdout, string(b))
	} else {
		render(a.Stdout, manifest, parent, dir)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return 130
	}
	if status == "completed" && finishErr == nil {
		return 0
	}
	return 1
}

func buildPrompt(kind string, target gitstate.Target, brief []byte) ([]byte, error) {
	template, err := assets.FS.ReadFile("prompts/review.md")
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.Write(template)
	fmt.Fprintf(&b, "\n\nReview kind: %s\nRepository root: %s\nTarget fingerprint: %s\n", kind, target.Root, target.Fingerprint)
	b.WriteString("\n<brief>\n")
	b.Write(brief)
	b.WriteString("\n</brief>\n")
	if len(target.Diff) > 0 {
		b.WriteString("\n<patch>\n")
		b.Write(target.Diff)
		b.WriteString("\n</patch>\n")
	}
	if len(target.IncludedText) > 0 {
		b.WriteString("\n<included-files>\n")
		b.Write(target.IncludedText)
		b.WriteString("\n</included-files>\n")
	}
	if b.Len() > 32<<20 {
		return nil, fmt.Errorf("review input exceeds 32 MiB")
	}
	return b.Bytes(), nil
}

func promptTemplateVersion() string {
	template, err := assets.FS.ReadFile("prompts/review.md")
	if err != nil {
		return "unknown"
	}
	sum := sha256.Sum256(template)
	return fmt.Sprintf("sha256:%x", sum[:])
}

type reviewPlan struct {
	Kind            string   `json:"kind"`
	Agent           string   `json:"agent"`
	Adapter         string   `json:"adapter"`
	Command         string   `json:"command"`
	Root            string   `json:"root"`
	Target          string   `json:"target"`
	MergeBase       string   `json:"mergeBase,omitempty"`
	ReviewScope     string   `json:"reviewScope"`
	AccessibleScope string   `json:"accessibleScope"`
	Restrictions    []string `json:"restrictions"`
	ConfigSources   []string `json:"configSources"`
	Excluded        []string `json:"excluded,omitempty"`
	Untracked       []string `json:"untrackedNotSelected,omitempty"`
}

func makePlan(name string, agent config.Agent, kind string, t gitstate.Target, sources []string) reviewPlan {
	scope := "brief"
	if kind == "implementation" {
		scope = "tracked diff"
	}
	accessible := "repository read/search; ignored files may be readable if discovered; external-directory isolation depends on the adapter"
	if agent.Adapter == "opencode" {
		accessible = "repository read/search; .env files and external directories denied"
	}
	return reviewPlan{Kind: kind, Agent: name, Adapter: agent.Adapter, Command: agent.Command, Root: t.Root, Target: t.Fingerprint, MergeBase: t.MergeBase, ReviewScope: fmt.Sprintf("%s plus %d explicitly included file(s)", scope, len(t.Included)), AccessibleScope: accessible, Restrictions: reviewer.Restrictions(agent.Adapter), ConfigSources: append([]string(nil), sources...), Excluded: append([]string(nil), t.Excluded...), Untracked: append([]string(nil), t.Untracked...)}
}

func (a App) printPlans(plans []reviewPlan, maxConcurrency int, jsonOut bool) {
	if jsonOut {
		value := struct {
			Plans          []reviewPlan `json:"plans"`
			MaxConcurrency int          `json:"maxConcurrency"`
		}{plans, maxConcurrency}
		b, _ := json.MarshalIndent(value, "", "  ")
		fmt.Fprintln(a.Stdout, string(b))
		return
	}
	for _, plan := range plans {
		fmt.Fprintf(a.Stdout, "kind: %s\nagent: %s (%s)\ncommand: %s\nroot: %s\ntarget: %s\n", plan.Kind, plan.Agent, plan.Adapter, plan.Command, plan.Root, plan.Target)
		if plan.MergeBase != "" {
			fmt.Fprintf(a.Stdout, "merge-base: %s\n", plan.MergeBase)
		}
		fmt.Fprintf(a.Stdout, "review scope: %s\naccessible scope: %s\nrestrictions: %s\nconfig: %s\n", plan.ReviewScope, plan.AccessibleScope, strings.Join(plan.Restrictions, ", "), strings.Join(plan.ConfigSources, " -> "))
		if len(plan.Excluded) > 0 {
			fmt.Fprintf(a.Stdout, "excluded from tracked diff: %s\n", strings.Join(plan.Excluded, ", "))
		}
		if len(plan.Untracked) > 0 {
			fmt.Fprintf(a.Stdout, "untracked repository files not selected with --include (brief is supplied separately): %s\n", strings.Join(plan.Untracked, ", "))
		}
	}
	if len(plans) > 1 {
		fmt.Fprintf(a.Stdout, "max concurrency: %d\n", maxConcurrency)
	}
}

func (a App) show(args showCommand) int {
	store, err := runstore.Default()
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 1
	}
	m, r, dir, err := store.Load(args.RunID)
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 1
	}
	if args.JSON {
		value := struct {
			Manifest runstore.Manifest  `json:"manifest"`
			Result   runstore.RunResult `json:"result"`
		}{m, r}
		b, _ := json.MarshalIndent(value, "", "  ")
		fmt.Fprintln(a.Stdout, string(b))
	} else {
		render(a.Stdout, m, r, dir)
	}
	return 0
}

func (a App) followup(ctx context.Context, args followupCommand) int {
	if args.Timeout < 0 || args.Timeout > 86400 {
		fmt.Fprintln(a.Stderr, "--timeout must be between 1 and 86400 seconds")
		return 2
	}
	store, err := runstore.Default()
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 1
	}
	original, result, _, err := store.Load(args.RunID)
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 2
	}
	previousReview := result.Review
	if len(result.AgentResults) > 0 {
		if args.Agent == "" {
			fmt.Fprintln(a.Stderr, "--agent is required when following up a multi-agent run")
			return 2
		}
		previousReview = nil
		for i := range result.AgentResults {
			candidate := &result.AgentResults[i]
			if candidate.Agent == args.Agent {
				if candidate.Status == "completed" {
					previousReview = candidate.Review
				}
				break
			}
		}
	}
	if previousReview == nil {
		fmt.Fprintln(a.Stderr, "followup requires a completed structured review from the selected agent")
		return 2
	}
	rebuttal, err := os.ReadFile(args.Brief)
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 2
	}
	loaded, err := config.Load(args.Config, original.Target.Root)
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 2
	}
	name := original.Agent
	if args.Agent != "" {
		name = args.Agent
	}
	agent, ok := loaded.Config.Agents[name]
	if !ok {
		fmt.Fprintf(a.Stderr, "agent %q is not configured\n", name)
		return 2
	}
	rootRun := original
	cursor := original
	for cursor.ParentRunID != "" {
		parent, _, _, loadErr := store.Load(cursor.ParentRunID)
		if loadErr != nil {
			fmt.Fprintln(a.Stderr, loadErr)
			return 1
		}
		cursor, rootRun = parent, parent
	}
	followupCount, err := store.FollowupCount(rootRun.ID)
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 1
	}
	if followupCount >= loaded.Config.Defaults.MaxFollowups {
		fmt.Fprintf(a.Stderr, "maximum followups exceeded (%d)\n", loaded.Config.Defaults.MaxFollowups)
		return 2
	}
	originalBrief, err := os.ReadFile(filepath.Join(store.Root, rootRun.ID, "brief.md"))
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 1
	}
	target, err := gitstate.Collect(original.Target.Root, original.Kind, original.Target.Base, original.Target.Included, original.Target.Excluded, originalBrief)
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 2
	}
	if target.Fingerprint != original.Target.Fingerprint {
		fmt.Fprintln(a.Stderr, "review target changed; start a new review instead of followup")
		return 2
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, 10*time.Second)
	probe := reviewer.ProbeAgent(probeCtx, agent)
	probeCancel()
	if !probe.Supported {
		if errors.Is(ctx.Err(), context.Canceled) {
			return 130
		}
		fmt.Fprintf(a.Stderr, "agent is not runnable: %s\n", probe.Error)
		return 2
	}
	prompt, err := buildFollowupPrompt(original.Kind, target, originalBrief, previousReview, rebuttal)
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 2
	}
	manifest := runstore.Manifest{Kind: original.Kind, Target: target, Agent: name, Adapter: agent.Adapter, ParentRunID: original.ID, ConfigSources: loaded.Sources}
	manifest, dir, err := store.New(manifest, originalBrief, target.Diff, target.IncludedText)
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 1
	}
	if err := store.SaveFollowupEvidence(manifest.ID, rebuttal); err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 1
	}
	fmt.Fprintf(a.Stderr, "run: %s\nrecords: %s\n", manifest.ID, dir)
	seconds := loaded.Config.Defaults.TimeoutSeconds
	if args.Timeout > 0 {
		seconds = args.Timeout
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	started := time.Now().UTC()
	review, metadata, raw, diagnostics, runErr := reviewer.Run(runCtx, agent, original.Target.Root, prompt)
	status := "completed"
	if runErr != nil {
		status = "failed"
		if errors.Is(runErr, context.Canceled) {
			status = "cancelled"
		}
	}
	current, staleErr := gitstate.Collect(original.Target.Root, original.Kind, original.Target.Base, original.Target.Included, original.Target.Excluded, originalBrief)
	stale := staleErr != nil || current.Fingerprint != target.Fingerprint
	model := metadata.Model
	if model == "" {
		model = "unknown"
	}
	out := runstore.RunResult{SchemaVersion: 1, RunID: manifest.ID, Kind: original.Kind, TargetFingerprint: target.Fingerprint, ToolVersion: Version, ToolCommit: Commit, SourceURL: SourceURL, TemplateVersion: promptTemplateVersion(), Agent: name, Adapter: agent.Adapter, AdapterVersion: probe.Version, RequestedModel: agent.Model, Model: model, ObservedModels: metadata.ObservedModels, Restrictions: reviewer.Restrictions(agent.Adapter), ProviderCostUSD: metadata.ProviderCostUSD, Status: status, Stale: stale, StartedAt: started, FinishedAt: time.Now().UTC(), Review: review}
	if runErr != nil {
		out.ErrorType = reviewer.ErrorKind(runErr)
		out.Error = runErr.Error()
	}
	saveErr := store.Finish(&manifest, out, raw, diagnostics)
	if saveErr != nil {
		fmt.Fprintf(a.Stderr, "save result: %v\n", saveErr)
	}
	if args.JSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(a.Stdout, string(b))
	} else {
		render(a.Stdout, manifest, out, dir)
	}
	if saveErr != nil || runErr != nil {
		if errors.Is(runErr, context.Canceled) && !reviewer.IsTimeout(runErr) {
			return 130
		}
		return 1
	}
	return 0
}

func buildFollowupPrompt(kind string, target gitstate.Target, originalBrief []byte, previous *runstore.Review, rebuttal []byte) ([]byte, error) {
	base, err := buildPrompt(kind, target, originalBrief)
	if err != nil {
		return nil, err
	}
	previousJSON, err := json.Marshal(previous)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.Write(base)
	b.WriteString("\n<previous-review>\n")
	b.Write(previousJSON)
	b.WriteString("\n</previous-review>\n<new-evidence>\n")
	b.Write(rebuttal)
	b.WriteString("\n</new-evidence>\nRe-evaluate only with this new evidence. Preserve or revise findings explicitly and return a complete structured review.\n")
	if b.Len() > 32<<20 {
		return nil, fmt.Errorf("review input exceeds 32 MiB")
	}
	return b.Bytes(), nil
}

func (a App) skillShow() int {
	content, err := assets.FS.ReadFile("skill/constulto/SKILL.md")
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 1
	}
	_, _ = a.Stdout.Write(content)
	return 0
}

func (a App) skillInstall(args skillInstallCommand) int {
	content, err := assets.FS.ReadFile("skill/constulto/SKILL.md")
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 1
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 1
	}
	destination := filepath.Join(home, ".codex", "skills", "constulto", "SKILL.md")
	if existing, err := os.ReadFile(destination); err == nil {
		if bytes.Equal(existing, content) {
			fmt.Fprintf(a.Stdout, "already current: %s\n", destination)
			return 0
		}
		if args.DryRun {
			fmt.Fprintf(a.Stdout, "would update: %s\n%s", destination, lineDiff(existing, content))
			return 0
		}
		if args.Force {
			if err := writeSkill(destination, content, false); err != nil {
				fmt.Fprintln(a.Stderr, err)
				return 1
			}
			fmt.Fprintf(a.Stdout, "updated: %s\n", destination)
			return 0
		}
		fmt.Fprintf(a.Stderr, "existing Skill differs; refusing to overwrite: %s\n", destination)
		fmt.Fprintln(a.Stderr, "inspect with --dry-run and replace explicitly with --force")
		return 1
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(a.Stderr, err)
		return 1
	}
	if args.DryRun {
		fmt.Fprintf(a.Stdout, "would install: %s\n", destination)
		return 0
	}
	if err := writeSkill(destination, content, true); err != nil {
		fmt.Fprintln(a.Stderr, err)
		return 1
	}
	fmt.Fprintf(a.Stdout, "installed: %s\n", destination)
	return 0
}

type skillCheck struct {
	Status string `json:"status"`
	Path   string `json:"path,omitempty"`
}

func inspectSkill() skillCheck {
	home, err := os.UserHomeDir()
	if err != nil {
		return skillCheck{Status: "unavailable"}
	}
	path := filepath.Join(home, ".codex", "skills", "constulto", "SKILL.md")
	existing, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return skillCheck{Status: "not-installed", Path: path}
	}
	if err != nil {
		return skillCheck{Status: "unreadable", Path: path}
	}
	bundled, _ := assets.FS.ReadFile("skill/constulto/SKILL.md")
	status := "outdated-or-modified"
	if bytes.Equal(existing, bundled) {
		status = "current"
	}
	return skillCheck{Status: status, Path: path}
}

func writeSkill(path string, content []byte, exclusive bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if exclusive {
		flags |= os.O_EXCL
		flags &^= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0644)
	if err != nil {
		return err
	}
	if _, err = f.Write(content); err != nil {
		f.Close()
		if exclusive {
			_ = os.Remove(path)
		}
		return err
	}
	return f.Close()
}

func lineDiff(old, next []byte) string {
	var b strings.Builder
	b.WriteString("--- installed\n+++ bundled\n")
	for _, line := range strings.Split(strings.TrimSuffix(string(old), "\n"), "\n") {
		fmt.Fprintf(&b, "- %s\n", line)
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(next), "\n"), "\n") {
		fmt.Fprintf(&b, "+ %s\n", line)
	}
	return b.String()
}

func render(w io.Writer, m runstore.Manifest, r runstore.RunResult, dir string) {
	fmt.Fprintf(w, "status: %s", r.Status)
	if r.Stale {
		fmt.Fprint(w, " (stale: target changed)")
	}
	fmt.Fprintf(w, "\nrun: %s\ntarget: %s\n", m.ID, m.Target.Fingerprint)
	if r.Error != "" {
		fmt.Fprintf(w, "error: %s\n", r.Error)
	}
	if len(r.AgentResults) > 0 {
		for _, child := range r.AgentResults {
			fmt.Fprintf(w, "\n== %s (%s): %s ==\n", child.Agent, child.Adapter, child.Status)
			if child.Error != "" {
				fmt.Fprintf(w, "error: %s\n", child.Error)
			}
			renderReview(w, child.Review)
		}
	} else {
		renderReview(w, r.Review)
	}
	fmt.Fprintf(w, "\nrecords: %s\n", dir)
}

func renderReview(w io.Writer, review *runstore.Review) {
	if review == nil {
		return
	}
	fmt.Fprintf(w, "\n%s\n", review.Summary)
	if len(review.Decisions) > 0 {
		fmt.Fprintln(w, "\nDecisions:")
		for _, d := range review.Decisions {
			fmt.Fprintf(w, "- %s — %s\n", d.Question, d.Recommendation)
		}
	}
	if len(review.Findings) > 0 {
		fmt.Fprintln(w, "\nFindings:")
		for _, f := range review.Findings {
			fmt.Fprintf(w, "- [%s] %s\n  Impact: %s\n  Evidence: %s\n", f.ID, f.Claim, f.Impact, strings.Join(f.Evidence, "; "))
		}
	} else {
		fmt.Fprintln(w, "\nFindings: none reported (not proof of correctness)")
	}
	fmt.Fprintf(w, "\nNot reviewed: %s\nTests not run: %s\n", none(review.Scope.NotReviewed), none(review.Scope.TestsNotRun))
}
func none(v []string) string {
	if len(v) == 0 {
		return "none reported"
	}
	return strings.Join(v, "; ")
}
