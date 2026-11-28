# **02\_data\_modeling — 霊符（reifu）**

# **1\. データモデル全体像**

霊符（reifu）が扱うデータはすべてプロジェクトルート配下のローカルファイルとして永続化される。データベースや外部サービスは使用しない。

* **`.reifu.jsonc`（プロジェクト設定マスター / SSOT）**: 人間とAIが編集可能なコメント付きJSON形式のマスター設定。
* **`.reifu.state.json`（同期状態スナップショット）**: 前回 `init` または `sync` 実行時の各設定ファイルのSHA-256ハッシュと管理対象ルールを記録するロックファイル（`.gitignore` 対象）。
* **エージェント固有設定ファイル群（コンパイル出力先）**:
  * `.claude/settings.json`（JSON形式）
  * `.codex/config.toml`（TOML形式）
  * `.gemini/settings.json`（JSON形式）
* **行動規範ファイル群**:
  * `AGENTS.md`（正本）
  * `CLAUDE.md`、`GEMINI.md`（`AGENTS.md` へのハードリンク／シンボリックリンク／コピー）

`.reifu.state.json` の任意フィールド `managed_skills` は、同期先ディレクトリをキーに管理済みスキル名の配列を保持する。削除済みの正本に対応するリンクのクリーンアップに使用する。検査対象がある場合に限り、`status --json` は `skills` にリンク状態を出力する。親エージェントディレクトリ（例: `.claude/`）がなく `agents.create_missing: false` の宛先は、`sync` がスキップするため `status` でも検査対象外とする（`link --check` は `link` が作成するので検査する）。

# **2\. `.reifu.jsonc` スキーマ定義**

## **2.1 ファイルフォーマット仕様**

* 文字コード: UTF-8（BOMなし）、改行コード: LF（`\n`）
* 構文: 標準JSONに加え、行コメント（`// ...`）、ブロックコメント（`/* ... */`）、オブジェクト・配列末尾のトレイリングカンマ（`,`）を許容する JSONC 形式。文字列リテラル（`"..."`）内の `//` や `/*` はコメントとして誤除去しない字句解析ベースのストリッパーで前処理する。

## **2.2 JSONC スキーマ構造例とフィールド定義**

```jsonc
{
  // スキーマバージョン（固定値: 1）
  "version": 1,

  // プロジェクト基本メタデータ
  "project": {
    "name": "my-app",
    // "go-cli" | "ts-web" | "minimal" | カスタムプリセット名
    "preset": "go-cli"
  },

  // 同期対象とするエージェント（未指定時は全3種）
  "agents": {
    "targets": ["claude", "codex", "gemini"],
    // true の場合、エージェントディレクトリ（.claude 等）が未存在でも新規作成する
    "create_missing": false
  },

  // AGENTS.md のリンク方式設定
  "links": {
    // "auto" | "hard" | "symlink" | "copy"
    "mode": "auto",
    "targets": ["CLAUDE.md", "GEMINI.md"]
  },

  // 任意: .agents/skills/ を正本としたマルチエージェント Skills 連携（省略時はデフォルト有効）
  "skills": {
    "enabled": true,
    "source": ".agents/skills",
    "targets": [".claude/skills"]
  },

  // エージェント横断の共通権限ポリシー（各ツール形式へ自動変換される）
  "permissions": {
    // 許可するシェルコマンドプレフィックス（例: "go test *", "go get *", "git status"）
    "allow_commands": [
      "go build *",
      "go test *",
      "go vet *",
      "gofmt *"
    ],
    // プロジェクト固有の拒否コマンド・ファイルパターン（グローバル設定と衝突する広範なdenyは避ける）
    "deny_commands": [],
    "deny_paths": [
      ".env",
      ".env.*"
    ]
  },

  // サンドボックス・ネットワーク方針（Codex CLI に反映）
  "sandbox": {
    // "workspace-write" | "read-only" | "danger-full-access"
    "mode": "workspace-write",
    // "never" | "on-request" | "untrusted"（省略時は "never"。danger-full-access との併用不可）
    "approval_policy": "never",
    // false の場合、Codex CLI で network_access = false を強制する
    "network_access": false
  },

  // 完了検証フック（Claude Code の Stop フック、Gemini CLI の AfterAgent フックに反映）
  "hooks": {
    // 空文字の場合はフックを生成しない。OS別のコマンド上書きも可能
    "verify_command": "gofmt -s -w . && go vet ./... 1>&2 && go test ./... 1>&2 || exit 2",
    "verify_command_windows": "",
    "verify_command_unix": ""
  },

  // 任意: `reifu run` フェーズ実行・レビュー自動化ランナー設定（省略時はCLIフラグのみで動作）
  "runner": {
    "implementer": "codex",
    "reviewer": "claude",
    // 任意: "off"（既定） | "end" | "phase"
    "notify": "off",
    // 内蔵エージェント（claude / codex / gemini / agy）の起動引数上書きやカスタム定義（シェル非経由）
    "agents": {}
  }
}
```

