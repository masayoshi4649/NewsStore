# NewsStore アーキテクチャ

## 目的

NewsStore は、RSS/Atom フィードと個別の REST API からニュース記事を取得し、見出しと本文を PostgreSQL に保存する。
取得対象、取得間隔、コネクタ固有設定は管理画面から変更し、PostgreSQL を正本とする。
`conf/*.toml` は、DB 接続、HTTP サーバーの待受ポート、MCP の既定記事件数といったアプリ起動設定だけを扱う。
実行設定は `conf/system.toml`、MCP 設定は `conf/mcp.toml`、PostgreSQL 接続設定は `conf/postgres.toml`、記事の保持設定は `conf/retention.toml` に分離する。

## 実行構成

初期版は、次の責務を 1 つの Go バイナリにまとめる。

1. Gin による管理画面、同一オリジン JSON API、ヘルスチェック
2. 保存済み記事を参照する読み取り専用 MCP
3. DB に保存された取得対象を定期確認するスケジューラー
4. RSS/Atom コネクタ
5. Reuters コネクタ
6. PostgreSQL への冪等保存

外部取得、DB 保存、Web 表示はパッケージ境界で分離し、将来 Web とワーカーを別プロセスへ分けられるようにする。

## パッケージ構成

```text
main.go                         エントリーポイントと依存関係の組み立て
internal/config/                conf/*.toml の読込、既定値、検証
internal/domain/                取得対象、記事、取得結果
internal/connector/             コネクタ共通インターフェースと選択
internal/connector/rss/         RSS/Atom の取得と本文抽出
internal/connector/reuters/     Reuters の一覧・詳細 API
internal/ingest/                取得実行、検証、冪等保存、実行結果集計
internal/scheduler/             定期取得と手動取得キュー
internal/mcpserver/             読み取り専用 MCP と記事参照用 DTO
internal/store/postgres/        PostgreSQL 接続、マイグレーション、Repository
internal/web/                   Gin ルーター、管理画面、記事 JSON API、CSRF
internal/web/templates/         埋め込み HTML テンプレート
internal/web/static/            埋め込み CSS と画面 JavaScript
web/assets.go                   ローカル Web ライブラリの埋め込みと配信
web/lib/bulma/                  管理画面用のローカル Bulma
web/lib/axios/                  記事 JSON API 呼び出し用のローカル Axios
web/lib/gridjs/                 記事一覧のローカルページング用 Grid.js
docs/                           設計キャッシュと運用情報
```

実行ファイルのパッケージはプロジェクトルートに置き、`go run .` で既定の `serve` を起動する。
管理画面の Bulma、Axios、Grid.js は `web/lib` からバイナリへ埋め込むため、実行時に CDN へ接続しない。
管理画面はダークテーマを既定とする。

配布ビルドは `dist/Windows` と `dist/linux` に分け、各フォルダへ対象 OS の実行ファイル、`conf/conf.toml.sample`、配布用 README、LICENSE を配置する。
認証情報を含み得る実設定 `system.toml`、`mcp.toml`、`postgres.toml` は配布物へコピーせず、利用者がサンプルから実設定を作成する。
HTML、CSS、JavaScript、ローカル Web ライブラリ、PostgreSQL マイグレーションは Go バイナリへ埋め込み、配布フォルダへソースツリーを複製しない。
`dist` は両 OS のビルドと必要ファイルの準備に成功した後で全体を入れ替え、途中失敗時は直前の配布物を維持する。

## 起動設定

`conf` ディレクトリ内の TOML はファイル名昇順で読み込む。
標準構成では責務を次のように分け、DB パスワードをアプリケーションの一般設定から分離する。

- `conf/system.toml`: `[SYSTEM]` セクションの `Port` による HTTP 待受ポート設定
- `conf/mcp.toml`: `[MCP]` セクションの無条件検索時の既定記事件数
- `conf/postgres.toml`: PostgreSQL の接続、コネクションプール、マイグレーション設定
- `conf/retention.toml`: `[retention] enabled` による 7 日保持の有効・無効

`conf/system.toml` の正規形は次の 1 設定だけとする。

```toml
[SYSTEM]
Port = 8080
```

