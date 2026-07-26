package postgres

import "errors"

// ErrNotFound は、指定された永続化データが存在しないことを表します。
var ErrNotFound = errors.New("対象データが見つかりません")