`runner.notify` は省略、空文字列、`off`、`end`、`phase` を受け付ける。解決順は `--notify`、`--resume` 時の保存値、`runner.notify`、既定の `off`。解決値を `.reifu.run.json` とラン履歴の `run.json` に `notify` として保存する。旧ランの状態にこのキーがなければ設定ファイルへフォールバックする。

## **2.3 Go データ型定義（`internal/model`）**

```go
package model

type SchemaVersion int

const CurrentSchemaVersion SchemaVersion = 1

type LinkMode string

const (
	LinkModeAuto    LinkMode = "auto"
	LinkModeHard    LinkMode = "hard"
	LinkModeSymlink LinkMode = "symlink"
	LinkModeCopy    LinkMode = "copy"
)

type AgentName string

const (
	AgentClaude AgentName = "claude"
	AgentCodex  AgentName = "codex"
	AgentGemini AgentName = "gemini"
)

type SandboxMode string

const (
	SandboxWorkspaceWrite   SandboxMode = "workspace-write"
	SandboxReadOnly         SandboxMode = "read-only"
	SandboxDangerFullAccess SandboxMode = "danger-full-access"
)

type ApprovalPolicy string

const (
	ApprovalNever     ApprovalPolicy = "never"
	ApprovalOnRequest ApprovalPolicy = "on-request"
	ApprovalUntrusted ApprovalPolicy = "untrusted"
)

type ReifuConfig struct {
	Version     SchemaVersion    `json:"version"`
	Project     ProjectMeta      `json:"project"`
	Agents      AgentsConfig     `json:"agents"`
	Links       LinksConfig      `json:"links"`
	Permissions PermissionsModel `json:"permissions"`
	Sandbox     SandboxModel     `json:"sandbox"`
	Hooks       HooksModel       `json:"hooks"`
}
```

## **2.4 バリデーション・セキュリティ検証ルール**

`.reifu.jsonc` の読み込み時および CLI 引数解析時に以下を厳格検証し、違反時は終了コード `1` でエラー終了する。