設定全体のサンプルは `conf/conf.toml.sample` に保存する。
HTTP サーバーは `Port` から `:Port` 形式のアドレスを組み立て、全インターフェースで待ち受ける。
`conf/mcp.toml` の `[MCP] DefaultArticleLimit` は既定 20、1 以上 100 以下とする。
`[retention] enabled` は既定 `false` とし、未指定の場合も無効とする。起動時に読み込み、設定変更は再起動で反映する。
`true` の場合だけ記事の自動削除と期限切れ記事の保存省略を有効にし、同日時上書きは設定にかかわらず有効とする。

## Web ルーティング

トップ URL `/`、管理画面ルート `/admin`、共通ロゴは記事一覧 `/admin/articles` へ遷移する。
利用者は `http://localhost:8080/` から管理画面へアクセスできる。
ヘルスチェックは `/healthz`、ローカル Web ライブラリは `/lib` 配下で配信する。

記事一覧 `/admin/articles` と記事詳細 `/admin/articles/:id` は記事データを SSR せず、ナビゲーションと空の表示領域を持つ HTML シェルを返す。
ローカルの `/lib/axios/axios.min.js`、`/lib/gridjs/gridjs.umd.js`、`/static/articles.js` が、次の同一オリジン JSON API を `GET` で呼び出して画面を構築する。

- `/api/sources`: 未削除の取得対象の ID、設定名、種類、有効状態
- `/api/articles`: 取得対象、日時範囲、検索語、件数、カーソルによる記事一覧
- `/api/articles/:id`: 本文位置と本文上限による記事詳細の分割取得

API は認証と CORS を追加せず、閉域ネットワーク内の同一オリジン利用に限定する。
すべての JSON 応答をキャッシュ禁止とし、DB 問い合わせ期限は 60 秒とする。
3 API は共通の 8 実行枠で同時実行数を制限し、満杯時は DB 接続を待たず HTTP 503 を返す。

MCP の Streamable HTTP は同じ待受ポートの `POST /mcp` で提供する。
MCP は管理画面用 CSRF の対象外とし、`Origin` ヘッダーを検証しない。
MCP リクエストは `application/json` と 1 MiB の本文上限を適用し、`POST` 以外のメソッドを拒否する。

## 管理画面の取得対象入力

取得対象の共通欄とコネクタ固有欄を分け、選択中の種類に対応する RSS または Reuters 固有欄だけを排他表示する。
非表示側の入力は無効化し、サーバー側でもコネクタ種別に不要な値を保存しない。

RSS ではフィード URL とリンク先本文抽出の有無を入力する。
Reuters では URL や内部 API パラメーターを入力させず、利用者は「取得内容」だけをサーバー定義のホワイトリストから選択する。
選択値はサーバー側で `section_id` へ変換する。
全取得対象は現行一覧 API への実通信で検証する。
日本は `/world/japan/` を使用し、旧版で保存された `/ja/` は読み込み時に現行値へ変換する。

## 処理の流れ

```text
管理画面 ──登録/変更──> news_sources
   │
   └─手動取得──> ジョブキュー ─┐
                               ├─> ingest.Service
スケジューラー ──期限到来──────┘       │
                                      ├─> RSS または Reuters
                                      ├─> news_articles（ON CONFLICT）
                                      └─> ingestion_runs / news_sources
```

