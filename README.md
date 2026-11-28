![霊符（reifu）](docs/assets/reifu.jpg)

# 霊符（reifu）

[![CI](https://github.com/withnic/reifu/actions/workflows/ci.yml/badge.svg)](https://github.com/withnic/reifu/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/withnic/reifu)](https://github.com/withnic/reifu/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

> 複数 AI コーディングエージェント（Claude Code / Codex CLI / Gemini CLI / Antigravity）向けの、ローカル完結型ボイラープレート＆プロジェクト設定統合 CLI ツール

- **Single Source of Truth（SSOT）**: 行動規範は `AGENTS.md`（`CLAUDE.md` / `GEMINI.md` へ自動リンク）、権限・サンドボックス・フック設定は `.reifu.jsonc` の 1 ファイルで一元管理。
- **マルチエージェント Skills**: `.agents/skills/` を正本として `.claude/skills/` へスキル単位でリンク。 `reifu link` / `reifu sync` で同期し、`reifu status` で確認。
- **薄いグローバル・自己完結型ローカル**: `~/.claude/` 等のグローバル設定を一切書き換えず（ホームディレクトリ直下での実行も無条件で拒否）、プロジェクトローカルで完結。
- **非破壊 3-way マージ＆ドリフト検知**: 外部ツールで追記された許可ルールを検知し、危険コマンドガード付きで `reifu sync --import` による逆取り込み・横展開が可能。
- **クロスプラットフォーム検証フック（`reifu hook verify`）**: Windows（PowerShell / cmd / Git Bash）・WSL2・macOS・Linux で同一動作し、`stop_hook_active` 無限ループ防止と未初期化スキップを内蔵。
- **マルチエージェント自動フェーズ実行（`reifu run` / `reifu logs`）**: `claude` / `codex` / `gemini` / `agy`（Antigravity）から実装担当とレビュー担当を指定し、全フェーズの「実装 → コミット → レビュー＆修正 → コミット」を一気に自動完走（中断再開・レビュー履歴の読み取り専用閲覧対応）。
- **ゼロ外部依存・シングルバイナリ**: Go 1.23+ 標準ライブラリのみ（`CGO_ENABLED=0` ビルド対応）。
- **JSONC 1 ファイルでプリセット拡張**: Rust / Electron / C++ 等の新しいスタックも Go コード変更なしで追加可能。

---

## インストール

### ビルド済みバイナリ（Go 不要）

[GitHub Releases](https://github.com/withnic/reifu/releases) からお使いの OS / アーキテクチャ（Windows / macOS / Linux）向けアーカイブをダウンロードし、展開した `reifu` を PATH の通ったディレクトリへ配置してください。

### `go install`（Go 1.23+）

```sh
go install github.com/withnic/reifu/cmd/reifu@latest
```

リポジトリをクローンしてローカルからビルド・インストールする場合：

```sh
git clone https://github.com/withnic/reifu.git
cd reifu
go install ./cmd/reifu
```

インストール確認：

```sh
reifu version
reifu help
```

---

## クイックスタート

プロジェクト作成から設計・全フェーズの自動実装＆レビュー完了まで、次の流れで完結できます。

```sh
# 1. 新規プロジェクトを作成して Git 初期化（内蔵プリセット: minimal / go-cli / ts-web / ts-electron / rust-cli）
reifu new my-app --preset go-cli
cd my-app && git init

# 2. 生成された prompts/phase0.md を AI エージェントへ渡して設計書と prompts/phases/01_*.md〜 を作成
#    設計・フェーズが揃ったらコミット
git add -A && git commit -m "docs: add design and phases"

# 3. 実装担当とレビュー担当を指定し、全フェーズを自動完走（実装 → コミット → レビュー＆修正 → コミット）
#    対応エージェント: claude / codex / gemini / agy（antigravity）
#    ※ --reviewer は省略可（省略時は --implementer と同一エージェントの新規セッションでレビュー、--no-review で省略可）
reifu run --implementer claude                        # 1 ツールだけで実装＋別セッションレビュー
reifu run --implementer codex --reviewer claude       # 異種ツールでのクロスレビューも可能

# 4. 進捗やレビュー結果を読み取り専用コマンドで確認（別ターミナルからの追従も可）
reifu logs             # 各フェーズの進捗とコミット一覧
reifu logs --review    # レビュアーの指摘・修正内容を確認
reifu logs -f          # 実行中の実装ログを追従表示

# 5. 途中停止した場合は原因解消後に中断ステップから再開
reifu run --resume --allow-dirty

# 6. .reifu.jsonc を編集したら各エージェント設定へ反映（以降の機能追加は prompts/add_feature.md から同じ流れ）
reifu sync
```

既存リポジトリへの導入（`reifu init`）、仕様書取り込み（`--spec`）、ドリフト逆取り込み（`reifu sync --import`）、`git clone` 後のリンク修復（`reifu link --force`）、`reifu run` / `reifu logs` の詳細、カスタムプリセット作成は **[HOWTO.md](HOWTO.md)** を参照してください。

---

## コマンド一覧

| コマンド | 概要 |
| :---- | :---- |
| `reifu new <dir>` | 新規ディレクトリを作成し、雛形・リンク・各エージェント設定を生成 |
| `reifu init [dir]` | 既存の空フォルダ／Git リポジトリに `reifu` を導入 |
| `reifu sync [dir]` | `.reifu.jsonc` を各エージェント設定へ非破壊コンパイル（`--import`, `--dry-run`, `--no-hooks` 対応） |
| `reifu status [dir]` | 環境・リンク・同期状態を診断（`--json`, `--quiet` 対応） |
| `reifu run [dir]` | フェーズを順番に実装・レビューし、ローカルにコミット（`--resume` 対応） |
| `reifu logs [dir]` | 実行履歴・実装ログ・レビューレポートを閲覧（`-f` で追従） |
| `reifu link [dir]` | `AGENTS.md` リンクの検証（`--check`）および再構築（`--force`） |
| `reifu preset list / export` | 内蔵・カスタムプリセットの一覧表示および書き出し |
| `reifu hook verify [dir]` | `.reifu.jsonc` の `workflow.verify_steps` をシェル非経由で検証実行 |
| `reifu version` | バージョン・コミットハッシュ・ビルド情報を表示（`--json`、`-v` / `--version` 対応） |
| `reifu help [cmd]` | 全体またはサブコマンド別のヘルプを表示 |

---

## ドキュメント

- **[HOWTO.md](HOWTO.md)** — 実践ワークフロー・フラグ一覧・カスタムプリセット作成・OS 別セキュリティガード＆終了コード
- **[docs/design/01_project_overview.md](docs/design/01_project_overview.md)** — プロジェクト概要・MVP スコープ・セキュリティ原則
- **[docs/design/02_data_modeling.md](docs/design/02_data_modeling.md)** — `.reifu.jsonc` / `.reifu.state.json` スキーマ・マージ＆ドリフト仕様
- **[docs/design/03_architecture.md](docs/design/03_architecture.md)** — アーキテクチャ・OS/WSL2 吸収・テスト戦略

---

## 開発・テスト

```sh
gofmt -s -w . && go vet ./...
go test -v -race ./...
CGO_ENABLED=0 go build ./cmd/reifu
```

---

## ライセンス

[MIT License](LICENSE)
