# 002_skills_sync — `.agents/skills/` 正本によるマルチエージェント Skills 連携

## 1. 背景・目的

Claude Code / Codex CLI / Gemini CLI / Antigravity の 4 エージェントはいずれもオープン標準の **Agent Skills**（`<skill-name>/SKILL.md` ＋任意の `scripts/`・`references/`・`assets/`）に対応している。

しかしプロジェクトローカルの探索パスには以下の差異がある：

* **Antigravity / Codex CLI / Gemini CLI**: `.agents/skills/<name>/SKILL.md` を標準探索
* **Claude Code**: `.claude/skills/<name>/SKILL.md` を探索

これは `AGENTS.md`（正本）と `CLAUDE.md` / `GEMINI.md`（リンク）と同一の構図である。本仕様では **`.agents/skills/` を Skills の単一ソース（SSOT）** とし、既存の `reifu link`・`reifu status`・`reifu sync`（および既存 `.agents/skills/` がある場合の `reifu init`）から `.claude/skills/<name>` へスキル単位で安全にリンク同期・検証・クリーンアップできる機能を追加する。

---

## 2. スコープ

### In-Scope
* `.reifu.jsonc` に任意の **`skills`** セクションを追加（省略時はデフォルト有効で既存設定と 100% 後方互換）
* `.agents/skills/<name>/SKILL.md` を自動検出し、スキルディレクトリ単位で `.claude/skills/<name>` へリンク同期
* クロスプラットフォームのディレクトリリンク戦略（`internal/fsx`）：
  * **Unix（macOS / Linux / WSL2）**: 相対シンボリックリンク（例: `../../.agents/skills/<name>`）→ 失敗時はディレクトリコピーへフォールバック
  * **Windows**: 相対シンボリックリンク → 管理者権限・開発者モード不要の **NTFS ディレクトリジャンクション** → 失敗時はディレクトリコピーへフォールバック
* 既存コマンドへの統合（新規サブコマンドを増やさない）：
  * **`reifu link [dir]`**: `AGENTS.md` リンクに加えてスキルリンクの検証（`--check` 不一致時は Exit `3`）・構築・再構築（`--force`）を実行
  * **`reifu status [dir]`**: スキルリンクの状態を表示（`--json` の `skills` フィールド、`--quiet` で不整合時 Exit `2`）
  * **`reifu sync [dir]`**: 設定コンパイルと併せてスキルリンク同期・削除済み管理スキルのクリーンアップを実行（`--dry-run` 対応）
  * **`reifu init [dir]`**: 既に `.agents/skills/` が存在するプロジェクトに導入する場合のみ自動リンク（`reifu new` / `init` で空の `.agents/skills/` は作成しない）
* 手動配置の Claude 固有スキル（`.agents/skills/` に存在しない `.claude/skills/<custom>`）の非破壊保護

### Out-of-Scope
* 新規プロジェクト作成時の `.agents/skills/` やサンプルスキルの自動生成（YAGNI）
* グローバルスキルディレクトリ（`~/.claude/skills/` や `~/.agents/skills/` 等）の操作
* リモートからのスキルダウンロード／マーケットプレイス連携
* `.claude/skills/` 全体の `.gitignore` 自動登録（手動の Claude 専用スキルと共存するため強制 ignore しない）

---

## 3. 詳細仕様

### 3.1 `.reifu.jsonc` の `skills` 拡張スキーマ（任意・完全後方互換）

```jsonc
  // 任意: .agents/skills/ を正本としたマルチエージェント Skills 連携
  "skills": {
    // 省略時は true。false にするとスキル同期を無効化
    "enabled": true,
    // 省略時は ".agents/skills"
    "source": ".agents/skills",
    // 省略時は agents.targets に "claude" を含む場合 [".claude/skills"]、含まない場合 []
    "targets": [".claude/skills"]
  }
```

#### バリデーションルール（`internal/model`）
* `skills.source`: 省略時は `.agents/skills`。指定時はプロジェクトルート配下の相対パスのみ許可（絶対パス、`..`、制御文字、Windows 予約名、`.` は拒否）。
* `skills.targets`: 各要素はプロジェクトルート配下の相対パスのみ許可。`skills.source` 自身、または `skills.source` の親／子孫（包含関係）、重複パス（大文字小文字正規化後）、他のターゲットとの入れ子（親リンク経由で正本へ書き込めてしまうため）は拒否。
* `skills.enabled`: `*bool`（未指定は有効扱い）。

### 3.2 スキル検出ルール
* `skills.enabled == false` または `skills.targets` が空の場合は完全 No-op。
* `skills.source`（デフォルト `.agents/skills`）が存在しない場合：
  * `.reifu.state.json` に管理済みスキル履歴（`managed_skills`）が残っていなければ完全 No-op（`.claude/skills/` を作成しない）。
  * 管理済みスキル履歴が残っている場合のみ、削除された管理下リンクのクリーンアップ対象として扱う。
