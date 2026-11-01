param([string]$GoExe = 'go')
$ErrorActionPreference = 'Stop'
if ($GoExe -eq 'go' -and -not (Get-Command go -ErrorAction SilentlyContinue)) {
    $bundledGo = Join-Path $PSScriptRoot '../../runtime/go/bin/go.exe'
    if (Test-Path -LiteralPath $bundledGo) { $GoExe = (Resolve-Path -LiteralPath $bundledGo).Path }
}
Push-Location $PSScriptRoot
try {
    & $GoExe mod download
    if ($LASTEXITCODE -ne 0) { throw 'MCP dependency download failed' }
    & $GoExe build -trimpath -o docflow-mcp.exe ./cmd/docflow-mcp
    if ($LASTEXITCODE -ne 0) { throw 'MCP build failed' }
    Write-Output "Built: $PSScriptRoot/docflow-mcp.exe"
} finally { Pop-Location }
