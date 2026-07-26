// Package articlesearch は、記事一覧で共通利用する検索条件の正規化処理を提供します。
package articlesearch

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxLimit は、記事一覧が1回に返せる最大件数です。
	MaxLimit = 100
	// MaxKeywords は、AND検索へ指定できる最大語数です。
	MaxKeywords = 5
	// MaxKeywordRunes は、検索語1語に指定できる最大Unicode文字数です。
	MaxKeywordRunes = 200
	// MaxCursorLength は、ページカーソルとして受け付ける最大バイト数です。
	MaxCursorLength = 128
)

/*
NormalizeKeywords は、単語検索条件をAND検索用の一覧へ変換します。

機能:
  - queryとkeywordsを前後空白除去後に結合する
  - 大文字小文字を区別せず重複語と空文字を除外する
  - 語数と各語のUnicode文字数を上限内へ制限する

引数:
  - query string: 後方互換用の単一検索語
  - keywords []string: 複数の検索語

返り値:
  - []string: 正規化済みのAND検索語
  - error: 語数または文字数が上限を超えた場合のエラー
*/
func NormalizeKeywords(query string, keywords []string) ([]string, error) {
	candidates := make([]string, 0, len(keywords)+1)
	candidates = append(candidates, query)
	candidates = append(candidates, keywords...)

	normalized := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		keyword := strings.TrimSpace(candidate)
		if keyword == "" {
			continue
		}
		if utf8.RuneCountInString(keyword) > MaxKeywordRunes {
			return nil, fmt.Errorf(
				"検索語は1語あたり%d文字以内で指定してください",
				MaxKeywordRunes,
			)
		}
		key := strings.ToLower(keyword)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, keyword)
	}
	if len(normalized) > MaxKeywords {
		return nil, fmt.Errorf(
			"query と keywords は合わせて%d語以内で指定してください",
			MaxKeywords,
		)
	}
	return normalized, nil
}

// ----------------------------------------

/*
ParseTime は、任意指定の検索日時を解析します。

機能:
  - 前後空白だけの値を未指定としてnilへ変換する
  - タイムゾーンを含むRFC3339日時をUTCへ統一する

引数:
  - fieldName string: 入力エラーへ表示するフィールド名
  - value string: 利用者が指定した日時文字列

返り値:
  - *time.Time: UTCへ正規化した日時。未指定時はnil
  - error: RFC3339として解析できない場合のエラー
*/
func ParseTime(fieldName string, value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("%s はRFC3339形式で指定してください", fieldName)
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

// ----------------------------------------

/*
NormalizeTimeRange は、任意指定の検索日時範囲を時系列順へ正規化します。

機能:
  - 両端が指定され、dateFromがdateToより後の場合だけ両者を入れ替える
  - 片側だけ指定された場合は下限または上限としての意味を維持する
  - 両端が同じ場合と両端とも未指定の場合は入力を維持する

引数:
  - dateFrom *time.Time: 検索日時の下限。nilは未指定
  - dateTo *time.Time: 検索日時の上限。nilは未指定

返り値:
  - *time.Time: 時系列順へ正規化した検索日時の下限
  - *time.Time: 時系列順へ正規化した検索日時の上限
*/
func NormalizeTimeRange(
	dateFrom *time.Time,
	dateTo *time.Time,
) (*time.Time, *time.Time) {
	if dateFrom != nil && dateTo != nil && dateFrom.After(*dateTo) {
		return dateTo, dateFrom
	}
	return dateFrom, dateTo
}

// ----------------------------------------

/*
NormalizeLimit は、記事一覧の最大取得件数を正規化します。

機能:
  - 0を設定由来の既定件数へ変換する
  - 指定件数と既定件数を1以上100以下へ制限する

引数:
  - input int: 利用者が指定した件数。0は未指定
  - defaultLimit int: inputが0の場合に使う設定由来の件数

返り値:
  - int: 検索で使用する最大取得件数
  - error: 件数が範囲外の場合のエラー
*/
func NormalizeLimit(input int, defaultLimit int) (int, error) {
	limit := input
	if limit == 0 {
		limit = defaultLimit
	}
	if limit < 1 || limit > MaxLimit {
		return 0, fmt.Errorf(
			"limit は1以上%d以下で指定してください",
			MaxLimit,
		)
	}
	return limit, nil
}

// ----------------------------------------

/*
EncodeCursor は、検索日時と記事IDを不透明なページカーソルへ変換します。

機能:
  - 検索日時と記事IDをv2版のURLセーフBase64へ変換する
  - 検索日時をUTCのRFC3339 Nano形式へ統一する

引数:
  - effectiveAt time.Time: 次回検索の上限にする検索日時
  - id int64: 同一日時内で次回検索の上限にする記事ID

返り値:
  - string: URLセーフなv2ページカーソル
*/
func EncodeCursor(effectiveAt time.Time, id int64) string {
	value := strings.Join(
		[]string{
			"v2",
			effectiveAt.UTC().Format(time.RFC3339Nano),
			strconv.FormatInt(id, 10),
		},
		"|",
	)
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

// ----------------------------------------

/*
DecodeCursor は、ページカーソルから検索日時と記事IDを復元します。

機能:
  - 空文字を最初のページとしてnilと0へ変換する
  - 長さ、Base64、版、RFC3339日時、正の記事IDを検証する

引数:
  - cursor string: 直前の記事一覧が返したページカーソル

返り値:
  - *time.Time: 次回検索で上限にするUTCの検索日時
  - int64: 同一日時内で次回検索の上限にする記事ID
  - error: カーソルが不正な場合のエラー
*/
func DecodeCursor(cursor string) (*time.Time, int64, error) {
	if cursor == "" {
		return nil, 0, nil
	}
	if len(cursor) > MaxCursorLength {
		return nil, 0, errors.New("cursor が不正です")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, 0, errors.New("cursor が不正です")
	}
	parts := strings.Split(string(decoded), "|")
	if len(parts) != 3 || parts[0] != "v2" {
		return nil, 0, errors.New("cursor が不正です。先頭ページから検索し直してください")
	}
	effectiveAt, err := time.Parse(time.RFC3339Nano, parts[1])
	if err != nil {
		return nil, 0, errors.New("cursor が不正です")
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || id < 1 {
		return nil, 0, errors.New("cursor が不正です")
	}
	effectiveAt = effectiveAt.UTC()
	return &effectiveAt, id, nil
}