1. スケジューラーは `next_fetch_at` が到来した有効な取得対象をキューへ入れる。
2. 手動取得も同じキューへ入り、HTTP リクエストを長時間占有しない。
3. 取り込みサービスはプロセス内マップと PostgreSQL advisory lock の二段階で、取得対象単位の同時実行を抑止する。
4. コネクタは見出しと本文を含む共通記事モデルを返す。
5. DB の一意キー `(source_id, external_id, COALESCE(updated_at, published_at, '-infinity'::timestamptz))` と `INSERT ... ON CONFLICT DO UPDATE` で、同じ記事の同日時を上書きし、異なる日時だけを別行に保存する。上書き時は記事 ID と `fetched_at` を維持する。
6. 取得履歴の完了と取得対象の次回予定は同じ DB トランザクションで確定する。
7. 個別記事の取得失敗は警告として記録し、取得可能な他の記事は保存する。
8. 起動時は古い `running` 履歴を確認し、該当取得対象の advisory lock が空いている場合だけ異常終了後の履歴として回復する。
9. Reuters は DB の `endpoint_url` に `https://jp.reuters.com` を保持し、`settings` には選択した `section_id` だけを新規保存する。
10. Reuters コネクタは `website=reuters-japan`、`offset=0`、一覧 API、詳細 API をコード上の固定値で補完する。
11. `[retention] enabled = true` の場合だけ、起動時と 1 時間ごとに `COALESCE(updated_at, published_at, fetched_at) <= CURRENT_TIMESTAMP - INTERVAL '168 hours'` の記事を削除し、保存時も期限切れ記事を省略する。取得対象の設定と取得履歴は残す。`false` または未指定の場合は両処理を無効にする。

通常画面の JSON API と MCP の参照経路は取得処理から分離し、記事検索と本文分割の PostgreSQL Repository を共有する。

```text
MCP クライアント ──POST /mcp──> HTTP ServeMux ──> MCP Server
                                                       │
                                                       ├─list_sources─────> news_sources
                                                       ├─search_articles──┐
                                                       └─get_article─────┐│
                                                                         ││
Web ブラウザー ──GET /admin/articles──────────────> Gin ──> 空のHTMLシェル ││
       │                                                                   ││
       ├─GET /api/sources──────────────────────────> Gin ────────────────> news_sources
       ├─GET /api/articles─────────────────────────> Gin ──共通Repository┘│
       └─GET /api/articles/:id─────────────────────> Gin ──共通Repository─┘

Web ブラウザー ──GET /healthz──────────────────────> Gin ──> PostgreSQL Ping
```

`list_sources` と `GET /api/sources` は未削除の `news_sources` から ID、設定名、種類、有効状態を返す。
保存済み記事の検索条件に使えるよう、現在無効な取得対象も一覧へ含める。

