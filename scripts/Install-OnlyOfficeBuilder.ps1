param([string]$RuntimeRoot='E:\DocJetRuntime',[string]$Version='10.0.0')
$ErrorActionPreference='Stop'
if($Version -notmatch '^\d+\.\d+\.\d+$'){throw 'Invalid version'}
$root=[IO.Path]::GetFullPath($RuntimeRoot)
$target=[IO.Path]::GetFullPath((Join-Path $root "onlyoffice-builder-$Version"))
if(-not $target.StartsWith($root.TrimEnd('\')+'\',[StringComparison]::OrdinalIgnoreCase)){throw 'Invalid runtime target'}
$exe=Join-Path $target 'docbuilder.exe'
if(Test-Path -LiteralPath $exe){Write-Output "Already installed: $exe";exit 0}
New-Item -ItemType Directory -Path $target -Force|Out-Null
$archive=Join-Path $target 'onlyoffice-documentbuilder-windows-x64.zip'
Invoke-WebRequest "https://github.com/ONLYOFFICE/DocumentBuilder/releases/download/v$Version/onlyoffice-documentbuilder-windows-x64.zip" -OutFile $archive
Add-Type -AssemblyName System.IO.Compression.FileSystem
$zip=[IO.Compression.ZipFile]::OpenRead($archive);$size=0L
try{foreach($entry in $zip.Entries){$resolved=[IO.Path]::GetFullPath((Join-Path $target $entry.FullName));if(-not $resolved.StartsWith($target+'\',[StringComparison]::OrdinalIgnoreCase)){throw 'Archive path outside target'};$size+=$entry.Length};if($size -gt 1GB){throw 'Archive exceeds expected size'}}finally{$zip.Dispose()}
Expand-Archive -LiteralPath $archive -DestinationPath $target -Force
if(-not (Test-Path -LiteralPath $exe)){throw 'docbuilder.exe not found'}
$signature=Get-AuthenticodeSignature -LiteralPath $exe
if($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notmatch 'Ascensio System'){throw 'Unexpected executable signature; do not run this package'}
Write-Output "Installed: $exe"
Write-Output 'Set this path in DocJet > Administration > OnlyOffice.'
Write-Output 'The free Document Builder includes a watermark; use the appropriate license for watermark-free output.'
