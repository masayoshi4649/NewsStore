# 実装状況

この文書は実装中の設計キャッシュ兼チェックリストとして更新する。

## 受け入れ条件

- [x] `conf/system.toml` と `conf/postgres.toml` を名前順に読み、未知項目をエラーにする
- [x] PostgreSQL の初期スキーマを自動適用できる
- [x] 管理画面から RSS/Reuters 取得対象を登録、変更、無効化できる
- [x] 管理画面から取得をキューへ投入できる
- [x] 定期実行で期限到来した取得対象を処理できる
- [x] RSS/Atom の見出しと本文を保存できる
- [x] Reuters の一覧・詳細 API から見出しと本文を保存できる
- [x] 管理画面はダークテーマを既定とし、RSS / Reuters 固有欄を種類に応じて排他表示する
- [x] Reuters の取得内容をホワイトリストから選択し、URL と内部 API 設定を固定できる
- [x] 同じ記事リビジョンを複数回取得しても 1 行だけ保存される
- [x] 記事と取得履歴を管理画面で確認できる
- [x] 単体テスト、`go vet`、ビルドが成功する

## 設計判断

1. 取得対象は TOML ではなく PostgreSQL を正本にする。
2. 記事は上書きせず、リビジョン単位で履歴を残す。
3. 参照 Reuters 実装のコードはライセンス上コピーせず、通信仕様だけを確認して独立実装する。
4. RSS のリンク先抽出は取得対象ごとに有効化し、失敗時はフィード本文へフォールバックする。
5. 実 DB と外部サイトを必要としないテストを通常テストとする。
6. 取得対象単位の PostgreSQL advisory lock で、Web、定期処理、CLI、複数プロセス間の重複取得を抑止する。
7. マイグレーションは advisory lock と SHA-256 検証を行い、適用済みファイルを不変とする。
8. エントリーポイントはプロジェクトルートの `main.go` とし、`go run .` と `go build .` を標準の実行・ビルド方法とする。
9. 管理画面は閉域ネットワークで運用し、アプリケーション内の利用者認証は設けない。CSRF、HTML 自動エスケープ、ネットワーク境界のアクセス制御は維持する。
10. 管理画面は `web/lib` のローカル Bulma をバイナリへ埋め込み、外部 CDN に依存しない。
11. HTTP 待受設定は `conf/system.toml` の `[SYSTEM] Port` だけとし、`:Port` 形式で全インターフェースに待ち受ける。PostgreSQL 設定は `conf/postgres.toml` に分離し、全体のサンプルは `conf/conf.toml.sample` に置く。
12. 管理画面はダークテーマを既定とし、RSS / Reuters の固有欄は選択中の種類だけを表示する。非表示側の入力は無効化し、サーバー側でも不要な値を保存しない。
13. Reuters は `https://jp.reuters.com`、`website=reuters-japan`、`offset=0`、一覧 API、詳細 API をコードで固定する。利用者は取得内容だけをホワイトリストから選択する。
14. Reuters の新規保存では `news_sources.endpoint_url` に固定 URL、`settings` に `section_id` だけを保持する。
15. Reuters の全取得対象は一覧 API への実通信で検証する。日本は現行の `/world/japan/` を使用し、旧版で保存された `/ja/` は読み込み時に現行値へ変換する。
16. テーブル名は 2 語の `snake_case` に統一し、`news_sources`、`news_articles`、`ingestion_runs`、`schema_migrations` とする。
17. テーブル命名変更時点で開発 DB の全テーブルは削除済みのため、初期マイグレーションを直接更新し、旧テーブルからのリネーム処理は持たない。
17. 旧開発DBの全テーブルが削除済みであることを前提に、初期マイグレーションから上記4テーブルを新規作成する。

## 検証記録

- `gofmt -l main.go main_test.go internal web`
- `go test ./...`
- `go vet ./...`
- `go mod tidy -diff`
- `go build .`

通常検証は実 PostgreSQL と外部サイトへ接続しない。
PowerShell では次のコマンドで、Reuters の全取得対象と記事詳細を任意に実通信確認できる。

```powershell
$env:NEWSSTORE_TEST_REUTERS_LIVE = "1"
go test -count=1 -run LiveReuters -v ./internal/connector/reuters
```

PostgreSQL 初期スキーマ適用後は、次のコマンドで新4テーブルの存在と旧1語テーブルの不存在を実DB確認できる。

```powershell
$env:NEWSSTORE_TEST_POSTGRES_LIVE = "1"
$env:NEWSSTORE_TEST_POSTGRES_CONF = (Resolve-Path conf).Path
go test -count=1 -run TestLiveSchemaUsesTwoWordTableNames -v ./internal/store/postgres
```

実環境への導入時は、対象 PostgreSQL でマイグレーション、`pg_trgm` 作成権限、advisory lock、および RSS/Reuters の実通信を確認する。
