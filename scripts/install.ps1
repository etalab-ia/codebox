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
		throw "URL de release GitHub inattendue : '$Uri'."
	}
	$tag = $Uri.AbsolutePath.Substring($tagPrefix.Length)
	if (-not $tag -or $tag.Contains('/')) {
		throw "URL de release GitHub inattendue : '$Uri'."
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
		throw "GitHub n'a pas renvoyé d'URL de release résolue."
	}
	Get-CodeboxReleaseMetadataFromUri -Uri $resolvedUri
}

function Get-CodeboxAssetName {
	param([Parameter(Mandatory = $true)][string]$Architecture)

	switch ($Architecture.ToUpperInvariant()) {
		{ $_ -in @('AMD64', 'X64') } { return 'codebox-windows-x64.exe' }
		'ARM64' { return 'codebox-windows-arm64.exe' }
		default { throw "architecture Windows non prise en charge : '$Architecture' ; architectures prises en charge : x64 et arm64." }
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
		throw "Téléchargement vide : $Uri"
	}
}

function Invoke-CodeboxSetup {
	param([Parameter(Mandatory = $true)][string]$Path)

	& $Path setup | Out-Host
	$LASTEXITCODE
}

function Test-CodeboxSetupComplete {
	param([Parameter(Mandatory = $true)][string]$Path)

	& $Path setup --check-complete | Out-Null
	$LASTEXITCODE -eq 0
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
			throw "remplacement atomique de $TargetPath impossible. Fermez les processus codebox en cours, vérifiez les droits d'écriture, puis réessayez. L'exécutable actuel n'a pas été remplacé. Détails : $($_.Exception.Message)"
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
		throw "Cet installateur ne prend en charge que Windows ; utilisez install.sh sur macOS ou Linux."
	}

	if (-not $Architecture) {
		$Architecture = $env:PROCESSOR_ARCHITEW6432
		if (-not $Architecture) { $Architecture = $env:PROCESSOR_ARCHITECTURE }
	}
	$asset = Get-CodeboxAssetName -Architecture $Architecture

	if (-not $InstallDirectory) {
		if (-not $env:LOCALAPPDATA) {
			throw "LOCALAPPDATA n'est pas définie ; impossible de choisir un répertoire d'installation propre à l'utilisateur."
		}
		$InstallDirectory = Join-Path $env:LOCALAPPDATA 'Programs/codebox'
	}

	$InstallDirectory = [System.IO.Path]::GetFullPath($InstallDirectory)
	if (Test-Path -LiteralPath $InstallDirectory) {
		$installItem = Get-Item -LiteralPath $InstallDirectory -Force
		if (-not $installItem.PSIsContainer) {
			throw "$InstallDirectory n'est pas un répertoire."
		}
	} else {
		$null = New-Item -ItemType Directory -Path $InstallDirectory -Force
	}
	$target = Join-Path $InstallDirectory 'codebox.exe'
	$backup = Join-Path $InstallDirectory 'codebox.previous.exe'
	if (Test-Path -LiteralPath $target) {
		$current = Get-Item -LiteralPath $target -Force
		if ($current.PSIsContainer -or ($current.Attributes -band [System.IO.FileAttributes]::ReparsePoint)) {
			throw "$target n'est pas un fichier ordinaire ; remplacement refusé."
		}
	}

	$tempDirectory = Join-Path $InstallDirectory ".codebox-install-$([guid]::NewGuid().ToString('N'))"
	$null = New-Item -ItemType Directory -Path $tempDirectory
	try {
		$release = Get-CodeboxLatestRelease
		$tag = [string]$release.tag_name
		if ($tag -notmatch '^codebox-v?\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$') {
			throw "Étiquette de release GitHub inattendue : '$tag'."
		}

		$binaryPath = Join-Path $tempDirectory $asset
		$checksumPath = Join-Path $tempDirectory 'SHA256SUMS'
		$binaryUri = "$script:CodeboxDownload/$tag/$asset"
		$checksumUri = "$script:CodeboxDownload/$tag/SHA256SUMS"
		# The release exists (its tag was just resolved) but an asset can be
		# missing while binaries are still being published. Name the release
		# and the retry path instead of surfacing a bare request error.
		try {
			Save-CodeboxReleaseAsset -Uri $binaryUri -Path $binaryPath
			Save-CodeboxReleaseAsset -Uri $checksumUri -Path $checksumPath
		} catch {
			throw "Téléchargement depuis la release $tag impossible : $($_.Exception.Message). Si la release vient d'être annoncée, ses binaires sont peut-être en cours de publication : réessayez dans quelques minutes. Fichiers de la release : https://github.com/$script:CodeboxRepository/releases/tag/$tag"
		}

		$checksumText = [System.IO.File]::ReadAllText($checksumPath, [System.Text.Encoding]::ASCII)
		$rows = @([regex]::Matches($checksumText, '(?m)^(?<hash>[0-9a-fA-F]{64})\s+\*?(?<name>\S+)\s*$') | Where-Object {
			$_.Groups['name'].Value -ceq $asset
		})
		if ($rows.Count -ne 1) {
			throw "SHA256SUMS de la release $tag ne contient pas exactement une entrée pour $asset."
		}
		$expected = $rows[0].Groups['hash'].Value.ToLowerInvariant()
		$actual = (Get-FileHash -LiteralPath $binaryPath -Algorithm SHA256).Hash.ToLowerInvariant()
		if ($actual -cne $expected) {
			throw "somme de contrôle non concordante pour $asset ; l'installation existante est inchangée."
		}

		$upgrading = Test-Path -LiteralPath $target -PathType Leaf
		Replace-CodeboxBinary -StagedPath $binaryPath -TargetPath $target -BackupPath $backup
		if ($upgrading) {
			Write-Host "Mise à niveau vers codebox $tag"
		}

		if (Test-CodeboxSetupComplete -Path $target) {
			Write-Host 'Configuration existante conservée. Lancez "codebox setup" pour la modifier.'
		} else {
			$setupExitCode = Invoke-CodeboxSetup -Path $target
			if ($setupExitCode -ne 0) {
				$recovery = if (Test-Path -LiteralPath $backup) {
					' Le binaire précédent est conservé dans codebox.previous.exe pour le retour arrière.'
				} else {
					''
				}
				throw "codebox est installé, mais la configuration s'est arrêtée avec le code de sortie $setupExitCode.$recovery"
			}
		}

		Write-Host "Installé $asset depuis la release $tag dans $target"
		$pathEntries = @($env:Path -split ';' | ForEach-Object { $_.TrimEnd('\') })
		if (-not ($pathEntries | Where-Object { $_ -ieq $InstallDirectory.TrimEnd('\') })) {
			Write-Host "Ajoutez le répertoire d'installation à PATH pour les prochains shells. Pour cette session PowerShell, lancez :"
			$pathLiteral = $InstallDirectory.Replace("'", "''")
			Write-Host "  `$env:Path = '$pathLiteral;' + `$env:Path"
			Write-Host "L'installateur ne modifie ni les profils utilisateur ni les paramètres d'environnement."
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
		[Console]::Error.WriteLine("installateur codebox : {0}", $_.Exception.Message)
		exit 1
	}
}
