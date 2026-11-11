#Requires -Version 5.1
[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$script:CodeboxRepository = 'etalab-ia/codebox'
$script:CodeboxReleasePage = "https://github.com/$script:CodeboxRepository/releases/latest"
$script:CodeboxDownload = "https://github.com/$script:CodeboxRepository/releases/download"

if ([System.Environment]::OSVersion.Platform -eq [System.PlatformID]::Win32NT) {
	[System.Net.ServicePointManager]::SecurityProtocol = [System.Net.ServicePointManager]::SecurityProtocol -bor [System.Net.SecurityProtocolType]::Tls12
}

function Get-CodeboxReleaseMetadataFromUri {
	param([Parameter(Mandatory = $true)][System.Uri]$Uri)

	$tagPrefix = "/$script:CodeboxRepository/releases/tag/"
	if ($Uri.Scheme -ne 'https' -or $Uri.Host -ine 'github.com' -or -not $Uri.AbsolutePath.StartsWith($tagPrefix, [System.StringComparison]::Ordinal)) {
		throw "Unexpected GitHub release URL '$Uri'."
	}
	$tag = $Uri.AbsolutePath.Substring($tagPrefix.Length)
	if (-not $tag -or $tag.Contains('/')) {
		throw "Unexpected GitHub release URL '$Uri'."
	}
	[pscustomobject]@{ tag_name = $tag }
}

function Get-CodeboxLatestRelease {
	$response = Invoke-WebRequest -Uri $script:CodeboxReleasePage -Method Head -UserAgent 'codebox-installer' -MaximumRedirection 10 -TimeoutSec 30 -UseBasicParsing -ErrorAction Stop
	$baseResponse = $response.BaseResponse
	$resolvedUri = $null
	if ($baseResponse -and $baseResponse.PSObject.Properties['ResponseUri']) {
		$resolvedUri = $baseResponse.ResponseUri
	} elseif ($baseResponse -and $baseResponse.RequestMessage) {
		$resolvedUri = $baseResponse.RequestMessage.RequestUri
	}
	if (-not $resolvedUri) {
		throw 'GitHub did not return a resolved release URL.'
	}
	Get-CodeboxReleaseMetadataFromUri -Uri $resolvedUri
}

function Get-CodeboxAssetName {
	param([Parameter(Mandatory = $true)][string]$Architecture)

	switch ($Architecture.ToUpperInvariant()) {
		{ $_ -in @('AMD64', 'X64') } { return 'codebox-windows-x64.exe' }
		'ARM64' { return 'codebox-windows-arm64.exe' }
		default { throw "Unsupported Windows architecture '$Architecture'; supported targets are x64 and arm64." }
	}
}

function Test-CodeboxWindows {
	[System.Environment]::OSVersion.Platform -eq [System.PlatformID]::Win32NT
}

function Save-CodeboxReleaseAsset {
	param(
		[Parameter(Mandatory = $true)][string]$Uri,
		[Parameter(Mandatory = $true)][string]$Path
	)

	Invoke-WebRequest -Uri $Uri -OutFile $Path -UserAgent 'codebox-installer' -MaximumRedirection 10 -TimeoutSec 300 -UseBasicParsing -ErrorAction Stop
	if (-not (Test-Path -LiteralPath $Path -PathType Leaf) -or (Get-Item -LiteralPath $Path).Length -eq 0) {
		throw "Download was empty: $Uri"
	}
}

function Invoke-CodeboxSetup {
	param([Parameter(Mandatory = $true)][string]$Path)

	& $Path setup | Out-Host
	$LASTEXITCODE
}

function Replace-CodeboxBinary {
	param(
		[Parameter(Mandatory = $true)][string]$StagedPath,
		[Parameter(Mandatory = $true)][string]$TargetPath,
		[Parameter(Mandatory = $true)][string]$BackupPath
	)

	if (Test-Path -LiteralPath $TargetPath) {
		if (Test-Path -LiteralPath $BackupPath) {
			Remove-Item -LiteralPath $BackupPath -Force
		}
		try {
			[System.IO.File]::Replace($StagedPath, $TargetPath, $BackupPath)
		} catch {
			throw "Could not atomically replace $TargetPath. Close any running codebox processes, check write access, and retry. The current executable was not replaced. Details: $($_.Exception.Message)"
		}
	} else {
		[System.IO.File]::Move($StagedPath, $TargetPath)
	}
}

function Install-Codebox {
	[CmdletBinding()]
	param(
		[string]$InstallDirectory,
		[string]$Architecture
	)

	if (-not (Test-CodeboxWindows)) {
		throw 'This installer supports Windows only; use install.sh on macOS or Linux.'
	}

	if (-not $Architecture) {
		$Architecture = $env:PROCESSOR_ARCHITEW6432
		if (-not $Architecture) { $Architecture = $env:PROCESSOR_ARCHITECTURE }
	}
	$asset = Get-CodeboxAssetName -Architecture $Architecture

	if (-not $InstallDirectory) {
		if (-not $env:LOCALAPPDATA) {
			throw 'LOCALAPPDATA is not set; cannot choose a user-owned installation directory.'
		}
		$InstallDirectory = Join-Path $env:LOCALAPPDATA 'Programs/codebox'
	}

	$InstallDirectory = [System.IO.Path]::GetFullPath($InstallDirectory)
	if (Test-Path -LiteralPath $InstallDirectory) {
		$installItem = Get-Item -LiteralPath $InstallDirectory -Force
		if (-not $installItem.PSIsContainer) {
			throw "$InstallDirectory is not a directory."
		}
	} else {
		$null = New-Item -ItemType Directory -Path $InstallDirectory -Force
	}
	$target = Join-Path $InstallDirectory 'codebox.exe'
	$backup = Join-Path $InstallDirectory 'codebox.previous.exe'
	if (Test-Path -LiteralPath $target) {
		$current = Get-Item -LiteralPath $target -Force
		if ($current.PSIsContainer -or ($current.Attributes -band [System.IO.FileAttributes]::ReparsePoint)) {
			throw "$target is not a regular file; refusing to replace it."
		}
	}

	$tempDirectory = Join-Path $InstallDirectory ".codebox-install-$([guid]::NewGuid().ToString('N'))"
	$null = New-Item -ItemType Directory -Path $tempDirectory
	try {
		$release = Get-CodeboxLatestRelease
		$tag = [string]$release.tag_name
		if ($tag -notmatch '^codebox-v?\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$') {
			throw "Unexpected GitHub release tag '$tag'."
		}

		$binaryPath = Join-Path $tempDirectory $asset
		$checksumPath = Join-Path $tempDirectory 'SHA256SUMS'
		$binaryUri = "$script:CodeboxDownload/$tag/$asset"
		$checksumUri = "$script:CodeboxDownload/$tag/SHA256SUMS"
		Save-CodeboxReleaseAsset -Uri $binaryUri -Path $binaryPath
		Save-CodeboxReleaseAsset -Uri $checksumUri -Path $checksumPath

		$checksumText = [System.IO.File]::ReadAllText($checksumPath, [System.Text.Encoding]::ASCII)
		$rows = @([regex]::Matches($checksumText, '(?m)^(?<hash>[0-9a-fA-F]{64})\s+\*?(?<name>\S+)\s*$') | Where-Object {
			$_.Groups['name'].Value -ceq $asset
		})
		if ($rows.Count -ne 1) {
			throw "SHA256SUMS from release $tag does not contain exactly one entry for $asset."
		}
		$expected = $rows[0].Groups['hash'].Value.ToLowerInvariant()
		$actual = (Get-FileHash -LiteralPath $binaryPath -Algorithm SHA256).Hash.ToLowerInvariant()
		if ($actual -cne $expected) {
			throw "Checksum mismatch for $asset; the existing installation was not changed."
		}

		Replace-CodeboxBinary -StagedPath $binaryPath -TargetPath $target -BackupPath $backup

		$setupExitCode = Invoke-CodeboxSetup -Path $target
		if ($setupExitCode -ne 0) {
			$recovery = if (Test-Path -LiteralPath $backup) {
				' Previous binary retained at codebox.previous.exe for rollback.'
			} else {
				''
			}
			throw "codebox was installed, but setup exited with status $setupExitCode.$recovery"
		}

		Write-Host "Installed $asset from release $tag at $target"
		$pathEntries = @($env:Path -split ';' | ForEach-Object { $_.TrimEnd('\') })
		if (-not ($pathEntries | Where-Object { $_ -ieq $InstallDirectory.TrimEnd('\') })) {
			Write-Host 'Add the installation directory to PATH for future shells. For this PowerShell session run:'
			$pathLiteral = $InstallDirectory.Replace("'", "''")
			Write-Host "  `$env:Path = '$pathLiteral;' + `$env:Path"
			Write-Host 'The installer does not edit user profiles or environment settings.'
		}
	} finally {
		if (Test-Path -LiteralPath $tempDirectory) {
			Remove-Item -LiteralPath $tempDirectory -Recurse -Force
		}
	}
}

if ($MyInvocation.InvocationName -ne '.') {
	try {
		Install-Codebox
	} catch {
		[Console]::Error.WriteLine("codebox installer: {0}", $_.Exception.Message)
		exit 1
	}
}
