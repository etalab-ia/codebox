//go:build windows

package codebox

import (
	"context"
	"fmt"
	"strings"
)

// WinCredStore stores credentials in the Windows Credential Manager via a
// PowerShell snippet that P/Invokes advapi32 (CredWriteW, CredReadW,
// CredDeleteW). There is no built-in CLI for generic credentials that can
// also read values back (`cmdkey` cannot), so the adapter ships the
// smallest P/Invoke surface that stores a generic credential and returns
// its value. The secret travels on PowerShell's stdin (the script reads
// it with -EncodedCommand from an environment-free stdin pipe), never in
// argv: process listings only show the encoded, secret-free script.
type WinCredStore struct {
	// Runner executes powershell. Defaults to OSRunner; tests inject a
	// fake.
	Runner Runner
}

func (w *WinCredStore) runner() Runner {
	if w.Runner != nil {
		return w.Runner
	}
	return OSRunner{}
}

func (w *WinCredStore) Kind() string { return "credential-manager" }

// wincredScript is the P/Invoke surface. It reads one line from stdin
// (the secret, without its newline), then performs the operation named
// in $op against the target name in $target — both set as assignments
// prepended to the script by the caller, because `powershell -Command`
// joins everything after the flag into one command string and never
// populates $args. CRED_TYPE_GENERIC=1, CRED_PERSIST_LOCAL_MACHINE=2.
// The value is written to stdout only for the "get" operation.
const wincredScript = `
$ErrorActionPreference = 'Stop'
Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public static class Cred {
  [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
  public static extern bool CredWriteW(ref CREDENTIAL cred, uint flags);
  [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
  public static extern bool CredReadW(string target, uint type, uint flags, out IntPtr credPtr);
  [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
  public static extern bool CredDeleteW(string target, uint type, uint flags);
  [DllImport("advapi32.dll")]
  public static extern void CredFree(IntPtr cred);
  [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
  public struct CREDENTIAL {
    public uint Flags; public uint Type; public string TargetName;
    public string Comment; public long LastWritten;
    public uint CredentialBlobSize; public IntPtr CredentialBlob;
    public uint Persist; public uint AttributeCount; public IntPtr Attributes;
    public string TargetAlias; public string UserName;
  }
}
"@
` + "$op = '%s'; $target = '%s'\n" + `
`