* `skills.source` が存在する場合：
  * `fsx.ValidateWithinRoot` でプロジェクト内かつ外部シンボリックリンク脱出がないことを検証する。
  * 直下のエントリをファイル名昇順で走査し、以下を満たすサブディレクトリ `<name>` をスキルとして認識する：
    * 隠しディレクトリ（`.` 始まり）でないこと
    * パス区切り・制御文字・`..`・`:` を含まず、末尾が `.` / 空白でないこと（ターゲットのパス要素として使えない名前は認識しない）
    * `<source>/<name>` が通常ディレクトリ（シンボリックリンク／ジャンクション脱出でないこと）であり、直下に通常ファイル `SKILL.md` が存在すること

### 3.3 ディレクトリリンク戦略と状態判定（`internal/fsx`）

`model.LinkMode` にディレクトリジャンクションを表す定数 **`LinkModeJunction LinkMode = "junction"`** を結果表示・状態報告用に追加する（CLI の `--mode` および `.reifu.jsonc` の `links.mode` 入力値は従来通り `auto` / `hard` / `symlink` / `copy`）。

#### 作成・再構築（`fsx.EnsureDirLink(root, source, target, mode, force)`）
* `mode == "copy"`: 通常ディレクトリとして再帰コピー（既存があり一致していれば No-op、差分があれば `--force` 時のみ一時ディレクトリ経由で置換）。
* `mode == "symlink"`: 相対シンボリックリンク（例: `../../.agents/skills/<name>`）を作成（失敗時はフォールバックせずエラー）。
* `mode == "auto"` または `"hard"`（ディレクトリはハードリンク不可のため自動切替）:
  1. 相対シンボリックリンクを試行（成功時は `LinkModeSymlink`）。
  2. Windows で特権エラー等によりシンボリックリンク不可の場合、**NTFS ディレクトリジャンクション**を試行（成功時は `LinkModeJunction`）。
     * ジャンクションは Windows 固有ファイル `internal/fsx/dirlink_windows.go` に隔離し、管理者権限・開発者モード不要の Win32 API（`DeviceIoControl` + `FSCTL_SET_REPARSE_POINT`）で作成する（`cmd.exe` 非起動、UTF-16 パス完全対応）。
  3. 上記がいずれも環境制約で不可（例: WSL2 DrvFS 等）の場合、ファイル単位でディレクトリコピーへフォールバック（成功時は `LinkModeCopy`、`stderr` へ通知）。
* 既存ターゲットがある場合：
  * 既に同一実体を指すリンク（または `mode == "copy"` で内容完全一致）であれば冪等に No-op 成功。
  * 異なる実体・通常ファイル・別リンク先が存在し `--force` 未指定の場合はエラー（既存を破壊しない）。
  * `--force` 指定時は、既存がリンク／ジャンクションなら **`os.Remove`（リンク自体のみ削除し、リンク先本体は絶対に消さない）**、通常ディレクトリならプロジェクト境界検証後に `os.RemoveAll` して差し替える。

#### 状態判定（`fsx.CheckDirLink(root, source, target)`）
* ターゲット未存在 → `MISSING`
* シンボリックリンク／ジャンクションが壊れている（リンク先不存在）→ `BROKEN`
* `os.SameFile(srcInfo, dstInfo)` が true → `OK`（`mode`: `symlink` または `junction`）
* 通常ディレクトリで、配下のすべての相対ファイルパス・内容が `source` と完全一致 → `COPY`（`mode`: `copy`）
* 上記以外（内容差分、別ディレクトリへのリンク、通常ファイルなど）→ `DIFFERENT`

#### セキュリティ制約
* `source` 側および `target` 側の親ディレクトリについて `fsx.ValidateWithinRoot` を必須とし、プロジェクト外へのシンボリックリンク脱出を完全に拒否する。
* コピー実行時・内容比較時に `source` 内にプロジェクト外を指すシンボリックリンクが含まれていた場合は即エラーで拒否する。
* 削除（`fsx.RemoveDirLink`）時は必ず `os.Lstat` でリパースポイント／シンボリックリンクを判別し、リンク先ディレクトリの中身を絶対に削除しないこと。
  * `fsx.RemoveDirLink(root, target, protectedSources...)` は既定の `.agents/skills` を常に保護する。カスタム `skills.source` を使う場合、呼び出し側は必ずそれを `protectedSources` に渡すこと（リンク経由の別名も解決して保護される）。
* Windows では種類を問わずリパースポイント全般（OneDrive のクラウドファイル等を含む）をリンク扱いし、追従・コピーしない（安全側に倒す）。