1. **`version`**: `CurrentSchemaVersion`（`1`）以外はエラー。
2. **`project.preset` / `--preset` / `preset export` 引数**: `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$` に一致しない値（`/`、`\`、`..` を含む値など）はパストラバーサル防止のため拒否する。
3. **`links.targets`**: プロジェクトルート配下の相対パスのみ許可する。空文字、絶対パス、`..` を含むパス、`AGENTS.md` 自身は拒否する。
4. **`agents.targets`**: `claude` / `codex` / `gemini` 以外はエラー。
5. **`links.mode`**: `auto` / `hard` / `symlink` / `copy` 以外はエラー（空時は `auto`）。
6. **`sandbox.mode` / `sandbox.approval_policy`**:
   * `sandbox.mode` は `workspace-write` / `read-only` / `danger-full-access` 以外はエラー（空時は `workspace-write`）。
   * `sandbox.approval_policy` は `never` / `on-request` / `untrusted` 以外はエラー（空時は `never`）。
   * **危険構成ガード**: `sandbox.mode == "danger-full-access"` かつ `sandbox.approval_policy == "never"` の同時指定は、完全無確認・無サンドボックス暴走を防ぐためバリデーションエラーとする。
7. **インジェクション防止（改行・制御文字禁止）**: `allow_commands`、`deny_commands`、`deny_paths`、`hooks.verify_command*`、`project.name` に改行文字（`\r`, `\n`）や NUL バイト（`\x00`）が含まれる場合はエラーとする。
8. **`runner`**:
   * `runner.implementer` / `runner.reviewer`: 省略または空文字許容。指定時は `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$` に一致すること。
   * `runner.notify`: 省略、空文字、`off`、`end`、`phase` のいずれか。
   * `runner.agents`: キーは上記パターンに一致し、値は空でない `[]string`（`argv[0]` の空白のみ不可）。全要素に制御文字（`\r`, `\n`, `\x00` 等）を含まないこと。また `argv[0]`（実行ファイル名）にプロンプトプレースホルダ `{prompt}` を含めることは禁止し、`argv[1:]` でのみ許容する。

# **3\. `.reifu.state.json`（状態スナップショット）スキーマ**

ドリフト検知（外部変更の検出）、3-way diff マージ（前回追加分の安全な削除・更新）、および逆取り込み（`--import`）のために保持する内部状態ファイル。

```json
{
  "version": 1,
  "last_synced_at": "2026-11-26T14:00:00Z",
  "master_sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "managed_commands": [
    "go build *",
    "go test *",
    "go vet *",
    "gofmt *"
  ],
  "targets": {
    "claude": {
      "path": ".claude/settings.json",
      "exists": true,
      "file_sha256": "8f434346648f6b96df89dda901c5176b10a6d83961dd3c1ac88b59b2dc327aa4",
      "applied_allow_commands": [
        "go build *",
        "go test *",
        "go vet *",
        "gofmt *"
      ],
      "applied_deny_commands": [],
      "applied_deny_paths": [
        ".env",
        ".env.*"
      ],
      "applied_verify_command": "gofmt -s -w . && go vet ./... 1>&2 && go test ./... 1>&2 || exit 2",
      "observed_allow_commands": [
        "go build *",
        "go test *",
        "go vet *",
        "gofmt *"
      ]
    },
    "codex": {
      "path": ".codex/config.toml",
      "exists": true,
      "file_sha256": "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae",
      "applied_allow_commands": [],
      "applied_deny_commands": [],
      "applied_deny_paths": [],
      "applied_verify_command": ""
    },
    "gemini": {
      "path": ".gemini/settings.json",
      "exists": true,
      "file_sha256": "4b227777d4dd1fc61c6f884f48641d02b4d121d3fd328cb08b5531fcacdabf8a",
      "applied_allow_commands": [
        "go build *",
        "go test *",
        "go vet *",
        "gofmt *"
      ],
      "applied_deny_commands": [],
      "applied_deny_paths": [],
      "applied_verify_command": "gofmt -s -w . && go vet ./... 1>&2 && go test ./... 1>&2 || exit 2"
    }
  }
}
```

# **4\. 各エージェント形式へのコンパイル・マージ仕様**

## **4.1 Claude Code（`.claude/settings.json`）への変換と非破壊マージ**

* **許可コマンド変換ルール**: `permissions.allow_commands` の各要素 `<cmd>` を `"Bash(<cmd>)"` に変換し、`permissions.allow` 配列へ 3-way diff マージする（重複排除）。
* **拒否コマンド変換ルール**: `permissions.deny_commands` の各要素 `<cmd>` を `"Bash(<cmd>)"` に変換し、`permissions.deny` 配列へ 3-way diff マージする。
* **パス拒否変換ルール**: `permissions.deny_paths` の各要素 `<p>` を `"Read(<p>)"`、`"Edit(<p>)"` の 2 エントリに変換し、`permissions.deny` 配列へ 3-way diff マージする（Claude Code では `Edit(...)` が `Edit` / `Write` 等のファイル変更系ツール全般をカバーし、`Write(<path>)` ルールはマッチしないため出力しない。また既存の `permissions.deny` にパス付き `Write(<path>)` が含まれている場合は、`Edit(<path>)` へ自動正規化し、同一の `Edit(<path>)` が既にあれば重複排除する）。
* **フック変換ルール（`hooks.Stop`）**:
  * OS 別解決済み `verify_command`（`--no-hooks` 指定時は空扱い）を以下の構造で設定する:
    ```json
    {
      "hooks": [
        {
          "type": "command",
          "command": "<resolved_verify_command>"
        }
      ]
    }
    ```
  * 既存の `hooks.Stop` 配列に `state.applied_verify_command` と一致するコマンドを持つ霊符管理エントリが存在する場合:
    * 新 `verify_command` が空でない場合はそのコマンドをインプレース更新する。
    * 新 `verify_command` が空の場合はその霊符管理エントリのみ削除する（ユーザー手動追加フックは保持する）。
  * 既存の霊符管理エントリが存在せず、新 `verify_command` が空でない場合は `hooks.Stop` の先頭に追加する。
* **非破壊 3-way diff マージ**: 既存の `.claude/settings.json` を `map[string]any` として読み込み、霊符管理外キーはすべて保持する。`permissions.allow` / `permissions.deny` からは、前回の `state.applied_allow_commands` / `applied_deny_commands` / `applied_deny_paths` に含まれていたが今回の `.reifu.jsonc` から削除されたルールのみをピンポイントで除去する。

## **4.2 Gemini CLI（`.gemini/settings.json`）への変換と非破壊マージ**

※ Antigravity CLI（`agy`）は `.gemini/settings.json` を読み込まないため、`reifu run` 実行時に `.reifu.jsonc` の `permissions` をセッション限定設定（`--settings`）として適用する（4.4 節参照）。

* **コンテキストバインド**: `context.fileName` に `"AGENTS.md"` を設定する。
* **コマンド変換ルール**:
  * `permissions.allow_commands` の各要素 `<cmd>` を `"run_shell_command(<cmd>)"` に変換し、`tools.allowed` 配列へ 3-way diff マージする。
  * `permissions.deny_commands` の各要素 `<cmd>` を `"run_shell_command(<cmd>)"` に変換し、`tools.exclude` 配列へ 3-way diff マージする。
* **`deny_paths` / `sandbox` の扱い**: MVP では Gemini CLI の `settings.json` へ `deny_paths` および `sandbox` は変換しない（既存のユーザー定義キーを破壊せず保持する）。
* **フック変換ルール（`hooks.AfterAgent`）**:
  * Claude Code の `hooks.Stop` と同一の構造・同一の `state.applied_verify_command` ベース非破壊更新／削除ルールで `hooks.AfterAgent` に反映する。

## **4.3 Codex CLI（`.codex/config.toml`）への変換とコメント保持マージ**

* **スカラー設定マッピング**:
  * `approval_policy = "<sandbox.approval_policy>"`（デフォルト `"never"`）
  * `sandbox_mode = "<sandbox.mode>"`
  * `[sandbox_workspace_write]` セクション配下に `network_access = <sandbox.network_access>`（`true` または `false`）
* **TOML パーサー/ライター仕様（ゼロ依存・インジェクション耐性）**:
  * 既存の `.codex/config.toml` を行単位で解析し、管理対象キー（ルート直下の `approval_policy`, `sandbox_mode`、および `[sandbox_workspace_write]` 配下の `network_access`）のみ値を置換・追記し、既存のコメントや他セクション（`[mcp_servers.*]` 等）は完全に保持する。
  * 書き出す値は 2.4 の列挙値バリデーションを通過した値のみを許可し、文字列は必ず二重引用符でクオートして TOML セクション／キー注入を防ぐ。

## **4.4 `reifu run` エージェント起動定義・`{prompt}` 引数展開・`agy` セッション設定（`--settings`）**

### 既定エージェントコマンド一覧

| エージェント名 | 既定 `argv` | プロンプト引き渡し | 備考 |
| :---- | :---- | :---- | :---- |
| `claude` | `["claude", "-p"]` | `stdin` | `.claude/settings.json` を参照 |
| `codex` | `["codex", "exec", "-s", "<sandbox.mode>", "-"]` | `stdin` | `<sandbox.mode>` は `.reifu.jsonc` の `sandbox.mode`（既定 `workspace-write`） |
| `gemini` | `["gemini", "-p", ""]` | `stdin` | `.gemini/settings.json` を参照 |
| `agy`（別名: `antigravity`） | `["agy", "--mode", "accept-edits", "--output-format", "stream-json", "--print={prompt}"]` | 引数（`{prompt}` 展開、`stdin` なし） | `.reifu.jsonc` の `permissions` が非空の場合、ラン開始時に `--settings .reifu/runs/<run-id>/agy-settings.json` を自動付与。`stream-json` のツール呼び出しと `result` を解析し、失敗判定とコマンド履歴記録を行う |

### `{prompt}` プレースホルダと長文プロンプト退避

* `argv[1:]` のいずれかの要素に `{prompt}` が含まれる場合、UTF-8 BOM を除去し改行を LF へ正規化したプロンプト本文をシェル非経由（`exec.CommandContext`）で該当要素へ展開し、`cmd.Stdin` には渡さない（`nil`）。
* Windows 互換エスケープ後のコマンドライン長が **24,000 UTF-16 コード単位**を超える場合、本文を `.reifu/runs/<run-id>/<phase>_<step>_prompt.md` へ UTF-8 / LF でアトミック書き出しし、`Read the full instructions from <path> and follow them completely.` を `{prompt}` へ展開する。退避後なお 32,767 UTF-16 コード単位を超える場合はエラー終了する。

### Antigravity CLI（`agy`）用セッション設定コンパイル（`internal/compiler.CompileAgySettings`）

`reifu run` で内蔵 `agy`（`IsCustom == false`）を使用する場合、`.reifu.jsonc` の `permissions` から `.reifu/runs/<run-id>/agy-settings.json` を生成して `--settings` へ渡す（`allow`・`deny` がともに空なら省略）。グローバル設定（`~/.gemini/antigravity-cli/settings.json`）は一切変更しない。

* **`permissions.allow_commands` → `permissions.allow`** / **`permissions.deny_commands` → `permissions.deny`**:
  * `*` 単独 → `"command(*)"`
  * ワイルドカード（`*` / `?`）なし（例: `go version`）→ `"command(<cmd>)"`。`deny_commands` に限っては Windows の完全一致仕様でサブコマンド／引数付き実行がすり抜けないよう `"command(regex:<quoted> .*)"` も併記する。
  * 末尾 `" *"`（例: `go test *`、`git push *`、`rm -rf *`）→ `"command(<prefix>)"`（プレフィックスにワイルドカードがある場合は `regex:` 形式）と `"command(regex:<glob→regex>)"` の両方を出力し、POSIX 語プレフィックス一致と Windows 行全体 `regex:` 一致の双方で動作させる。
  * その他の `*` / `?` を含むパターン（例: `git reset --hard*`、`reifu status*`）→ `"command(regex:<glob→regex>)"`（`regexp.QuoteMeta` 後に `\*` → `.*`、`\?` → `.`）。
* **`permissions.deny_paths` → `permissions.deny`**:
  * `agy` の `read_file(<path>)` はパスプレフィックス一致（かつ `read_file` 拒否が `write_file` 拒否を暗黙に含む）であるため、`*` / `?` を含まないリテラルパス（例: `.env`）を正規化して `"read_file(<path>)"` として出力する（glob パターンはスキップ）。
* 優先順位は `agy` のパーミッションエンジンに従い **`Deny > Ask > Allow`** が保証される。

### フェーズ別コマンド履歴（`.reifu/runs/<run-id>/<phase>_commands.json`）スキーマ

`agy`（`--output-format stream-json` または `json`）の実行時に取得できたコマンド実行・拒否履歴をフェーズ単位で UTF-8 / LF の JSON として保存する。`PhaseRunRecord`（`.reifu.run.json` / `run.json`）には任意フィールド `commands_log`（相対パス）と `denied_commands`（ヘッドレス自動拒否されたコマンドまたはツール名の配列）を記録する。`--resume` で同一ステップを再実行した場合は当該ステップのエントリを最新結果で置き換える。

```json
{
  "version": 1,
  "phase": "01_setup",
  "commands": [
    {
      "step": "implement",
      "agent": "agy",
      "tool": "run_command",
      "command": "go test ./...",
      "status": "allowed"
    },
    {
      "step": "implement",
      "agent": "agy",
      "tool": "run_command",
      "command": "cargo test --all",
      "status": "denied",
      "denied_by": "headless_auto_deny",
      "error": "permission check failed for unsandboxed \"cargo test --all\": user denied permission to run command:"
    }
  ]
}
```

| フィールド | 型 | 説明 |
| :---- | :---- | :---- |
| `version` | int | 固定値 `1` |
| `phase` | string | 対象フェーズ名（拡張子なし） |
| `commands[].step` | string | `"implement"` または `"review"` |
| `commands[].agent` | string | エージェント名（例: `"agy"`） |
| `commands[].tool` | string | ツール名（例: `"run_command"`, `"Bash"`） |
| `commands[].command` | string (任意) | 実行しようとしたコマンド文字列（取得できた場合） |
| `commands[].status` | string | `"allowed"` / `"denied"` / `"error"` |
| `commands[].denied_by` | string (任意) | `"headless_auto_deny"`（許可不足による自動拒否）または `"deny_rule"`（`deny_commands` 等の明示拒否ルール一致） |
| `commands[].error` | string (任意) | 拒否・エラー時の先頭行メッセージ |

# **5\. 状態遷移とドリフト検知・逆取り込み（`--import`）アルゴリズム**

## **5.1 初期状態（`.reifu.state.json` 未存在時 / `git clone` 直後）の扱い**

* `.reifu.state.json` は `.gitignore` 対象のため、`git clone` 直後は未存在となる。
* `.reifu.state.json` が未存在の場合:
  * `reifu status`（通常実行）は `STATE_MISSING` と再同期推奨の案内を表示し、終了コード `0` を返す。
  * `reifu status --quiet` は未同期状態として終了コード `2` を返す。
* `reifu sync` 実行時に `.reifu.state.json` が未存在の場合、`.reifu.jsonc` の内容を基準として各ターゲット設定へ非破壊マージ（初回ベースライン同期）を実行し、最新の SHA-256 ハッシュと適用済みルールを記録した `.reifu.state.json` を新規生成する。
* **フック適用警告（セキュリティ対策）**: 初回ベースライン同期時、または前回 `state` と比較して `verify_command` が新規設定・変更される同期時には、設定されるフックコマンド文字列を警告する。通常は `stderr`、`--json` 時は `warnings` に出力する。`--no-hooks` 指定時はフックの反映を抑止する。

## **5.2 状態判定マトリクス（ファイル単位）**

`reifu sync` および `reifu status` は各ターゲットファイルについて以下の 4 状態（および未存在スキップ時は `SKIPPED`、state 未存在時は `STATE_MISSING`）を判定する。

| 状態コード | 条件 | `reifu sync`（デフォルト）の挙動 | `reifu sync --import` の挙動 |
| :---- | :---- | :---- | :---- |
| `IN_SYNC` | `current_file_sha256 == state.file_sha256` かつ `current_master_sha256 == state.master_sha256` | 何もしない（スキップ） | 何もしない（スキップ） |
| `MASTER_UPDATED` | `current_file_sha256 == state.file_sha256` かつ `current_master_sha256 != state.master_sha256` | マスターの変更をターゲットへコンパイル反映 | マスターの変更をターゲットへ反映 |
| `DRIFTED` | `current_file_sha256 != state.file_sha256` かつ `current_master_sha256 == state.master_sha256` | **エラー終了（Exit Code 2）**。外部変更がある旨と `--import` / `--force` の案内を出力 | ターゲットの `permissions.allow`（Claude）または `tools.allowed`（Gemini）から新規追加コマンドを抽出し、安全検証後に `.reifu.jsonc` の `allow_commands` へ逆追記した上で全ターゲットへ再同期 |
| `CONFLICT` | `current_file_sha256 != state.file_sha256` かつ `current_master_sha256 != state.master_sha256` | **エラー終了（Exit Code 2）**。双方変更競合を警告 | 全ドリフト対象から新規追加コマンドを収集・重複排除して `.reifu.jsonc` へマージ後、マスターを正として全ターゲットへ再同期（`deny_commands` との衝突や危険コマンド検出時は取り込み拒否・警告出力し Exit Code `2`） |

* **外部削除コマンドの扱い**: `--import` は外部追加されたコマンドの逆取り込みのみを行う。コマンドを削除したい場合は正本である `.reifu.jsonc` を直接編集して `reifu sync` する。

## **5.3 逆取り込み（`--import`）のパース規則・安全ガード・コメント保持編集**

* **抽出ルール**:
  * Claude Code の `.claude/settings.json` 内の `permissions.allow` から `"Bash(<pattern>)"` 形式の文字列を抽出し、`<pattern>` を取り出す。
  * Gemini CLI の `.gemini/settings.json` 内の `tools.allowed` から `"run_shell_command(<pattern>)"` 形式の文字列を抽出し、`<pattern>` を取り出す。
  * 複数ターゲットから抽出された `<pattern>` は順序を保って重複排除し、`.reifu.jsonc` の `permissions.allow_commands` に未登録のもののみ候補とする。
* **危険コマンド・矛盾ガード（セキュリティ要件）**:
  * 候補 `<pattern>` が `.reifu.jsonc` の `permissions.deny_commands` と一致する場合、または以下に該当する過度に広範・危険なパターンの場合は、`--force` が明示されない限り自動取り込みを拒否し `stderr` に警告を出力する（終了コード `2`）:
    * ワイルドカード単独（`*`）または空文字
    * 任意シェル起動（`sh *`, `bash *`, `zsh *`, `pwsh *`, `powershell *`, `cmd *` および引数なし一致）
    * 高リスク操作（`rm -rf *`, `git push *`, `git reset --hard*`, `curl *`, `wget *`）
    * 改行・制御文字を含むパターン
* **コメント保持テキスト挿入仕様（`internal/jsonc` が提供）**:
  * 構造体全体を再シリアライズしてコメントを消失させるのではなく、字句解析により `"allow_commands"` 配列の閉じ括弧 `]` の直前位置を特定してテキスト挿入し、既存の `//` および `/* */` コメントを完全に保持する。
  * 挿入する文字列は必ず `json.Marshal(cmd)` でエスケープする。
  * 以下のコーナーケースを正しく処理すること:
    1. **空配列 `[]`**: 先頭カンマを付けずに挿入する。
    2. **末尾トレイリングカンマあり**: 二重カンマ（`,,`）にしない。
    3. **末尾要素の同一行に `//` コメントあり**: コメントの後ろではなく末尾要素の直後にカンマを置くか、構文破壊しない位置へ挿入する。
    4. **1行配列 `["a", "b"]` と複数行配列の両対応**: 既存インデントに合わせて挿入する。
  * 挿入後のテキストは書き込み前に `jsonc.Unmarshal` で構文自己検証し、万一検証に失敗した場合は書き込まずエラーを返す。`"allow_commands"` キー自体が存在しない場合のみフォールバックとして整形 JSON 再出力を行う。