`search_articles` と `GET /api/articles` は、取得対象、両端を含む RFC3339 日時範囲、最大 5 個の検索語をすべて AND 条件で組み合わせる。
各検索語は見出しまたは本文に部分一致する記事を対象とし、`%`、`_`、`\` は通常文字として扱う。
日時を両方指定した場合は画面、API、MCP のいずれも入力順を問わず、早い日時を下限、遅い日時を上限へ自動的に正規化する。
日時を片方だけ指定した場合は、`date_from` を下限、`date_to` を上限とする意味を維持する。
日時は `COALESCE(published_at, fetched_at)` の降順、同日時では記事 ID の降順とする。
同じ日時と記事 ID の組を持つ不透明な keyset cursor を使い、全件数の `COUNT` と `OFFSET` を実行しない。
要求上限は 100 件とし、SQL では判定用の 1 件を加えて取得する。
全条件を省略した場合は `conf/mcp.toml` の `DefaultArticleLimit` 件を新しい順に返す。
本文抜粋は SQL で先頭 500 文字だけを読み出す。

通常画面の記事一覧は、API が返した最大件数内を Grid.js で 1 ページ 10 件にローカルページングする。
記事内容は本文抜粋を出さず、Grid.jsの1列内にヘッドライン、取得対象、更新日時をまとめて表示する。更新日時がない場合は投稿日時を使用し、取得日時にはフォールバックしない。
API の本文抜粋、取得件数、続きの有無、keyset cursor による「もっと読む」は画面へ出さない。
API と MCP の keyset cursor 仕様は維持し、通常画面だけがその後続カーソルを消費しない。
検索パネルは既定で閉じ、開いた場合は日時範囲を `～` で横並びにし、入力順にかかわらず早い日時から遅い日時までを検索する。
5 個の検索語入力は縦並びにし、最大件数は狭い数値欄で指定する。
最大件数欄には `conf/mcp.toml` の `DefaultArticleLimit` の実際の設定値を初期表示し、AND表記と重複する検索語の補足文は表示しない。

`get_article` と `GET /api/articles/:id` は本文を既定 8,000 文字、最大 16,000 文字に分割する。
SQL の `SUBSTRING` で要求範囲と続き判定用の 1 文字だけを読み出し、本文全体をアプリケーションへ読み込まない。
通常画面は `has_more` が真の場合だけ「続きを読む」を表示し、`next_body_offset` から続きを取得してテキストノードとして既存本文へ追加する。

通常画面の詳細仕様は [web-api.md](web-api.md)、MCP の詳細仕様は [mcp.md](mcp.md) に保存する。

## セキュリティ

- 管理画面は閉域ネットワークでの利用を前提とし、既定で `:8080` により全インターフェースで待ち受ける。
- 管理画面と MCP にアプリケーション内の利用者認証は設けない。インターネットへ直接公開せず、ファイアウォール、VPN、接続元制限などのネットワーク境界でアクセスを制御する。
- 状態変更リクエストは CSRF トークンを検証する。
- 記事 JSON API は参照専用の `GET` とし、同一オリジンからだけ利用する。CORS は有効化しない。
- MCP は読み取り専用 tool だけを公開し、記事、取得対象、取得処理を変更する操作を持たない。
- MCP は閉域ネットワーク境界で接続元を制限し、`Origin` ヘッダーの有無や値は検証しない。
- MCP はリクエスト本文を 1 MiB に制限し、DB の raw payload、SQL、接続情報、内部エラー詳細を返さない。
- HTML は `html/template` の自動エスケープを使う。JSON API のヘッドラインは Grid.js の仮想 DOM へテキスト子として渡し、本文と設定名は `textContent` で描画して、安全でない HTML として解釈しない。
- JSON API が返す元記事 URL は、ブラウザー側でも解析し、`http` または `https` の場合だけリンクとして表示する。
- 管理画面用 Bulma、Axios、Grid.js はローカルファイルを埋め込み配信し、外部 CDN を信頼境界へ加えない。
- 外部 HTTP はタイムアウトとレスポンスサイズ上限を設ける。
- URL は `http` または `https` のみ許可し、ユーザー情報を含む URL を拒否する。
- DNS 解決後の接続時に、ループバック、プライベート、リンクローカル、共有、文書用、ベンチマーク、予約済み等の特殊用途アドレスを拒否する。
- DB パスワードを含む実設定 `conf/*.toml` は Git 管理しない。

## 初期版の制約

- 複数プロセスから同じ取得対象が要求されても PostgreSQL advisory lock で重複取得を抑止する。ただし、定期キューそのものは各プロセスが個別に保持する。
- Reuters の取得先は公開 Web が内部利用する API で、安定した開発者向け契約ではない。URL、Web サイト識別子、開始位置、一覧・詳細 API をコードで固定し、変更時にコネクタだけ交換できる構造とする。
- Reuters のホワイトリストは現行一覧 API への実通信で検証する。内部 API のため、Reuters 側の変更により取得できなくなる可能性がある。
- RSS 本文はフィード本文を優先し、取得対象で指定された場合だけリンク先の記事要素を抽出する。抽出に失敗した場合はフィード本文または説明へフォールバックする。
- 記事検索は PostgreSQL の `pg_trgm` を利用するため、DB ユーザーには対象データベースで同拡張を作成できる権限が必要になる。
- MCP は軽量化を優先してステートレス JSON 応答だけを使用する。セッション、SSE、resources、prompts、MCP のログ配信機能は提供しない。
- MCP の検索ページングは `COALESCE(published_at, fetched_at)` と記事 ID をカーソルに使う。検索中に新しい記事が追加されても、続きの取得では前ページ末尾より新しい記事を読み直さない。
- MCP の DB 問い合わせ期限は 60 秒、HTTP 応答期限は 75 秒とし、遅い DB を許容しつつ処理枠を無期限に占有しない。
- 通常画面の記事検索も MCP と同じ Repository、実効日時順、keyset cursor、本文分割を使う。画面だけで独自の `COUNT`、`OFFSET`、全文読み込みを行わない。
- 部分一致検索では 1～2 文字の検索語に対する `pg_trgm` の効果が限定される。記事数が多い場合は取得対象や日時範囲を併用して走査対象を狭める。
