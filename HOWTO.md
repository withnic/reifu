# HOWTO — 霊符（reifu）詳細ガイド

このドキュメントでは、`reifu` の実践的な使い方、日常運用、カスタムプリセットの作成方法、セキュリティ機能の仕組みを詳しく説明します（インストール方法やコマンド一覧は [README.md](README.md) を参照してください）。

---

## 1. プロジェクト立ち上げパターン

### パターン A: ゼロから新規プロジェクトを作る

```sh
reifu new my-app --preset go-cli
cd my-app
git init
```

生成される主なファイル：

```text
my-app/
├── AGENTS.md                   # 行動規範の正本
├── CLAUDE.md                   # AGENTS.md へのハードリンク（環境に応じて自動選択）
├── GEMINI.md                   # AGENTS.md へのハードリンク
├── .reifu.jsonc                # 全エージェント設定の正本（SSOT）
├── .reifu.state.json           # 同期スナップショット（.gitignore 済み）
├── .claude/settings.json       # Claude Code 設定
├── .codex/config.toml          # Codex CLI 設定
├── .gemini/settings.json       # Gemini CLI / Antigravity 設定
├── docs/
│   ├── design/                 # 【常に最新】全体設計（ファイル / DB / ADR 含む）
│   │   ├── 01_project_overview.md
│   │   ├── 02_data_modeling.md
│   │   └── 03_architecture.md
│   └── specs/                  # 【機能ごとに増える】仕様書兼チェックリスト
│       └── README.md
└── prompts/
    ├── README.md               # 【人間用】実行ガイド・ヘッドレス連続実行コマンド例
    ├── phase0.md               # 【AI用・対話】全体設計＋prompts/phases/01〜NN 生成
    ├── add_feature.md          # 【AI用・対話】docs/specs/NNN_xxx.md 作成 → design 更新 → 追加フェーズ生成
    ├── review.md               # 【AI用】共通レビュー＋ドメイン知見を docs/design/ へ還元
    └── phases/                 # 【自動実行キュー】実装フェーズ（01_xxx.md ...）専用
        └── .gitkeep
```