# **6\. CLI サブコマンド・フラグ・JSON 出力仕様**

## **6.1 サブコマンド別フラグ一覧**

| サブコマンド | フラグ | 概要 |
| :---- | :---- | :---- |
| `reifu new <dir>` | `--name <str>` / `--preset <name>` / `--spec <file>` / `--no-docs` / `--no-settings` / `--no-hooks` / `--link-mode <mode>` / `--agents <csv>` / `--dry-run` | 新規ディレクトリ `<dir>`（必須・既存時はエラー）を作成して雛形生成・リンク作成・初期コンパイル・state 保存。`--spec` 指定時は既存仕様書を `docs/specs/000_source_spec.md` へ取り込み `prompts/phase0.md` を仕様書ベースモードで生成。 |
| `reifu init [dir]` | `--name <str>` / `--preset <name>` / `--spec <file>` / `--no-docs` / `--no-settings` / `--no-hooks` / `--link-mode <mode>` / `--agents <csv>` / `--force` / `--dry-run` | 既存ディレクトリへの雛形生成・リンク作成・初期コンパイル・state 保存。`--agents` 指定時は `.reifu.jsonc` の `agents.targets` にも指定値のみ設定する。ホームディレクトリ直下は無条件拒否。 |
| `reifu link [dir]` | `--mode <mode>` / `--check` / `--force` | `AGENTS.md` からのリンク検証（`--check` 不一致時は Exit `3`）および再構築。 |
| `reifu sync [dir]` | `--import` / `--force` / `--create` / `--no-hooks` / `--dry-run` | `.reifu.jsonc` から各エージェント設定への非破壊同期・逆取り込み。 |
| `reifu status [dir]` | `--quiet` / `--json` | 環境判定・リンク状態・設定同期状態の診断出力。`--json` 時は `stdout` に単一 JSON のみ出力。 |
| `reifu preset list` | （なし） | 内蔵およびユーザー設定ディレクトリ（`os.UserConfigDir()/reifu/presets/`）のプリセット一覧を表示（オーバーライド元も明示）。 |
| `reifu preset export <name>` | `--out <dir>` / `--force` | 指定プリセット `<name>` を出力先ディレクトリ（省略時は `os.UserConfigDir()/reifu/presets/`）へ `<name>.jsonc` として書き出す。既存ファイルがある場合は `--force` なしならエラー。 |
| `reifu hook verify [dir]` | （なし） | `.reifu.jsonc` の `workflow.verify_steps` をシェル非経由で順次実行するクロスプラットフォーム検証フック。`stop_hook_active: true`、`BLOCKED_REPORT.md` 存在、`workflow.require_files` 未存在、未定義 npm スクリプト時は Exit `0` でスキップ。失敗時は `stderr` へ出力し Exit `2`（`stdout` 非汚染）。 |
| `reifu run [dir]` | `--implementer <agent>` / `--reviewer <agent>` / `--phase <name>` / `--from <name>` / `--no-review` / `--no-commit` / `--allow-dirty` / `--resume` / `--timeout <dur>` / `--notify[=mode]` / `--dry-run` / `--json` | `prompts/phases/*.md` を辞書順に「実装 → コミット → `prompts/review.md` レビュー → コミット」で自動実行。`claude` / `codex` / `gemini` / `agy`（`antigravity`）対応。進捗を `.reifu.run.json`、ログ・レビュー結果を `.reifu/runs/<run-id>/` へ保存。`--notify` で完了／停止時の通知音・OS 通知に対応。 |
| `reifu logs [dir]` | `--list` / `--run <id>` / `--phase <name>` / `--review` / `-f` (`--follow`) / `--json` | `reifu run` の実行進捗サマリー・実装ログ・レビューレポートを安全に読み取り専用で閲覧（`-f` は都度クローズ方式でロック競合ゼロ）。 |

