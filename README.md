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
    "open-model": {
      "adapter": "opencode",
      "command": "opencode",
      "model": "provider/model"
    }
  }
}
```

## doctor

設定した全プロファイル（`--agent`指定時は選択したもの）のCLI版と必要な制御フラグ、およびインストール済みSkillが同梱版と一致するかを確認します。
実行結果にはツール・テンプレート・アダプターの版、実際に観測できたモデル、適用した制限、provider報告コストを保存します。
provider報告コストは実際の請求額を保証しません。

## License

Copyright (c) 2026 Rokoucha

Released under the MIT license, see LICENSE.
