$ErrorActionPreference = 'Stop'

$repositoryRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
. (Join-Path $repositoryRoot 'scripts/install.ps1')

if (-not (Test-CodeboxWindows)) {
	$platformRejected = $false
	try { Install-Codebox -InstallDirectory $env:TEMP -Architecture AMD64 } catch { $platformRejected = $true }
	if (-not $platformRejected) { throw 'Windows installer accepted a non-Windows host.' }
}
function Test-CodeboxWindows { $true }

$global:InstallerTestTag = 'codebox-v9.8.7'
$global:InstallerTestBinary = [System.Text.Encoding]::ASCII.GetBytes('verified test binary')
$global:InstallerTestSums = ''
$global:InstallerTestUris = @()
$global:InstallerSetupInvoked = $false
$global:InstallerSetupExitCode = 0
$global:InstallerFailDownload = $false
$global:InstallerReleaseCalls = 0

function Assert-InstallerTest {
	param([bool]$Condition, [string]$Message)
	if (-not $Condition) { throw $Message }
}

function Get-CodeboxLatestRelease {
	$global:InstallerReleaseCalls++
	[pscustomobject]@{ tag_name = $global:InstallerTestTag }
}

function Save-CodeboxReleaseAsset {
	param([string]$Uri, [string]$Path)
	$global:InstallerTestUris += $Uri
	if ($Uri.EndsWith('/SHA256SUMS', [System.StringComparison]::Ordinal)) {
		[System.IO.File]::WriteAllText($Path, $global:InstallerTestSums, [System.Text.Encoding]::ASCII)
		return
	}
	if ($global:InstallerFailDownload) {
		[System.IO.File]::WriteAllBytes($Path, [byte[]]@(1, 2, 3))
		throw 'simulated interrupted download'
	}
	[System.IO.File]::WriteAllBytes($Path, $global:InstallerTestBinary)
}

function Invoke-CodeboxSetup {
	param([string]$Path)
	$global:InstallerSetupInvoked = $true
	$global:InstallerSetupExitCode
}

function Set-InstallerTestChecksum {
	param([string]$Hash)
	$global:InstallerTestSums = "$Hash  codebox-windows-x64.exe`n"
}

$releaseMetadata = Get-CodeboxReleaseMetadataFromUri -Uri 'https://github.com/etalab-ia/codebox/releases/tag/codebox-v1.2.3'
Assert-InstallerTest ($releaseMetadata.tag_name -eq 'codebox-v1.2.3') 'latest-release URL parser returned the wrong tag'
$unexpectedReleaseUriRejected = $false
try { Get-CodeboxReleaseMetadataFromUri -Uri 'https://example.com/etalab-ia/codebox/releases/tag/codebox-v1.2.3' } catch { $unexpectedReleaseUriRejected = $true }
Assert-InstallerTest $unexpectedReleaseUriRejected 'latest-release URL parser accepted a non-GitHub URL'

$tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) "codebox installer tests $([guid]::NewGuid().ToString('N'))"
$null = New-Item -ItemType Directory -Path $tempRoot
try {
	$installDirectory = Join-Path $tempRoot 'install path with spaces'
	$null = New-Item -ItemType Directory -Path $installDirectory
	$target = Join-Path $installDirectory 'codebox.exe'
	$backup = Join-Path $installDirectory 'codebox.previous.exe'
	$hash = [System.BitConverter]::ToString([System.Security.Cryptography.SHA256]::Create().ComputeHash($global:InstallerTestBinary)).Replace('-', '').ToLowerInvariant()
	Set-InstallerTestChecksum -Hash $hash

	[System.IO.File]::WriteAllText($target, 'old binary')
	$global:InstallerTestUris = @()
	$global:InstallerSetupInvoked = $false
	Install-Codebox -InstallDirectory $installDirectory -Architecture AMD64
	Assert-InstallerTest ($global:InstallerSetupInvoked) 'setup was not invoked after installation'
	Assert-InstallerTest ([System.IO.File]::ReadAllText($target) -eq 'verified test binary') 'verified binary was not installed'
	Assert-InstallerTest ([System.IO.File]::ReadAllText($backup) -eq 'old binary') 'previous binary was not retained'
	Assert-InstallerTest ($global:InstallerTestUris.Count -eq 2) 'installer did not fetch exactly the binary and checksum'
	Assert-InstallerTest (@($global:InstallerTestUris | Where-Object { $_ -notmatch "/$global:InstallerTestTag/" }).Count -eq 0) 'binary and checksum were not pinned to the same release'

	$freshDirectory = Join-Path $tempRoot 'fresh install'
	$global:InstallerSetupInvoked = $false
	Install-Codebox -InstallDirectory $freshDirectory -Architecture AMD64
	Assert-InstallerTest ([System.IO.File]::ReadAllText((Join-Path $freshDirectory 'codebox.exe')) -eq 'verified test binary') 'fresh install did not create the executable'
	Assert-InstallerTest (-not (Test-Path -LiteralPath (Join-Path $freshDirectory 'codebox.previous.exe'))) 'fresh install created a rollback binary when none existed'
	Assert-InstallerTest $global:InstallerSetupInvoked 'fresh install did not invoke setup'

	[System.IO.File]::WriteAllText($target, 'must remain')
	Set-InstallerTestChecksum -Hash ('0' * 64)
	$global:InstallerSetupInvoked = $false
	$checksumFailed = $false
	try { Install-Codebox -InstallDirectory $installDirectory -Architecture AMD64 } catch { $checksumFailed = $true }
	Assert-InstallerTest $checksumFailed 'checksum mismatch unexpectedly succeeded'
	Assert-InstallerTest ([System.IO.File]::ReadAllText($target) -eq 'must remain') 'checksum mismatch changed the current binary'
	Assert-InstallerTest (-not $global:InstallerSetupInvoked) 'setup ran after checksum failure'

	Set-InstallerTestChecksum -Hash $hash
	$global:InstallerFailDownload = $true
	$downloadFailed = $false
	try { Install-Codebox -InstallDirectory $installDirectory -Architecture AMD64 } catch { $downloadFailed = $true }
	$global:InstallerFailDownload = $false
	Assert-InstallerTest $downloadFailed 'partial download unexpectedly succeeded'
	Assert-InstallerTest ([System.IO.File]::ReadAllText($target) -eq 'must remain') 'partial download changed the current binary'

	$blockedPath = Join-Path $tempRoot 'not a directory'
	[System.IO.File]::WriteAllText($blockedPath, 'blocking file')
	$global:InstallerReleaseCalls = 0
	$permissionFailed = $false
	try { Install-Codebox -InstallDirectory $blockedPath -Architecture AMD64 } catch { $permissionFailed = $true }
	Assert-InstallerTest $permissionFailed 'unusable installation directory unexpectedly succeeded'
	Assert-InstallerTest ($global:InstallerReleaseCalls -eq 0) 'installer contacted GitHub before confirming the destination was usable'

	$interruptedDirectory = Join-Path $tempRoot 'interrupted'
	$null = New-Item -ItemType Directory -Path $interruptedDirectory
	$interruptedTarget = Join-Path $interruptedDirectory 'codebox.exe'
	$interruptedBackup = Join-Path $interruptedDirectory 'codebox.previous.exe'
	$replaceTemp = Join-Path $interruptedDirectory 'staging'
	$null = New-Item -ItemType Directory -Path $replaceTemp
	[System.IO.File]::WriteAllText($interruptedTarget, 'old before interrupt')
	$replaceFailed = $false
	try {
		Replace-CodeboxBinary -StagedPath (Join-Path $replaceTemp 'missing.exe') -TargetPath $interruptedTarget -BackupPath $interruptedBackup
	} catch { $replaceFailed = $true }
	Assert-InstallerTest $replaceFailed 'missing staged binary unexpectedly replaced the target'
	Assert-InstallerTest ([System.IO.File]::ReadAllText($interruptedTarget) -eq 'old before interrupt') 'interrupted replacement damaged the current binary'

	$unsupportedFailed = $false
	try { Get-CodeboxAssetName -Architecture x86 } catch { $unsupportedFailed = $true }
	Assert-InstallerTest $unsupportedFailed 'unsupported Windows architecture was accepted'
	Assert-InstallerTest ((Get-CodeboxAssetName -Architecture ARM64) -eq 'codebox-windows-arm64.exe') 'Windows arm64 mapped to the wrong release asset'
	Write-Host 'Windows installer checks passed'
} finally {
	Remove-Item -LiteralPath $tempRoot -Recurse -Force
}