// Put stores the credential. The secret is the stdin line; the operation
// and target name travel as PowerShell arguments (both secret-free).
func (w *WinCredStore) Put(ctx context.Context, kind CredentialKind, value string) error {
	script := wincredScript + `
$secret = [Console]::In.ReadLine()
$blob = [System.Text.Encoding]::Unicode.GetBytes($secret)
$cred = New-Object Cred+CREDENTIAL
$cred.Flags = 0; $cred.Type = 1; $cred.TargetName = $target
$cred.Comment = ''; $cred.Persist = 2
$cred.AttributeCount = 0; $cred.TargetAlias = $null; $cred.UserName = $null
$cred.CredentialBlobSize = $blob.Length
$cred.CredentialBlob = [System.Runtime.InteropServices.Marshal]::AllocHGlobal($blob.Length)
[System.Runtime.InteropServices.Marshal]::Copy($blob, 0, $cred.CredentialBlob, $blob.Length)
if (-not [Cred]::CredWriteW([ref]$cred, 0)) { exit 2 }
exit 0
`
	script = fmt.Sprintf(script, "put", credentialService(kind))
	res, err := w.runner().RunStdin(ctx, strings.NewReader(value+"\n"),
		"powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	if err != nil {
		return classifyExecError("add", w.Kind(), res, err)
	}
	switch res.ExitCode {
	case 0:
		return nil
	case 2:
		return storeErrorf("add", w.Kind(), "denied", "CredWriteW failed")
	default:
		return storeErrorf("add", w.Kind(), "corrupt", "exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
}

// Get returns the stored value. CredReadW returns the blob as UTF-16
// bytes; the script decodes and prints it to stdout.
func (w *WinCredStore) Get(ctx context.Context, kind CredentialKind) (string, error) {
	script := wincredScript + `
$ptr = [IntPtr]::Zero
if (-not [Cred]::CredReadW($target, 1, 0, [ref]$ptr)) {
  $err = [System.Runtime.InteropServices.Marshal]::GetLastWin32Error()
  if ($err -eq 1168) { exit 1 }  # ERROR_NOT_FOUND
  exit 2
}
$cred = [System.Runtime.InteropServices.Marshal]::PtrToStructure($ptr, [type][Cred+CREDENTIAL])
$bytes = New-Object byte[] $cred.CredentialBlobSize
[System.Runtime.InteropServices.Marshal]::Copy($cred.CredentialBlob, $bytes, 0, $cred.CredentialBlobSize)
[Cred]::CredFree($ptr)
[System.Text.Encoding]::Unicode.GetString($bytes)
exit 0
`
	script = fmt.Sprintf(script, "get", credentialService(kind))
	res, err := w.runner().Run(ctx, "powershell", "-NoProfile", "-NonInteractive",
		"-Command", script)
	if err != nil {
		return "", classifyExecError("get", w.Kind(), res, err)
	}
	switch res.ExitCode {
	case 0:
		value := strings.TrimSuffix(res.Stdout, "\n")
		value = strings.TrimSuffix(value, "\r")
		if value == "" {
			return "", storeErrorf("get", w.Kind(), "corrupt", "empty value stored for %s", kind)
		}
		return value, nil
	case 1:
		return "", ErrCredentialNotFound
	default:
		return "", storeErrorf("get", w.Kind(), "denied", "exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
}

func (w *WinCredStore) Remove(ctx context.Context, kind CredentialKind) error {
	script := wincredScript + `
if (-not [Cred]::CredDeleteW($target, 1, 0)) {
  $err = [System.Runtime.InteropServices.Marshal]::GetLastWin32Error()
  if ($err -eq 1168) { exit 1 }  # ERROR_NOT_FOUND
  exit 2
}
exit 0
`
	script = fmt.Sprintf(script, "remove", credentialService(kind))
	res, err := w.runner().Run(ctx, "powershell", "-NoProfile", "-NonInteractive",
		"-Command", script)
	if err != nil {
		return classifyExecError("remove", w.Kind(), res, err)
	}
	switch res.ExitCode {
	case 0:
		return nil
	case 1:
		return ErrCredentialNotFound
	default:
		return storeErrorf("remove", w.Kind(), "denied", "exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
}

// Verify probes the Credential Manager with a read of a sentinel target.
// It must not create anything; ERROR_NOT_FOUND proves the store is
// reachable and answering.
func (w *WinCredStore) Verify(ctx context.Context) error {
	script := wincredScript + `
$ptr = [IntPtr]::Zero
if (-not [Cred]::CredReadW($target, 1, 0, [ref]$ptr)) {
  $err = [System.Runtime.InteropServices.Marshal]::GetLastWin32Error()
  if ($err -eq 1168) { exit 0 }  # not found: store reachable
  exit 2
}
[Cred]::CredFree($ptr)
exit 3  # sentinel unexpectedly present
`
	script = fmt.Sprintf(script, "verify", credentialService("__verify__"))
	res, err := w.runner().Run(ctx, "powershell", "-NoProfile", "-NonInteractive",
		"-Command", script)
	if err != nil {
		return classifyExecError("verify", w.Kind(), res, err)
	}
	switch res.ExitCode {
	case 0:
		return nil
	case 3:
		return fmt.Errorf("verify sentinel unexpectedly found in credential manager")
	default:
		return storeErrorf("verify", w.Kind(), "denied", "exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
}

// Generation returns no marker: the native store has no store-local
// rotation counter, so reconcile uses the host-side generation counter
// (CredentialGeneration), which `auth add` bumps on every write.
func (w *WinCredStore) Generation(ctx context.Context, kind CredentialKind) (string, error) {
	return "", nil
}
