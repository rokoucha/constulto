# constulto

コーディングエージェントがコーディングエージェントに相談するためのツール

## How to use

```sh
# 推論を実行せず、設定・CLI版・必要な制御機能を確認
constulto doctor

# 対象と起動予定を確認（モデルは呼ばない）
constulto review --kind design --brief brief.md --dry-run

# 設計レビュー
constulto review --kind design --brief brief.md

# 同じ対象を独立した2担当でレビュー（設定のmaxConcurrencyまで並列実行）
constulto review --kind design --brief brief.md --agent claude --agent muse

# merge-baseからtracked working treeまでの実装レビュー
constulto review --kind implementation --base main --brief brief.md

# 未追跡ファイルを明示的に対象へ加える
constulto review --kind implementation --base main --brief brief.md --include path/to/new.go

# 保存済み結果を表示
constulto show <run-id>
constulto show <run-id> --json

# 同じ対象へ反証・追加資料を渡す（既定1回まで）
constulto followup <run-id> --brief rebuttal.md

# 複数担当の結果から1担当を選んで再照会
constulto followup <run-id> --agent muse --brief rebuttal.md

# 親エージェント用Skillを確認・明示的にインストール
constulto skill show
constulto skill install --agent codex --dry-run
constulto skill install --agent codex

# Claude Code用（~/.claude/skills/constulto/SKILL.md）
constulto skill install --agent claude --dry-run
constulto skill install --agent claude

# 既存Skillとの差分確認後、明示的に更新
constulto skill install --agent codex --force
```

- `review` は開始時にrun IDと保存先をstderrへ、最終結果をstdoutへ出します
  - 既定の記録先は `${XDG_STATE_HOME:-~/.local/state}/constulto/runs/` です
  - 実行中に対象が変化した結果には `stale` が付きます
- `--include` のパスはGitリポジトリのルート基準です
- `--json` はdry-runを含めてstdoutへ単一のJSON文書を出します
- `maxFollowups` は元のrunから派生する追加照会の合計上限です
  - 各followupは元の依頼書を維持し、新しい証拠を別ファイルへ保存します。

同梱Skillでは、「Claudeにこの変更をレビューして」のような送信先を指定した依頼を、レビューに必要な差分・関連ソース・README・依存定義・テストなどの送信許可として扱います。同じタスク・送信先で資料を追加するたびに、ファイル一覧やペイロードへの再承認は求めません。資格情報や無関係な機微情報などは対象外です。Codexなどの実行環境が行う承認審査を無効にするものではありません。既存のSkillには `constulto skill install --agent codex --force` で反映してください。

## 設定

- ユーザー設定: `${XDG_CONFIG_HOME:-~/.config}/constulto/config.json`
- プロジェクト設定: リポジトリ直下の `constulto.json`
  - 実行ファイルとモデルはユーザー設定だけで指定でき、プロジェクト設定からは上書きできません

```json
{
  "version": 1,
  "defaults": {
    "agent": "claude",
    "timeoutSeconds": 600,
    "maxConcurrency": 2,
    "maxFollowups": 1
  },
  "agents": {
    "claude": {
      "adapter": "claude",
      "command": "claude"
    },
    "muse": {
      "adapter": "muse",
      "command": "muse"
    },
    "codex": {
      "adapter": "codex",
      "command": "codex"
    },
    "antigravity": {
      "adapter": "antigravity",
      "command": "agy"
    },
    "cursor": {
      "adapter": "cursor",
      "command": "cursor-agent"
    },
    "open-model": {
      "adapter": "opencode",
      "command": "opencode",
      "model": "provider/model"
    }
  }
}
```

## Claude CodeからCodexへレビューを依頼する

1. `constulto skill install --agent claude` でスキルをインストールします。既存版の更新は `--force` を指定します。
2. 上記のユーザー設定に `codex` プロファイルを追加します。主にCodexへ依頼する場合は `defaults.agent` も `codex` にします。
3. `constulto doctor --agent codex` でCLIの対応状況を確認します。
4. Claude Codeで「constultoでCodexにこの変更をレビューして」と依頼します。CLIからは `constulto review --agent codex --kind implementation --base main --brief brief.md` でも実行できます。

Codexは通常のユーザー設定（`$CODEX_HOME/config.toml`、既定は `~/.codex/config.toml`）を読み込みます。LiteLLM Proxyのprovider・接続先・認証はCodex側で設定し、通常のCodex CLIで接続できる状態にしてください。環境変数も子プロセスへ引き継ぎます。constultoの `model` を指定するとCodex側のモデルを上書きするので、Proxyのモデル名と一致させるか省略してください。

読み取り専用sandbox、承認 `never`、ephemeral、rules無視、daemon無効は起動時に指定します。ユーザー設定のMCPやhooksなども読み込まれるため、レビューに適したCodex設定を使用してください。doctorは推論やProxyへの接続試験を行いません。

## doctor

設定した全プロファイル（`--agent`指定時は選択したもの）のCLI版と必要な制御フラグ、およびCodex・Claude Codeのインストール済みSkillが同梱版と一致するかを確認します。
実行結果にはツール・テンプレート・アダプターの版、実際に観測できたモデル、適用した制限、provider報告コストを保存します。
provider報告コストは実際の請求額を保証しません。

Cursor CLI は `--mode ask` と `--sandbox enabled` で実行し、一時設定で Shell・Write・WebFetch・MCP とサブエージェント起動を拒否します。読み取れる範囲は Cursor の実行環境に依存します。

## License

Copyright (c) 2026 Rokoucha

Released under the MIT license, see LICENSE.