## **6.2 `reifu status --json` 出力スキーマ**

```json
{
  "root": "/abs/path/to/project",
  "env": "windows",
  "state_missing": false,
  "has_issues": false,
  "links": [
    {
      "target": "CLAUDE.md",
      "status": "OK",
      "mode": "hard"
    }
  ],
  "skills": [
    {
      "target": ".claude/skills/my-skill",
      "status": "OK",
      "mode": "junction"
    }
  ],
  "targets": {
    "claude": {
      "path": ".claude/settings.json",
      "status": "IN_SYNC"
    },
    "codex": {
      "path": ".codex/config.toml",
      "status": "IN_SYNC"
    },
    "gemini": {
      "path": ".gemini/settings.json",
      "status": "SKIPPED"
    }
  }
}
```

# **7\. エラーコードと終了ステータス仕様**

| 終了コード | エラー種別 | 発生条件とメッセージ方針 |
| :---- | :---- | :---- |
| `0` | 正常終了 | すべての処理が正常完了（`status --quiet` でドリフト・未同期・リンク切れなしの場合も `0`） |
| `1` | 一般エラー / バリデーション失敗 | 引数不正、`.reifu.jsonc` 構文エラー、バリデーション違反（パストラバーサル・危険構成等）、I/O権限エラー、対象ディレクトリ不存在 |
| `2` | 設定ドリフト / 衝突 / 危険取り込み検知 | `sync` 時に外部変更（`DRIFTED` / `CONFLICT`）や `--import` 時の危険コマンド・矛盾を検知、または `status --quiet` で `STATE_MISSING` / ドリフト / リンク切れを検知、`hook verify` 失敗、`run` の安全停止（エージェント異常終了・タイムアウト・Ctrl+C 中断・`BLOCKED_REPORT.md` 検出） |
| `3` | リンク整合性エラー | `link --check` 時に `CLAUDE.md` / `GEMINI.md` が `AGENTS.md` と異なる inode / 実体になっている場合 |

