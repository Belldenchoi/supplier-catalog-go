param(
    [Parameter(Mandatory=$true)][string]$Transcript,
    [Parameter(Mandatory=$true)][string]$OutputDirectory
)
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Speech
$demoData = Get-Content -Raw -LiteralPath $Transcript -Encoding UTF8 | ConvertFrom-Json
New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null
$speaker = New-Object System.Speech.Synthesis.SpeechSynthesizer
try {
    $speaker.Rate = 1
    $speaker.Volume = 100
    $english = $speaker.GetInstalledVoices() | Where-Object { $_.Enabled -and $_.VoiceInfo.Culture.Name -eq 'en-US' } | Select-Object -First 1
    if ($english) { $speaker.SelectVoice($english.VoiceInfo.Name) }
    for ($sceneIndex = 0; $sceneIndex -lt $demoData.scenes.Count; $sceneIndex++) {
        $wavPath = Join-Path $OutputDirectory ('scene-' + $sceneIndex + '.wav')
        $speaker.SetOutputToWaveFile($wavPath)
        $speaker.Speak($demoData.scenes[$sceneIndex].narration)
        $speaker.SetOutputToNull()
    }
} finally { $speaker.Dispose() }