### 3.4 管理済みスキル追跡とクリーンアップ
* `.reifu.state.json`（`model.ReifuState`）に `ManagedSkills map[string][]string \`json:"managed_skills,omitempty"\``（キー: ターゲットディレクトリ例 `".claude/skills"`、値: 管理済みスキル名一覧）を追加する。
* 正本 `.agents/skills/` からスキル `<removed>` が削除され、`managed_skills` に含まれていた場合：
  * `<target>/<removed>` がシンボリックリンク／ジャンクションであれば、リンク切れを含めリンク自体を削除する。
  * ユーザーが通常ディレクトリとして独自に作り替えた場合は `--force` なしでは削除せず警告にとどめる。
* `.agents/skills/` に存在せず `managed_skills` にも含まれない `.claude/skills/<manual>`（手動の Claude 専用スキル）には一切触れない。

削除を見送った独立ディレクトリと、現在の同期先から外れたディレクトリの管理履歴は保持する。

### 3.5 各サブコマンドの挙動
プロジェクト外を指す宛先リンクは、`link --check` と `status` で `DIFFERENT` として報告し、残りの宛先も検査する。

* **`reifu link [dir]`**:
  * `AGENTS.md` リンクに続けて、検出された全スキル（および要クリーンアップの管理済みスキル）を検査・同期する。
  * `--check` 時は全スキルリンクの状態を出力し、いずれかが不整合（`auto`/`hard`/`symlink` 時の `MISSING` / `BROKEN` / `DIFFERENT` / `COPY`、または `copy` 時の `COPY` 以外）なら Exit `3`（`ExitLinkIntegrity`）を返す。
* **`reifu status [dir]`**:
  * `StatusReport` に `Skills []LinkReport \`json:"skills,omitempty"\`` を追加する（スキル対象がない場合は省略され既存 JSON と完全互換）。
  * スキルリンクに不整合がある場合は `has_issues: true` とし、`--quiet` 時は Exit `2` を返す。
* **`reifu sync [dir]`**:
  * `.claude/` ディレクトリが存在する（または `--create` / `agents.create_missing: true`）かつ `.agents/skills/`（または `managed_skills`）がある場合、スキルリンクの同期・クリーンアップを実行し、`ChangedPaths` と `.reifu.state.json` の `managed_skills` を更新する（`--dry-run` ではプレビューのみ）。
  * 既存の非破壊原則に従い、既存と同名の管理外ディレクトリが `.claude/skills/<name>` にある場合は `--force` なしでエラー終了する。

---

## 4. 受け入れ基準チェックリスト

### モデル・ファイルシステム基盤（`internal/model`, `internal/fsx`）
- [x] `internal/model` に `SkillsConfig`（`enabled` / `source` / `targets`）、`LinkModeJunction`、`ReifuState.ManagedSkills`、`StatusReport.Skills` が追加され、既存 `.reifu.jsonc` / `.reifu.state.json` と 100% 後方互換である
- [x] `SkillsConfig.Validate()` がパストラバーサル・絶対パス・包含関係（source と target の重複/入れ子）・制御文字を拒否する
- [x] `internal/fsx` に `EnsureDirLink` / `CheckDirLink` / `RemoveDirLink` が実装され、Unix は相対シンボリックリンク→コピー、Windows は相対シンボリックリンク→管理者不要ジャンクション→コピーで動作する
- [x] `RemoveDirLink` と `--force` 置換がリンク／ジャンクション削除時に正本ディレクトリ本体を絶対に削除しないことがテストで検証されている
- [x] `EnsureDirLink` / `CheckDirLink` がプロジェクト外シンボリックリンク脱出を拒否することがテストで検証されている

### コマンド統合・同期・ドキュメント（`internal/syncer`, `internal/app`, `cmd/reifu`）
- [x] `.agents/skills/` が存在しないプロジェクトでは `reifu new` / `init` / `link` / `sync` / `status` の挙動・出力が従来と変わらない（不要な空フォルダを作らない）
- [x] `.agents/skills/<name>/SKILL.md` があるプロジェクトで `reifu link` / `reifu sync` が `.claude/skills/<name>` へスキル単位でリンクを作成し、`reifu link --check` と `reifu status`（通常・`--json`・`--quiet`）が正しく検証できる
- [x] `.claude/skills/` に手動配置された Claude 専用スキルが `reifu link` / `reifu sync` で破壊・削除されない
- [x] `.agents/skills/` からスキルを削除した後の `reifu sync` / `reifu link` で、管理済みリンクのみが安全にクリーンアップされる
- [x] `git clone` 後の独立コピー状態に対して `reifu link --check` が Exit `3`、`reifu status --quiet` が Exit `2`、`reifu link --force` でリンク再構築できる
- [x] `README.md`、`HOWTO.md`、`docs/design/01〜03` に `.agents/skills/` 連携の説明が反映されている
- [x] `gofmt -s -w .`、`go vet ./...`、`go test -v ./...`、`CGO_ENABLED=0 go build ./cmd/reifu` がすべて成功する