その後、好きな AI エージェントに `prompts/phase0.md` を渡して以下を進めます：
1. 対話による要件整理と `docs/design/01〜03`・`docs/specs/001_initial_mvp.md` の作成
2. 規模に応じた最適なフェーズ数（`prompts/phases/01_*.md` 〜 `NN_*.md`）の協議・生成
3. 設計・フェーズファイルをコミット後、**`reifu run --implementer codex --reviewer claude`** で全フェーズの **実装 → コミット → `prompts/review.md` でレビュー＆修正 → コミット** を最後まで一気に自動実行（進捗・レビュー内容は **`reifu logs` / `reifu logs --review` / `reifu logs -f`** で確認。詳細は「[6. フェーズの自動実行とログ閲覧](#6-フェーズの自動実行とログ閲覧reifu-run--reifu-logs)」参照）
4. MVP 完了後の機能追加は **`prompts/add_feature.md`** で追加フェーズを作成し、同じ `reifu run` サイクルで継続

### パターン B: 自前の仕様書ファイルから始める（`--spec`）

すでに要件メモや設計案の Markdown / テキストファイルがある場合は `--spec` で渡せます。

```sh
reifu new my-app --preset go-cli --spec ./draft-spec.md
```

* 指定ファイルが `docs/specs/000_source_spec.md`（UTF-8 / LF 正規化済み）として取り込まれます。
* `prompts/phase0.md` が自動的に「`docs/specs/000_source_spec.md` を読み解き、不足・曖昧な点だけ確認して `docs/design/01〜03` と `prompts/phases/01_*.md〜` を作るモード」に切り替わります。

### パターン C: 既存リポジトリへ導入する（`reifu init`）

```sh
cd existing-repo
reifu init --preset ts-web
```

* 誤爆防止のため、`reifu init` で `[dir]` を省略したときに「非空かつ `.git` も `.reifu.jsonc` もないディレクトリ（例: `projects/` のような親フォルダ）」だった場合は `--force` なしでは拒否されます。
* ホームディレクトリ直下への `new` / `init` / `sync` / `link` は `--force` を付けても無条件で拒否されます。

### よく使うフラグ（`new` / `init`）

| フラグ | 説明 |
| :---- | :---- |
| `--preset <name>` | プリセット名（省略時: `minimal`、内蔵: `minimal` / `go-cli` / `ts-web` / `ts-electron` / `rust-cli`） |
| `--name <str>` | プロジェクト名（省略時はディレクトリ名） |
| `--spec <file>` | 既存仕様書を `docs/specs/000_source_spec.md` へ取り込む（`--no-docs` と併用不可） |
| `--agents <csv>` | 対象エージェントを絞る（例: `--agents claude,codex`） |
| `--link-mode <mode>` | リンク方式を指定（`auto` / `hard` / `symlink` / `copy`） |
| `--no-docs` | `docs/` および `prompts/` の生成を省略 |
| `--no-settings` | 各エージェント設定と `.reifu.state.json` の初期生成を省略 |
| `--no-hooks` | 完了検証フックの出力を省略 |
| `--dry-run` | ファイルを書き込まずに変更予定だけ表示 |
| `--force` | 既存ファイルの上書きを許可（`init` のみ） |

---

## 2. 日常開発と設定ドリフト運用

### 機能を追加・仕様変更する（`prompts/add_feature.md` → `reifu run`）

MVP 完了後に新しい機能を追加したり既存仕様を変更したりする場合は、以下のサイクルで進めます。

1. **対話セッションで仕様とフェーズを作る**
   好きな AI エージェントに `prompts/add_feature.md` を渡して要件を伝えると、以下が自動生成されます：
   * 機能仕様書兼チェックリスト `docs/specs/NNN_<feature>.md`
   * 影響箇所のみの `docs/design/01〜03` 更新
   * 追加実装フェーズ `prompts/phases/10_<feature>.md`（複数段階の場合は連番）
2. **設計・仕様・フェーズファイルを先にコミットする**
   ```sh
   git add -A && git commit -m "docs(spec): add <feature> spec and phases"
   ```
3. **追加したフェーズだけ `reifu run` で自動実装＆レビューする**
   `prompts/phases/` に過去の完了済みフェーズが残っている場合は、`--phase`（単一）または `--from`（指定フェーズ以降）を付けて追加分だけを実行します（過去の完了済みフェーズを削除してから `reifu run` しても構いません）。
   ```sh
   # 追加フェーズが 1 件のとき
   reifu run --implementer codex --reviewer claude --phase 10_<feature>

   # 追加フェーズが複数（10_〜11_ 等）のとき
   reifu run --implementer codex --reviewer claude --from 10_<feature>_part1
   ```
4. **レビュー内容と仕様チェックリストを確認する**
   ```sh
   reifu logs             # 進捗とコミット一覧
   reifu logs --review    # レビュアーの指摘・修正内容を確認
   ```

### 設定を変更して全エージェントへ反映する
`.reifu.jsonc` を編集してから `reifu sync` を実行します。

```sh
reifu sync --dry-run   # 事前確認
reifu sync             # 反映
```

### エージェント側で追加された許可ルールを逆取り込みする（`--import`）
作業中に Claude Code 等で「常に許可」を押して `.claude/settings.json` が外部変更された場合、通常の `reifu sync` は上書きを防ぐため終了コード `2` で停止します。

```sh
reifu status                 # DRIFTED を確認
reifu sync --import --dry-run
reifu sync --import          # .reifu.jsonc のコメントを保持したまま追記し他エージェントへ展開
```

**危険コマンドガード**: `*`、シェル起動系（`sh` / `bash` / `pwsh` / `cmd` 等）、`sudo` / `env` ラッパー、`rm -rf *`、`git push *`、`git reset --hard*`、`curl *` / `wget *`、および `deny_commands` と矛盾するパターンは `--import` 時に自動拒否されます（終了コード `2`）。

### `git clone` 後のリンク修復とベースライン作成
Git はハードリンクを保持しないため、別環境で `git clone` した直後は `CLAUDE.md` / `GEMINI.md` が独立コピーになります。また `.reifu.state.json` は `.gitignore` 対象のため未存在です。

```sh
reifu link --check     # リンク切れを検知（Exit 3）
reifu link --force     # AGENTS.md へのリンクを再構築
reifu sync             # .reifu.state.json の初回ベースラインを作成
reifu status --quiet   # 異常なし（Exit 0）を確認
```

---

## 3. クロスプラットフォーム検証フック（`reifu hook verify`）

`go-cli` / `ts-web` / `ts-electron` / `rust-cli` プリセットでは、Claude Code の `Stop` フックおよび Gemini CLI の `AfterAgent` フックに `"reifu hook verify"` が設定されます。

* `.reifu.jsonc` の `workflow.verify_steps`（コマンド＋引数配列）をシェル非経由で直接実行するため、Windows（PowerShell / cmd / Git Bash）・WSL2・macOS・Linux すべてで同一動作します。
* すべてのコマンド出力を `stderr` へ流し、`stdout` を汚染しません。
* 以下のいずれかに該当する場合は Exit `0` で自動スキップします：
  * 標準入力 JSON に `"stop_hook_active": true` が含まれる（無限ループ防止）
  * プロジェクト直下に `BLOCKED_REPORT.md` が存在する
  * `workflow.require_files`（例: `go.mod` / `package.json`）がまだ存在しない
  * Node 系コマンドで `package.json` の `scripts` に該当スクリプトが未定義（または `npm init` のダミー）
* 検証に失敗した場合のみ Exit `2` でエージェントへ差し戻します。

---

## 4. カスタムプリセットの追加（C++ / Python 等）

Go コードの変更や再ビルドなしで、JSONC ファイルを 1 個置くだけで新しい言語・構成のプリセットを増やせます。

### 手順
1. ユーザー設定ディレクトリへ既存プリセットを書き出して場所を確認します：
   ```sh
   reifu preset export go-cli
   ```
   出力されたパス（Windows: `%APPDATA%\reifu\presets\`、macOS: `~/Library/Application Support/reifu/presets/`、Linux: `~/.config/reifu/presets/`）に `<name>.jsonc` を配置すると、`reifu preset list` に自動で現れます。

2. 例：`cpp-cmake.jsonc` を作成する場合：
   ```jsonc
   {
     "version": 1,
     "project": { "name": "", "preset": "cpp-cmake" },
     "agents": { "targets": ["claude", "codex", "gemini"], "create_missing": false },
     "links": { "mode": "auto", "targets": ["CLAUDE.md", "GEMINI.md"] },
     "permissions": {
       "allow_commands": ["cmake *", "ctest *"],
       "deny_commands": ["git push *", "git reset --hard*", "rm -rf *"],
       "deny_paths": [".env", ".env.*", "**/.env", "**/.env.*"]
     },
     "sandbox": { "mode": "workspace-write", "approval_policy": "never", "network_access": false },
     "hooks": { "verify_command": "reifu hook verify" },
     "workflow": {
       "stack": "C++ (CMake)",
       "linter": "clang-format / clang-tidy",
       "setup_note": "Step 1 または Phase 1 で `CMakeLists.txt` を作成します。",
       "require_files": ["CMakeLists.txt"],
       "commands": [
         { "step": "Configure", "run": "cmake -B build" },
         { "step": "Build",     "run": "cmake --build build" },
         { "step": "Test",      "run": "ctest --test-dir build --output-on-failure" }
       ],
       "verify_steps": [
         ["cmake", "--build", "build"],
         ["ctest", "--test-dir", "build", "--output-on-failure"]
       ],
       "gitignore": ["build/"]
     }
   }
   ```

3. そのまま利用します：
   ```sh
   reifu preset list
   reifu new my-cpp-app --preset cpp-cmake
   ```

---

## 5. OS 別セキュリティガードと終了コード

### 自動適用される保護
* **OS 別システムパス・設定ファイル保護 `deny`（Claude Code コンパイル時自動付与、Claude Code 仕様上 `Edit` ルールが `Edit`/`Write` 双方をカバー）**:
  * エージェント共有設定：`.claude/settings.json`, `.codex/config.toml`, `.gemini/settings.json`（Edit 拒否、Read は許可）
  * エージェント個人ローカル設定：`.claude/settings.local.json`, `.gemini/settings.local.json`（Read / Edit 拒否、`.gitignore` にも自動登録）
  * 認証ディレクトリ共通：`~/.ssh/**`, `~/.aws/**`, `~/.gnupg/**`（Read / Edit 拒否）
  * Windows：`C:/Windows/**`, `C:/Program Files/**`, `C:/Program Files (x86)/**`（Edit 拒否）
  * macOS：`/System/**`, `/Library/**`, `/etc/**`（Edit 拒否）
  * Linux / WSL2：`/etc/**`, `/usr/**`, `/bin/**`, `/sbin/**`（WSL2 は `/mnt/c/Windows/**`, `/mnt/c/Program Files/**` も追加）
* **OS 別サンドボックス**:
  * Codex CLI：全 OS で `sandbox_mode` + `approval_policy` + `[sandbox_workspace_write].network_access` を出力
  * Claude Code：macOS / Linux / WSL2 で `sandbox.enabled: true` を自動有効化（`danger-full-access` 除く）
  * Gemini CLI：`.reifu.jsonc` で `"sandbox": { "gemini_sandbox": true }` を明示した非 Windows 環境でのみ `tools.sandbox: true` を出力
* **パス境界検証**: パストラバーサル（`..`・絶対パス）およびプロジェクト外を指すシンボリックリンク追従を全ファイル操作で遮断します。

### 終了コード

| コード | 意味 |
| :---- | :---- |
| `0` | 正常終了 |
| `1` | 引数エラー、構文・バリデーション違反、I/O エラー、ホームディレクトリ保護 |
| `2` | 設定ドリフト／衝突検知、`--import` 危険コマンド拒否、`status --quiet` 異常、`hook verify` 失敗、`run` の安全停止（異常終了・タイムアウト・中断・`BLOCKED_REPORT.md`） |
| `3` | `link --check` によるリンク整合性エラー |

---

## 6. フェーズの自動実行とログ閲覧（`reifu run` / `reifu logs`）

| コマンド | 用途 |
| --- | --- |
| `reifu run [dir]` | フェーズの実装・レビューとローカルコミット |
| `reifu logs [dir]` | 実行進捗、ログ、レビューレポートの閲覧 |

プロジェクトルートの `prompts/phases/` に `01_foundation.md` のような実装プロンプトを置き、指示を `prompts/review.md` に用意します。自動実行では原則 reifu がコミットするため、フェーズプロンプトにコミット指示を書かないでください。エージェント自身がコミットした場合も、ステップ開始時のコミットを基準に差分と SHA を記録します。

実装・レビュー担当は `claude`、`codex`、`gemini`、`agy`（別名 `antigravity`）から指定できます。起動する CLI を事前にインストールしてください。**1 種類のエージェントだけでも利用できます**（`--reviewer` を省略すると `--implementer` と同じエージェントが毎回新規セッションでレビューを実行します。レビュー自体を省く場合は `--no-review` を付けます）。

```sh
reifu run --implementer claude                        # 1 ツールのみ（同一ツールの別セッションでレビュー）
reifu run --implementer codex --reviewer claude --dry-run
reifu run --implementer codex --reviewer claude       # 異種ツールでのクロスレビュー
reifu run --implementer agy --reviewer gemini
```

担当を固定する場合は、既存の `.reifu.jsonc` のトップレベルに次の `runner` を追加します。CLI の担当指定で上書きできます。任意の `agents` は起動コマンドと引数の配列で、実行前にコマンドが標準エラーに表示されます。信頼できるプロジェクトの設定とプロンプトで実行してください。

```jsonc
"runner": {
  "implementer": "codex",
  "reviewer": "claude",
  "agents": {
    "claude": ["claude", "-p", "--model", "sonnet"]
  }
}
```

`--phase 01_foundation` で 1 件、`--from 01_foundation` でそのフェーズ以降を選べます。実装・レビュー各ステップの制限時間は既定で 30 分です。`--timeout 0` で無制限になります。`--no-review` でレビューを、`--no-commit` で自動コミットを省略できます。

異常終了、タイムアウト、Ctrl+C、`BLOCKED_REPORT.md` 検出時は実行を停止します。状態とログを確認し、停止原因を解消して `reifu run --resume` で中断したステップから続けます。`BLOCKED_REPORT.md` がある場合は内容を確認し、解消後に取り除きます。再開時は保存済みのレビュー・コミット設定が使われます。

自動コミットを行う場合は、既定で未コミット変更のある Git 作業ツリーを拒否します。変更をコミットまたは退避するか、差分を確認して取り込む場合は `--allow-dirty`（再開時は `--resume --allow-dirty`）を指定します。両コマンドはホームディレクトリ直下を拒否します。ラン状態とログは `.reifu.run.json` と `.reifu/runs/` に保管され、reifu の自動コミット対象から除外されます。

```sh
reifu logs                            # 最新ランの進捗とコミット
reifu logs --list                     # 過去ラン一覧
reifu logs --review                   # 最新ランの全レビューレポート
reifu logs --run RUN_ID --review      # 指定ランのレビュー履歴
reifu logs --phase 01_foundation      # 指定フェーズの実装ログ
reifu logs -f                         # 実装ログを追従。Ctrl+C で終了
```

`reifu logs` は読み取り専用です。`--json` で状態や履歴一覧を取得できます。追従中はテキストが逐次出力されるため、`-f` と `--json` は併用できません。`--json` は常にラン全体の状態を出力するため、`--phase` / `--review` とも併用できません。`-f` が追従するのは実装ログで、レビュー内容はレビュー完了後に `--review` で確認します。

---

## 7. 将来拡張 ToDo

- `reifu preset import <file>`: 他者が作成・共有したプリセット JSONC を安全検証してユーザー設定ディレクトリへ取り込む機能
