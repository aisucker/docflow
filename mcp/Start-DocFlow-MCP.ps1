param([string]$ConfigFile = (Join-Path $PSScriptRoot '../local-config.json'), [switch]$ReadOnly)
$ErrorActionPreference = 'Stop'
[Console]::InputEncoding = [Text.UTF8Encoding]::new($false)
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
$OutputEncoding = [Text.UTF8Encoding]::new($false)
# stdout belongs exclusively to the MCP protocol. Do not print configuration.
$binary = Join-Path $PSScriptRoot 'docflow-mcp.exe'
if (-not (Test-Path -LiteralPath $binary)) { throw 'Build MCP first: powershell -NoProfile -File mcp/build.ps1' }
if (-not (Test-Path -LiteralPath $ConfigFile)) { throw 'Pass -ConfigFile with a Docflow user configuration file.' }
$arguments = @('--config-file', (Resolve-Path -LiteralPath $ConfigFile).Path)
if ($ReadOnly) { $arguments += '--read-only' }
& $binary @arguments
exit $LASTEXITCODE
