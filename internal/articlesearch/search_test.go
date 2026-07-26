package articlesearch

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

/*
TestNormalizeKeywords は、AND検索語の正規化を検証します。

機能:
  - 空文字、前後空白、大文字小文字違いの重複を正規化する
  - 最初に現れた検索語の表記と順序を保持する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestNormalizeKeywords(t *testing.T) {
	keywords, err := NormalizeKeywords(
		"  経済  ",
		[]string{" 政策 ", "経済", "", "NEWS", "news"},
	)
	if err != nil {
		t.Fatalf("NormalizeKeywords() error = %v", err)
	}
	expected := []string{"経済", "政策", "NEWS"}
	if len(keywords) != len(expected) {
		t.Fatalf("検索語数 = %d, 期待値は%d: %v", len(keywords), len(expected), keywords)
	}
	for index := range expected {
		if keywords[index] != expected[index] {
			t.Errorf("検索語[%d] = %q, 期待値は%q", index, keywords[index], expected[index])
		}
	}
}

// ----------------------------------------

/*
TestNormalizeKeywordsRejectsLimits は、AND検索語の上限検証を確認します。

機能:
  - 1語200Unicode文字を受け付け、201文字を拒否する
  - 重複除去後に6語残る条件を拒否する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestNormalizeKeywordsRejectsLimits(t *testing.T) {
	if _, err := NormalizeKeywords(strings.Repeat("記", MaxKeywordRunes), nil); err != nil {
		t.Errorf("上限内の検索語が拒否されました: %v", err)
	}

	testCases := []struct {
		name     string
		query    string
		keywords []string
		want     string
	}{
		{
			name:  "文字数超過",
			query: strings.Repeat("記", MaxKeywordRunes+1),
			want:  "検索語は1語あたり200文字以内で指定してください",
		},
		{
			name:     "語数超過",
			keywords: []string{"1", "2", "3", "4", "5", "6"},
			want:     "query と keywords は合わせて5語以内で指定してください",
		},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			_, err := NormalizeKeywords(testCase.query, testCase.keywords)
			if err == nil || err.Error() != testCase.want {
				t.Errorf("error = %v, 期待値は%q", err, testCase.want)
			}
		})
	}
}

// ----------------------------------------

/*
TestParseTime は、検索日時の解析とUTC統一を検証します。

機能:
  - 空文字を未指定として扱う
  - RFC3339日時をUTCへ変換し、不正形式を日本語エラーで拒否する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestParseTime(t *testing.T) {
	empty, err := ParseTime("date_from", "  ")
	if err != nil || empty != nil {
		t.Fatalf("空文字 = time:%v error:%v, nilとnilを期待", empty, err)
	}

	parsed, err := ParseTime("date_from", " 2026-07-26T09:30:00.123456789+09:00 ")
	if err != nil {
		t.Fatalf("ParseTime() error = %v", err)
	}
	expected := time.Date(2026, 7, 26, 0, 30, 0, 123456789, time.UTC)
	if parsed == nil || !parsed.Equal(expected) || parsed.Location() != time.UTC {
		t.Errorf("解析日時 = %v, 期待値は%sのUTC", parsed, expected)
	}

	_, err = ParseTime("date_to", "2026-07-26")
	if err == nil || err.Error() != "date_to はRFC3339形式で指定してください" {
		t.Errorf("不正形式のerror = %v", err)
	}
}

// ----------------------------------------

/*
TestNormalizeTimeRange は、検索日時範囲の入力順と片側指定を検証します。

機能:
  - 両端を古い順と新しい順のどちらで指定しても時系列順へ正規化する
  - 片側だけの指定では下限または上限としての意味を維持する
  - 同一日時と両端未指定をそのまま受け付ける

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestNormalizeTimeRange(t *testing.T) {
	earlier := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	later := time.Date(2026, 7, 31, 23, 59, 59, 0, time.UTC)
	testCases := []struct {
		name     string
		dateFrom *time.Time
		dateTo   *time.Time
		wantFrom *time.Time
		wantTo   *time.Time
	}{
		{
			name:     "古い日時から新しい日時",
			dateFrom: &earlier,
			dateTo:   &later,
			wantFrom: &earlier,
			wantTo:   &later,
		},
		{
			name:     "新しい日時から古い日時",
			dateFrom: &later,
			dateTo:   &earlier,
			wantFrom: &earlier,
			wantTo:   &later,
		},
		{
			name:     "下限のみ",
			dateFrom: &earlier,
			wantFrom: &earlier,
		},
		{
			name:   "上限のみ",
			dateTo: &later,
			wantTo: &later,
		},
		{
			name:     "同一日時",
			dateFrom: &earlier,
			dateTo:   &earlier,
			wantFrom: &earlier,
			wantTo:   &earlier,
		},
		{name: "両端未指定"},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			dateFrom, dateTo := NormalizeTimeRange(
				testCase.dateFrom,
				testCase.dateTo,
			)
			if !equalOptionalTime(dateFrom, testCase.wantFrom) ||
				!equalOptionalTime(dateTo, testCase.wantTo) {
				t.Errorf(
					"日時範囲 = from:%v to:%v, 期待値は from:%v to:%v",
					dateFrom,
					dateTo,
					testCase.wantFrom,
					testCase.wantTo,
				)
			}
		})
	}
}

// ----------------------------------------

/*
TestNormalizeLimit は、検索件数の既定値と範囲を検証します。

機能:
  - 未指定の0を設定由来の既定値へ変換する
  - 1以上100以下だけを許可する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestNormalizeLimit(t *testing.T) {
	testCases := []struct {
		name         string
		input        int
		defaultLimit int
		want         int
		wantError    string
	}{
		{name: "既定値", input: 0, defaultLimit: 20, want: 20},
		{name: "下限", input: 1, defaultLimit: 20, want: 1},
		{name: "上限", input: MaxLimit, defaultLimit: 20, want: MaxLimit},
		{
			name:         "負数",
			input:        -1,
			defaultLimit: 20,
			wantError:    "limit は1以上100以下で指定してください",
		},
		{
			name:         "上限超過",
			input:        MaxLimit + 1,
			defaultLimit: 20,
			wantError:    "limit は1以上100以下で指定してください",
		},
		{
			name:         "既定値不正",
			input:        0,
			defaultLimit: 0,
			wantError:    "limit は1以上100以下で指定してください",
		},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			actual, err := NormalizeLimit(testCase.input, testCase.defaultLimit)
			if testCase.wantError != "" {
				if err == nil || err.Error() != testCase.wantError {
					t.Errorf("error = %v, 期待値は%q", err, testCase.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeLimit() error = %v", err)
			}
			if actual != testCase.want {
				t.Errorf("件数 = %d, 期待値は%d", actual, testCase.want)
			}
		})
	}
}

// ----------------------------------------

/*
TestCursorRoundTrip は、v2カーソルの生成と復元を検証します。

機能:
  - UTC以外の日時と正の記事IDをURLセーフBase64へ変換する
  - 復元した日時をUTCへ統一し、記事IDを保持する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestCursorRoundTrip(t *testing.T) {
	location := time.FixedZone("JST", 9*60*60)
	effectiveAt := time.Date(2026, 7, 26, 18, 30, 0, 123, location)
	cursor := EncodeCursor(effectiveAt, 87)
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatalf("カーソルをBase64解析できません: %v", err)
	}
	if !strings.HasPrefix(string(decoded), "v2|") {
		t.Errorf("カーソル本文 = %q, v2版を期待", decoded)
	}

	parsedTime, parsedID, err := DecodeCursor(cursor)
	if err != nil {
		t.Fatalf("DecodeCursor() error = %v", err)
	}
	if parsedTime == nil ||
		!parsedTime.Equal(effectiveAt) ||
		parsedTime.Location() != time.UTC ||
		parsedID != 87 {
		t.Errorf("復元値 = time:%v id:%d", parsedTime, parsedID)
	}
}

// ----------------------------------------

/*
TestDecodeCursorRejectsInvalidValues は、不正なカーソルの拒否を検証します。

機能:
  - 空カーソルを先頭ページとして扱う
  - 長さ、Base64、版、日時、記事IDの不正を安全な日本語エラーにする

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestDecodeCursorRejectsInvalidValues(t *testing.T) {
	parsedTime, parsedID, err := DecodeCursor("")
	if err != nil || parsedTime != nil || parsedID != 0 {
		t.Fatalf("空カーソル = time:%v id:%d error:%v", parsedTime, parsedID, err)
	}

	encode := func(value string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(value))
	}
	testCases := []struct {
		name   string
		cursor string
		want   string
	}{
		{
			name:   "長さ超過",
			cursor: strings.Repeat("a", MaxCursorLength+1),
			want:   "cursor が不正です",
		},
		{name: "Base64不正", cursor: "%", want: "cursor が不正です"},
		{
			name:   "旧版",
			cursor: encode("v1|2026-07-26T00:00:00Z|1"),
			want:   "cursor が不正です。先頭ページから検索し直してください",
		},
		{
			name:   "日時不正",
			cursor: encode("v2|invalid|1"),
			want:   "cursor が不正です",
		},
		{
			name:   "記事ID不正",
			cursor: encode("v2|2026-07-26T00:00:00Z|0"),
			want:   "cursor が不正です",
		},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			_, _, err := DecodeCursor(testCase.cursor)
			if err == nil || err.Error() != testCase.want {
				t.Errorf("error = %v, 期待値は%q", err, testCase.want)
			}
		})
	}
}

// ----------------------------------------

/*
equalOptionalTime は、nilを許容する2つの日時が同じかを判定します。

機能:
  - 両方がnilの場合を同値として扱う
  - 片方だけがnilの場合を異なる値として扱う
  - 両方が日時の場合はtime.Time.Equalで同じ時点かを判定する

引数:
  - left *time.Time: 比較する左側の日時。nilを許容
  - right *time.Time: 比較する右側の日時。nilを許容

返り値:
  - bool: 両者が同じ指定状態かつ同じ時点の場合はtrue
*/
func equalOptionalTime(left *time.Time, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}
