$ErrorActionPreference = "Stop"

$goCacheDirectory = Join-Path $PSScriptRoot ".cache\go-build"
New-Item -ItemType Directory -Force -Path $goCacheDirectory | Out-Null
$env:GOCACHE = $goCacheDirectory

Write-Host "Go ソースを整形確認しています..."
$unformatted = gofmt -l main.go main_test.go internal web
if ($LASTEXITCODE -ne 0) {
    throw "gofmt の実行に失敗しました。終了コード: $LASTEXITCODE"
}
if ($unformatted) {
    Write-Error "gofmt が必要なファイルがあります:`n$($unformatted -join "`n")"
}

Write-Host "単体テストを実行しています..."
go test ./...
if ($LASTEXITCODE -ne 0) {
    throw "go test に失敗しました。終了コード: $LASTEXITCODE"
}

Write-Host "静的検査を実行しています..."
go vet ./...
if ($LASTEXITCODE -ne 0) {
    throw "go vet に失敗しました。終了コード: $LASTEXITCODE"
}

Write-Host "Go モジュール定義の整合性を確認しています..."
go mod tidy -diff
if ($LASTEXITCODE -ne 0) {
    throw "go mod tidy -diff に失敗しました。終了コード: $LASTEXITCODE"
}

Write-Host "ビルドを確認しています..."
$outputDirectory = Join-Path $PSScriptRoot ".cache\test-build"
New-Item -ItemType Directory -Force -Path $outputDirectory | Out-Null
$outputPath = Join-Path $outputDirectory "newsstore.exe"
go build -o $outputPath .
if ($LASTEXITCODE -ne 0) {
    throw "go build に失敗しました。終了コード: $LASTEXITCODE"
}

Write-Host "すべてのテストが成功しました。"
