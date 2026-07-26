# 実装状況

この文書は実装中の設計キャッシュ兼チェックリストとして更新する。

## 受け入れ条件

- [x] `conf/system.toml`、`conf/mcp.toml`、`conf/postgres.toml` を名前順に読み、未知項目をエラーにする
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
- [x] 記事一覧と記事詳細が記事データを SSR せず、ローカル Axios から同一オリジン JSON API を呼び出して表示できる
- [x] 記事一覧がヘッドラインだけを表示し、API 応答内を Grid.js で 1 ページ 10 件にローカルページングできる
- [x] 記事検索パネルを既定で閉じ、入力順不問の横並び日時範囲、5 個の個別検索語欄、狭い最大件数欄を必要時だけ表示できる
- [x] `GET /api/sources` から未削除の取得対象について ID、設定名、種類、有効状態を取得できる
- [x] `GET /api/articles` が MCP と同じ複数条件 AND 検索、実効日時順、keyset cursor、本文 500 文字抜粋を提供できる
- [x] `GET /api/articles/:id` が MCP と同じ Repository で本文を分割取得し、「続きを読む」で続きだけを表示できる
- [x] 記事 API が共通 8 枠で同時実行を制限し、長文本文を全体再設定せず追記できる
- [x] `POST /mcp` で `list_sources`、`search_articles`、`get_article` だけを公開できる
- [x] MCP から未削除の取得対象について `news_sources.id`、設定名、種類、有効状態を一覧取得できる
- [x] MCP の記事一覧で取得対象、入力順不問の日時範囲、最大 5 個の検索語を AND 条件として任意に組み合わせられる
- [x] MCP の無条件検索が `conf/mcp.toml` の既定件数を新しい順に返す
- [x] MCP の検索が日時と記事 ID のカーソル方式で動作し、本文詳細を DB から分割取得できる
- [x] MCP がステートレス JSON で動作し、Origin、メソッド、本文サイズ、Content-Type を検証できる
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
10. 管理画面は `web/lib` のローカル Bulma、Axios、Grid.js をバイナリへ埋め込み、外部 CDN に依存しない。
11. HTTP 待受設定は `conf/system.toml` の `[SYSTEM] Port` だけとし、`:Port` 形式で全インターフェースに待ち受ける。MCP 設定は `conf/mcp.toml`、PostgreSQL 設定は `conf/postgres.toml` に分離し、全体のサンプルは `conf/conf.toml.sample` に置く。
12. 管理画面はダークテーマを既定とし、RSS / Reuters の固有欄は選択中の種類だけを表示する。非表示側の入力は無効化し、サーバー側でも不要な値を保存しない。
13. Reuters は `https://jp.reuters.com`、`website=reuters-japan`、`offset=0`、一覧 API、詳細 API をコードで固定する。利用者は取得内容だけをホワイトリストから選択する。
14. Reuters の新規保存では `news_sources.endpoint_url` に固定 URL、`settings` に `section_id` だけを保持する。
15. Reuters の全取得対象は一覧 API への実通信で検証する。日本は現行の `/world/japan/` を使用し、旧版で保存された `/ja/` は読み込み時に現行値へ変換する。
16. テーブル名は 2 語の `snake_case` に統一し、`news_sources`、`news_articles`、`ingestion_runs`、`schema_migrations` とする。
17. テーブル命名変更時点で開発 DB の全テーブルは削除済みのため、初期マイグレーションを直接更新し、旧テーブルからのリネーム処理は持たない。
18. 旧開発 DB の全テーブルが削除済みであることを前提に、初期マイグレーションから上記 4 テーブルを新規作成する。
19. MCP は既存 HTTP サーバーの `/mcp` へ同居させ、アプリケーション内の認証を追加せず、閉域ネットワーク境界で接続元を制御する。
20. MCP は `list_sources`、`search_articles`、`get_article` の読み取り専用 tool だけを公開し、resources、prompts、MCP のログ配信機能を登録しない。
21. MCP はステートレス JSON 応答とし、セッションと SSE の保持処理を持たない。
22. MCP の無条件検索件数は `conf/mcp.toml` の `[MCP] DefaultArticleLimit` で定義し、既定 20、1 以上 100 以下とする。`conf/system.toml` は `[SYSTEM] Port` だけを保持する。
23. 記事検索は取得対象、両端を含む RFC3339 日時範囲、正規化後に最大 5 個の検索語を任意に組み合わせ、すべて AND 条件にする。日時を両方指定した場合は入力順を問わず早い日時を下限、遅い日時を上限へ正規化し、片側指定では `date_from` を下限、`date_to` を上限として扱う。各語は見出しまたは本文へ部分一致させ、`%`、`_`、`\` は通常文字として扱う。
24. 記事検索は `COALESCE(published_at, fetched_at)` の降順、同日時では記事 ID の降順とし、両値を含む不透明な keyset cursor を使う。`COUNT` と `OFFSET` は実行せず、本文は先頭 500 文字だけを SQL で読み出す。
25. 未削除の `news_sources` は有効状態にかかわらず `list_sources` へ含め、ID、設定名、種類、有効状態を返す。
26. 検索順に対応する式インデックスは既存マイグレーションを書き換えず、`003_mcp_article_order.sql` で追加する。
27. 記事詳細は本文を既定 8,000 文字、最大 16,000 文字ずつ SQL の `SUBSTRING` で取得し、本文全体をアプリケーションへ読み込まない。
28. MCP は `Origin` ヘッダーが存在する要求を拒否し、`application/json`、`POST`、1 MiB の本文上限を検証する。
29. MCP の出力には raw payload、PostgreSQL 接続情報、SQL、内部エラー詳細を含めない。
30. MCP の DB 問い合わせ期限は 60 秒、HTTP 応答期限は 75 秒とし、リモート DB の遅延を許容しながら無期限の占有を防ぐ。
31. 記事一覧 `/admin/articles` と記事詳細 `/admin/articles/:id` は記事データを SSR せず、ローカルの `/lib/axios/axios.min.js` と `/static/articles.js` から同一オリジン JSON API を呼び出して描画する。
32. 記事参照 API は `GET /api/sources`、`GET /api/articles`、`GET /api/articles/:id` に限定し、書き込み API、認証、CORS を追加しない。閉域ネットワーク境界で接続元を制限する。
33. 通常画面と MCP は取得対象一覧、複数条件 AND 検索、`COALESCE(published_at, fetched_at)` の日時順、keyset cursor、本文分割の PostgreSQL Repository を共有する。
34. 記事 API の無条件検索件数は `conf/mcp.toml` の `[MCP] DefaultArticleLimit` を共用し、`conf/system.toml` は `[SYSTEM] Port` だけを保持する。
35. 記事 API は一覧本文を先頭 500 文字、詳細本文を既定 8,000 文字、最大 16,000 文字ずつ SQL で読み出す。本文位置は 0 以上 10,000,000 以下とし、長文は「続きを読む」で追加取得する。
36. 記事 API はキャッシュを禁止し、DB 問い合わせ期限を 60 秒とする。内部エラー詳細、SQL、接続情報、raw payload は JSON 応答へ含めない。
37. API のヘッドラインは Grid.js の仮想 DOM へテキスト子として渡し、その他の文字列は `textContent` で描画する。元記事 URL は `http` または `https` の場合だけリンクとして表示する。
38. 3 つの記事 API は共通の最大 8 実行枠を使い、満杯時は待機せず `Retry-After: 1` 付きの HTTP 503 を返して取得処理用の DB 接続を圧迫しない。
39. 分割取得した追加本文は既存本文全体を再設定せず、テキストノードとして末尾へ追加して長文表示時の二次的なコピーを避ける。
40. 通常画面の記事一覧は API 応答の最大件数内を Grid.js で 1 ページ 10 件にローカルページングし、ヘッドラインだけを表示する。API と MCP の keyset cursor は維持するが、通常画面に追加取得ボタン、件数、続きの有無を表示しない。
41. 記事検索パネルは既定で閉じ、日時範囲を `～` で横並びにし、どちらへ早い日時を入力しても同じ範囲へ正規化する。5 個の個別検索語欄は縦並びにし、最大件数欄の幅を抑える。

## 検証記録

- `gofmt -l main.go main_test.go internal web`
- `go test ./...`
- `go vet ./...`
- `go mod tidy -diff`
- `go build .`
- 公式 MCP クライアントによる Streamable HTTP の初期化、tool 一覧、取得対象一覧、複数条件の記事検索、本文分割
- 実 PostgreSQL に対する `list_sources`、`search_articles`、`get_article`
- 記事 JSON API の取得対象一覧、無条件・全条件検索、日時順カーソル、本文分割、入力制約、内部エラー秘匿
- Axios 記事画面の空シェル、既定で閉じた検索パネル、Grid.js の 10 件ローカルページング、ヘッドラインのみの一覧、空状態、エラー状態、記事詳細の「続きを読む」、安全なテキスト・URL 描画

通常検証は実 PostgreSQL と外部サイトへ接続しない。
MCP 実装後は、公式 MCP クライアントを使うテストで tool 一覧、検索カーソル、本文分割、入力制約、HTTP 境界を確認する。
実 PostgreSQL を使う MCP の任意検証は次のコマンドで実行できる。

```powershell
$env:NEWSSTORE_TEST_MCP_LIVE = "1"
$env:NEWSSTORE_TEST_MCP_CONF = (Resolve-Path conf).Path
go test -count=1 -run TestLiveMCPReadsPostgreSQLArticles -v ./internal/mcpserver
```

実 PostgreSQL を使う記事 JSON API の任意検証は次のコマンドで実行できる。

```powershell
$env:NEWSSTORE_TEST_WEB_API_LIVE = "1"
$env:NEWSSTORE_TEST_WEB_API_CONF = (Resolve-Path conf).Path
go test -count=1 -run TestLiveArticleAPIReadsPostgreSQL -v ./internal/web
```

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
