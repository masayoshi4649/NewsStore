# NewsStore 配布版

このフォルダには、NewsStore の実行ファイルと設定サンプルが含まれます。
実際の接続情報は配布物へ含めないため、初回起動前に設定ファイルを作成してください。

## Windows

PowerShell でこのフォルダへ移動し、設定サンプルをコピーして編集してから実行します。

```powershell
Copy-Item .\conf\conf.toml.sample .\conf\conf.toml
# .\conf\conf.toml の PostgreSQL 接続情報を編集
.\newsstore.exe
```

## Linux x64

このフォルダへ移動し、設定サンプルをコピーして編集します。
必要に応じて実行権限を付けてから実行します。

```bash
cp ./conf/conf.toml.sample ./conf/conf.toml
# ./conf/conf.toml の PostgreSQL 接続情報を編集
chmod +x newsstore
./newsstore
```

引数を省略すると管理画面、記事 JSON API、MCP、記事取得スケジューラーを起動します。
マイグレーションだけを実行する場合は `migrate`、取得対象を指定して即時取得する場合は `fetch -source-id ID` を使用します。

## 設定サンプル

- `conf/conf.toml.sample`: HTTP 待受ポート、記事検索の既定件数、PostgreSQL 接続設定の記載例

`build.ps1` は実際の `system.toml`、`mcp.toml`、`postgres.toml` を配布物へコピーしません。
作成した `conf/conf.toml` にはパスワードが含まれるため、配布先のアクセス権を確認し、インターネットへ直接公開しないでください。

管理画面の HTML、CSS、JavaScript、Bulma、Axios、Grid.js、PostgreSQL マイグレーションは実行ファイルへ埋め込まれているため、別途配置する必要はありません。

このフォルダは `build.ps1` の実行ごとに作り直されます。
配布用サンプルを変更する場合は、ソース側の `conf/conf.toml.sample` を変更してから再ビルドしてください。
