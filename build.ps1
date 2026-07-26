$ErrorActionPreference = "Stop"

$projectRoot = $PSScriptRoot
$sourceConfigurationSample = Join-Path $projectRoot "conf\conf.toml.sample"
$sourceDistributionReadme = Join-Path $projectRoot "docs\distribution.md"
$sourceLicense = Join-Path $projectRoot "LICENSE"
$distributionDirectory = Join-Path $projectRoot "dist"
$goCacheDirectory = Join-Path $projectRoot ".cache\go-build"
$temporaryBuildDirectory = Join-Path $projectRoot ".cache\distribution-build"
$temporaryDistributionDirectory = Join-Path $temporaryBuildDirectory (
    "dist.{0}" -f [Guid]::NewGuid().ToString("N")
)
$backupDistributionDirectory = Join-Path $temporaryBuildDirectory (
    "dist-backup.{0}" -f [Guid]::NewGuid().ToString("N")
)

$originalEnvironment = @{
    GOOS        = $env:GOOS
    GOARCH      = $env:GOARCH
    CGO_ENABLED = $env:CGO_ENABLED
    GOCACHE     = $env:GOCACHE
}
$existingEnvironment = @{
    GOOS        = Test-Path Env:GOOS
    GOARCH      = Test-Path Env:GOARCH
    CGO_ENABLED = Test-Path Env:CGO_ENABLED
    GOCACHE     = Test-Path Env:GOCACHE
}

$targets = @(
    @{
        Name       = "Windows x64"
        FolderName = "Windows"
        GOOS       = "windows"
        GOARCH     = "amd64"
        OutputName = "newsstore.exe"
    },
    @{
        Name       = "Linux x64"
        FolderName = "linux"
        GOOS       = "linux"
        GOARCH     = "amd64"
        OutputName = "newsstore"
    }
)

try {
    Get-Command go -ErrorAction Stop | Out-Null

    if (!(Test-Path -LiteralPath $sourceConfigurationSample -PathType Leaf)) {
        throw ("配布用の設定サンプルが見つかりません: {0}" -f $sourceConfigurationSample)
    }
    if (!(Test-Path -LiteralPath $sourceDistributionReadme -PathType Leaf)) {
        throw ("配布用 README が見つかりません: {0}" -f $sourceDistributionReadme)
    }
    if (!(Test-Path -LiteralPath $sourceLicense -PathType Leaf)) {
        throw ("ライセンスファイルが見つかりません: {0}" -f $sourceLicense)
    }

    New-Item -ItemType Directory -Force -Path $goCacheDirectory | Out-Null
    New-Item -ItemType Directory -Force -Path $temporaryBuildDirectory | Out-Null
    New-Item -ItemType Directory -Force -Path $temporaryDistributionDirectory | Out-Null

    $env:GOCACHE = $goCacheDirectory
    $env:CGO_ENABLED = "0"

    Push-Location $projectRoot
    try {
        foreach ($target in $targets) {
            $targetDirectory = Join-Path $temporaryDistributionDirectory $target.FolderName
            $targetConfigurationDirectory = Join-Path $targetDirectory "conf"
            $outputPath = Join-Path $targetDirectory $target.OutputName
            New-Item -ItemType Directory -Force -Path $targetDirectory | Out-Null
            New-Item `
                -ItemType Directory `
                -Force `
                -Path $targetConfigurationDirectory | Out-Null

            Copy-Item `
                -LiteralPath $sourceConfigurationSample `
                -Destination (Join-Path $targetConfigurationDirectory "conf.toml.sample") `
                -Force
            Copy-Item `
                -LiteralPath $sourceDistributionReadme `
                -Destination (Join-Path $targetDirectory "README.md") `
                -Force
            Copy-Item `
                -LiteralPath $sourceLicense `
                -Destination (Join-Path $targetDirectory "LICENSE") `
                -Force

            $env:GOOS = $target.GOOS
            $env:GOARCH = $target.GOARCH

            Write-Host ("{0} をビルドしています..." -f $target.Name)
            & go build -trimpath -ldflags "-s -w" -o $outputPath .
            if ($LASTEXITCODE -ne 0) {
                throw ("{0} のビルドに失敗しました。終了コード: {1}" -f $target.Name, $LASTEXITCODE)
            }
        }
    }
    finally {
        Pop-Location
    }

    if (Test-Path -LiteralPath $distributionDirectory) {
        Move-Item `
            -LiteralPath $distributionDirectory `
            -Destination $backupDistributionDirectory
    }

    try {
        Move-Item `
            -LiteralPath $temporaryDistributionDirectory `
            -Destination $distributionDirectory
    }
    catch {
        $replacementError = $_
        if (
            (Test-Path -LiteralPath $backupDistributionDirectory) -and
            !(Test-Path -LiteralPath $distributionDirectory)
        ) {
            Move-Item `
                -LiteralPath $backupDistributionDirectory `
                -Destination $distributionDirectory
        }
        throw $replacementError
    }

    if (Test-Path -LiteralPath $backupDistributionDirectory) {
        Remove-Item -LiteralPath $backupDistributionDirectory -Recurse -Force
    }

    foreach ($target in $targets) {
        $targetDirectory = Join-Path $distributionDirectory $target.FolderName
        Write-Host ("作成しました: {0}" -f $targetDirectory)
    }
}
finally {
    if (Test-Path -LiteralPath $temporaryDistributionDirectory) {
        Remove-Item -LiteralPath $temporaryDistributionDirectory -Recurse -Force
    }

    foreach ($name in $originalEnvironment.Keys) {
        if ($existingEnvironment[$name]) {
            Set-Item -Path ("Env:{0}" -f $name) -Value $originalEnvironment[$name]
        }
        else {
            Remove-Item -Path ("Env:{0}" -f $name) -ErrorAction SilentlyContinue
        }
    }
}
