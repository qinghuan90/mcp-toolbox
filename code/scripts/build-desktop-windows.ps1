param([string]$GoBin = "go")
$ErrorActionPreference = "Stop"
$rootDir = Split-Path -Parent $PSScriptRoot
$originalLocation = Get-Location
function Invoke-Checked([scriptblock]$Command) {
    & $Command
    if ($LASTEXITCODE -ne 0) { throw "Build command failed with exit code $LASTEXITCODE" }
}
try {
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    $env:CGO_ENABLED = "0"
    $wailsDir = Join-Path $rootDir ".cache\bin"
    New-Item -ItemType Directory -Force -Path $wailsDir | Out-Null
    $env:GOBIN = $wailsDir
    $wailsBin = Join-Path $wailsDir "wails.exe"
    if (!(Test-Path $wailsBin)) { Invoke-Checked { & $GoBin install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0 } }
    Set-Location (Join-Path $rootDir "web")
    Invoke-Checked { npm.cmd ci --cache (Join-Path $rootDir ".cache\npm") }
    Invoke-Checked { npm.cmd run build }
    $webdist = Join-Path $rootDir "backend\cmd\server\webdist"
    New-Item -ItemType Directory -Force -Path $webdist | Out-Null
    Get-ChildItem -Path $webdist -Force | Remove-Item -Recurse -Force
    Copy-Item -Path (Join-Path $rootDir "web\dist\*") -Destination $webdist -Recurse
    Set-Location (Join-Path $rootDir "backend\cmd\server")
    Invoke-Checked { & $wailsBin build -s -m -skipbindings -skipembedcreate -nosyncgomod -compiler $GoBin -tags desktop -platform windows/amd64 -o Toolbox.exe }
    Invoke-Checked { python (Join-Path $rootDir "scripts\package-desktop.py") windows amd64 }
} finally {
    Set-Location $originalLocation
}
