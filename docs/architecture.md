# NewsStore アーキテクチャ

## 目的

NewsStore は、RSS/Atom フィードと個別の REST API からニュース記事を取得し、見出しと本文を PostgreSQL に保存する。
取得対象、取得間隔、コネクタ固有設定は管理画面から変更し、PostgreSQL を正本とする。
`conf/*.toml` は、DB 接続と HTTP サーバーの待受ポートといったアプリ起動設定だけを扱う。
実行設定は `conf/system.toml`、PostgreSQL 接続設定は `conf/postgres.toml` に分離する。

## 実行構成

初期版は、次の責務を 1 つの Go バイナリにまとめる。

1. Gin による管理画面とヘルスチェック
2. DB に保存された取得対象を定期確認するスケジューラー
3. RSS/Atom コネクタ
4. Reuters コネクタ
5. PostgreSQL への冪等保存

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
internal/store/postgres/        PostgreSQL 接続、マイグレーション、Repository
internal/web/                   Gin ルーター、管理画面、CSRF
internal/web/templates/         埋め込み HTML テンプレート
internal/web/static/            埋め込み CSS
web/assets.go                   ローカル Web ライブラリの埋め込みと配信
web/lib/bulma/                  管理画面用のローカル Bulma
docs/                           設計キャッシュと運用情報
```

実行ファイルのパッケージはプロジェクトルートに置き、`go run .` で既定の `serve` を起動する。
管理画面の Bulma は `web/lib` からバイナリへ埋め込むため、実行時に CDN へ接続しない。
管理画面はダークテーマを既定とする。

## 起動設定

`conf` ディレクトリ内の TOML はファイル名昇順で読み込む。
標準構成では責務を次のように分け、DB パスワードをアプリケーションの一般設定から分離する。

- `conf/system.toml`: `[SYSTEM]` セクションの `Port` による HTTP 待受ポート設定
- `conf/postgres.toml`: PostgreSQL の接続、コネクションプール、マイグレーション設定

`conf/system.toml` の正規形は次の 1 設定だけとする。

```toml
[SYSTEM]
Port = 8080
```

設定全体のサンプルは `conf/conf.toml.sample` に保存する。
HTTP サーバーは `Port` から `:Port` 形式のアドレスを組み立て、全インターフェースで待ち受ける。

## Web ルーティング

トップ URL `/` は管理画面の取得対象一覧 `/admin/sources` へリダイレクトする。
利用者は `http://localhost:8080/` から管理画面へアクセスできる。
ヘルスチェックは `/healthz`、ローカル Web ライブラリは `/lib` 配下で配信する。

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
5. DB の一意制約と `INSERT ... ON CONFLICT DO NOTHING` を最終的な重複防止にする。
6. 取得履歴の完了と取得対象の次回予定は同じ DB トランザクションで確定する。
7. 個別記事の取得失敗は警告として記録し、取得可能な他の記事は保存する。
8. 起動時は古い `running` 履歴を確認し、該当取得対象の advisory lock が空いている場合だけ異常終了後の履歴として回復する。
9. Reuters は DB の `endpoint_url` に `https://jp.reuters.com` を保持し、`settings` には選択した `section_id` だけを新規保存する。
10. Reuters コネクタは `website=reuters-japan`、`offset=0`、一覧 API、詳細 API をコード上の固定値で補完する。

## セキュリティ

- 管理画面は閉域ネットワークでの利用を前提とし、既定で `:8080` により全インターフェースで待ち受ける。
- 管理画面にアプリケーション内の利用者認証は設けない。インターネットへ直接公開せず、ファイアウォール、VPN、接続元制限などのネットワーク境界でアクセスを制御する。
- 状態変更リクエストは CSRF トークンを検証する。
- HTML は `html/template` の自動エスケープを使い、記事本文を安全でない HTML として描画しない。
- 管理画面用 Bulma はローカルファイルを埋め込み配信し、外部 CDN を信頼境界へ加えない。
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