## コンパニオン API のレスポンス

操作系コマンドの `--json` は単一 JSON 値を返す。以下の必須配列は項目がなければ `[]` になる。リンク項目は既存の `LinkReport`（`target`, `status`, 任意の `mode`）、設定対象は既存の `TargetReport`（`path`, `status`）を使う。

| 型 | JSON フィールド |
| --- | --- |
| プリセット一覧 | `name` と `source` を持つオブジェクトの配列 |
| `SyncReport` | `root`, `dry_run`, `state_missing`, `changed_paths`（パス配列）, `warnings`（文字列配列）, `targets`（エージェント名から設定状態へのマップ）, `skills`（同期計画時のリンク状態） |
| `LinkCommandReport` | `root`, `mode`, `check`, `has_issues`, `links`, `skills`。検査結果に問題があれば JSON 出力後に Exit 3 |
| `SetupReport` | `command`（`new` / `init`）, `root`, `preset`, `dry_run`, `files`（雛形ファイルのパス配列）, `links`, `changed_paths`（設定・状態・スキルの同期対象パス配列）, `warnings`。dry-run の `links` は予定の宛先と現在の状態を示す |
| `RunnerStatus` | `blocked`, `git_repo`, `git_dirty`, `phases`（拡張子なしの昇順の名前配列）, `agents`（名前昇順の `AgentStatus` 配列）, `latest_run`（取得できた場合の `RunSummary`） |
| `AgentStatus` | `name`, `available`, `is_custom`, `executable`（検出した場合の絶対パス） |
| `RunSummary` | `run_id`, `started_at`, `updated_at`, `status`, `completed`, `total` |

