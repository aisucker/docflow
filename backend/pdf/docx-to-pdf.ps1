param([string]$Source,[string]$Target)
$ErrorActionPreference='Stop'
$word=$null
$document=$null
try {
    # A dedicated instance; never attach to an existing user document.
    $word=New-Object -ComObject Word.Application
    $word.Visible=$false
    $word.DisplayAlerts=0
    $word.AutomationSecurity=3
    $word.Options.UpdateLinksAtOpen=$false
    $document=$word.Documents.Open($Source,$false,$true,$false)
    $document.ExportAsFixedFormat($Target,17)
} finally {
    if($document){$document.Close(0);[void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($document)}
    if($word){$word.Quit();[void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($word)}
}
