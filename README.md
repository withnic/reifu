![霊符（reifu）](docs/assets/reifu.jpg)

# 霊符（reifu）

[![CI](https://github.com/withnic/reifu/actions/workflows/ci.yml/badge.svg)](https://github.com/withnic/reifu/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/withnic/reifu)](https://github.com/withnic/reifu/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

> 複数 AI コーディングエージェント（Claude Code / Codex CLI / Gemini CLI / Antigravity）向けの、ローカル完結型ボイラープレート＆プロジェクト設定統合 CLI ツール

- **Single Source of Truth（SSOT）**: 行動規範は `AGENTS.md`（`CLAUDE.md` / `GEMINI.md` へ自動リンク）、権限・サンドボックス・フック設定は `.reifu.jsonc` の 1 ファイルで一元管理。
- **薄いグローバル・自己完結型ローカル**: `~/.claude/` 等のグローバル設定を一切書き換えず（ホームディレクトリ直下での実行も無条件で拒否）、プロジェクトローカルで完結。
- **非破壊 3-way マージ＆ドリフト検知**: 外部ツールで追記された許可ルールを検知し、危険コマンドガード付きで `reifu sync --import` による逆取り込み・横展開が可能。
- **クロスプラットフォーム検証フック（`reifu hook verify`）**: Windows（PowerShell / cmd / Git Bash）・WSL2・macOS・Linux で同一動作し、`stop_hook_active` 無限ループ防止と未初期化スキップを内蔵。
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

```sh
# 1. 新規プロジェクトを作成（内蔵プリセット: minimal / go-cli / ts-web / ts-electron / rust-cli）
reifu new my-app --preset go-cli
cd my-app && git init

# 2. 生成された prompts/phase0.md を AI エージェントへ渡して設計・実装フェーズを生成
#    （以降の機能追加は prompts/add_feature.md、各フェーズ後は prompts/review.md を利用）

# 3. .reifu.jsonc を編集したら各エージェント設定へ反映
reifu sync
```

既存リポジトリへの導入（`reifu init`）、仕様書取り込み（`--spec`）、ドリフト逆取り込み（`reifu sync --import`）、`git clone` 後のリンク修復（`reifu link --force`）、カスタムプリセット作成は **[HOWTO.md](HOWTO.md)** を参照してください。

---

## コマンド一覧

| コマンド | 概要 |
| :---- | :---- |
| `reifu new <dir>` | 新規ディレクトリを作成し、雛形・リンク・各エージェント設定を生成 |
| `reifu init [dir]` | 既存の空フォルダ／Git リポジトリに `reifu` を導入 |
| `reifu sync [dir]` | `.reifu.jsonc` を各エージェント設定へ非破壊コンパイル（`--import`, `--dry-run`, `--no-hooks` 対応） |
| `reifu status [dir]` | 環境・リンク・同期状態を診断（`--json`, `--quiet` 対応） |
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
