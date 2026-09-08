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

管理画面で RSS / Atom、Reuters、Fisco の取得対象を登録できます。
Fisco はコネクタに「Fisco」を選び、設定名、取得間隔、最大取得件数（1 以上 50 以下）、有効状態を指定します。
カテゴリや取得内容の選択、取得先 URL の入力は不要です。最大取得件数を固定一覧 API の `limit` に反映し、記事ごとの詳細 API から本文を取得します。

記事は取得対象、外部記事 ID、更新日時の組み合わせで保存します。更新日時がない場合は公開日時を使い、両方ない場合も同じ記事を 1 行にまとめます。
同じ日時は最新の取得内容で上書きし、日時が異なる場合だけ別行に保存します。上書き時は記事 ID と、その行を初めて保存した取得日時を維持します。
既存 DB へのマイグレーションでは、同じ記事の同日時の重複を取得日時、記事 ID の降順に整理し、先頭の 1 行を残します。

## 設定サンプル

- `conf/conf.toml.sample`: HTTP 待受ポート、記事検索の既定件数、PostgreSQL 接続設定、記事の保持設定の記載例

7 日保持は `conf/retention.toml` またはコピーした `conf/conf.toml` で切り替えます。

```toml
[retention]
enabled = false
```

既定値は `false` で、設定を省略した場合も無効です。`true` にすると、7 日（168 時間）以上前の記事の自動削除と、期限を過ぎた記事の新規保存の省略を両方有効にします。
更新日時、公開日時、初回取得日時の順に存在する日時で判定し、起動時と 1 時間ごとに記事を削除します。取得対象の設定と取得履歴は削除しません。
設定変更後は再起動してください。同日時の記事の上書きは、この設定にかかわらず有効です。

`build.ps1` は実際の `system.toml`、`mcp.toml`、`postgres.toml` を配布物へコピーしません。
作成した `conf/conf.toml` にはパスワードが含まれるため、配布先のアクセス権を確認し、インターネットへ直接公開しないでください。

管理画面の HTML、CSS、JavaScript、Bulma、Axios、Grid.js、PostgreSQL マイグレーションは実行ファイルへ埋め込まれているため、別途配置する必要はありません。

このフォルダは `build.ps1` の実行ごとに作り直されます。
配布用サンプルを変更する場合は、ソース側の `conf/conf.toml.sample` を変更してから再ビルドしてください。