`status --json` は既存のレポートに `runner` を追加する。取得できない `latest_run` は省略する。ランナーの状態は `has_issues` と `status --quiet` の終了判定から独立している。設定ファイルが未作成のディレクトリでも `status --json` は `runner` と `state_missing: true` を返す。

## `RunEvent` の NDJSON スキーマ

`run --stream-json` は各行を UTF-8 / LF の JSON オブジェクトとして出力する。常に `schema_version: 1`、`type`、`timestamp`（UTC RFC3339Nano）、`elapsed_ms` を含む。`elapsed_ms` が意味を持つのは `heartbeat`、`step_completed`、`phase_completed`、`phase_failed` で、他のイベントでは `0` になる。以下の追加フィールドは空なら省略する。

| `type` | 主な追加フィールド |
| --- | --- |
| `run_plan` | `plan` |
| `run_started` | `run_id`, `state` |
| `phase_started` | `phase`, `phase_index`（1 始まり）, `phase_total`, `step` |
| `step_started` | フェーズ識別情報, `step`, `agent`, `path`, `warning`（カスタムエージェント） |
| `log` | フェーズ識別情報, `step`, `agent`, `text` |
| `heartbeat` | フェーズ識別情報, `step`, `agent` |
| `commit` | フェーズ識別情報, `step`, `commit` |
| `step_completed` | フェーズ識別情報, `step`, `agent`, `commit`, `path` |
| `phase_completed` | フェーズ識別情報, `status: "completed"` |
| `phase_failed` | フェーズ識別情報, `status: "failed" \| "blocked"`, `error`, `denied_commands`（許可不足で拒否されたコマンドがある場合） |
| `run_completed` | `run_id`, `status: "completed"`, `state` |
| `run_failed` | `run_id`, `status: "failed" \| "blocked"`, `error`, `denied_commands`（許可不足で拒否されたコマンドがある場合）, `state` |

ランごとの終端イベントは `run_plan`、`run_completed`、`run_failed` のうち 1 行。ランナーが終端を送る前にエラー終了した場合は、CLI が `run_failed` を補う。フォールバックの `status` は `BLOCKED_REPORT.md` による Exit 2 で `blocked`、それ以外で `failed` になる。`--resume` で `implement_commit` / `review_commit` から再開したステップでは `commit` / `step_completed` が出て、`step_started` は省略される。キャンセルとタイムアウトはどちらも `failed` になり、`error` の `context canceled` / `context deadline exceeded` で区別できる。
