package codebox

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// This file implements P09 revocation: removing a credential from the store
// must revoke the access it granted. For Microsandbox instances the proxy
// registration is dropped — live on a running sandbox (NoRestart), persisted
// for the next boot on a stopped one. Tart and agent-vm hand the plaintext
// credential to the guest, where any process may already have copied it, so
// removal while such an instance runs is refused outright: reporting success
// there would claim a revocation codebox cannot perform.

// RevokeReport describes what a revocation did, per instance.
type RevokeReport struct {
	// LiveRevoked lists running Microsandbox instances whose proxy
	// registration was dropped. Their guest environment keeps the dangling
	// placeholder until the next restart (see PlaceholderDangles).
	LiveRevoked []string
	// StoppedCleared lists stopped Microsandbox instances whose persisted
	// secret reference was removed; the next boot has no secret at all.
	StoppedCleared []string
	// SkippedEnv lists instances whose credential was environment-sourced
	// (per the persisted BoundCredentials record): codebox cannot revoke a
	// variable it does not own, so nothing was touched there.
	SkippedEnv []string
	// Revolved lists the guest bindings actually dropped, by guest variable
	// name, so the caller can name what a removal reached.
	Revolved []string
	// Pending lists instances whose binding was revoked but whose source
	// could not be confirmed with today's inputs (the credential no longer
	// resolves and no persisted store marker exists). They are revoked for
	// safety and reported rather than silently claimed as clean.
	Pending []string
	// PlaceholderDangles is set when any live revocation ran: the guest
	// environment still holds the placeholder, which travels literally to
	// the formerly allowed host (the request fails server-side, but the
	// placeholder pattern is disclosed) until the instance restarts.
	PlaceholderDangles bool
}

// ErrRevocationBlocked refuses a removal that cannot take effect: the named
// running instances hold the plaintext credential (Tart/agent-vm transport),
// so deleting the stored copy would revoke nothing.
type ErrRevocationBlocked struct {
	Kind      CredentialKind
	Instances []string
	// BoundVMs, when non-empty, names the running plaintext VMs whose
	// persisted record shows the removed entry feeding their Albert
	// binding — the evidence for the refusal, beyond the general rule.
	BoundVMs []string
}

func (e *ErrRevocationBlocked) Error() string {
	return fmt.Sprintf("credential %q is held in plaintext by running instances %v; "+
		"stop them first (codebox stop) — removing the stored credential cannot revoke a copy the guest already holds",
		e.Kind, e.Instances)
}

// Revoker carries the seams RevokeCredential needs, so tests can drive the
// flow without runtimes. The zero value is valid: production defaults are
// filled in (embedded SDK client, OS runners, default state dir).
type Revoker struct {
	MSB            msbClient
	TartRunning    func(ctx context.Context) ([]string, error)
	AgentVMRunning func(ctx context.Context) ([]string, error)
	StateDir       string
	FS             FS
	// Store names which credential store is being edited ("native", "file",
	// or "" for the normal order). Revocation targets the instances whose
	// persisted binding names that store, so editing the fallback never
	// revokes instances bound to the native entry and vice versa.
	Store string
	// Config supplies the resolution inputs (CredentialRef, legacy env) the
	// fallback detection needs: an instance whose Albert credential did not
	// resolve from a store at all must not be revoked by a store edit.
	Config Config
	// ReadCredential overrides the store reader (tests). Empty means
	// readStoredWith.
	ReadCredential credentialReader
}

func (r Revoker) withDefaults() Revoker {
	if r.MSB == nil {
		r.MSB = sdkMSBClient{}
	}
	if r.TartRunning == nil {
		t := NewTart(Config{})
		r.TartRunning = t.RunningInstances
	}
	if r.AgentVMRunning == nil {
		a := NewAgentVM(Config{})
		r.AgentVMRunning = a.RunningInstances
	}
	if r.StateDir == "" {
		r.StateDir = DefaultStateDir()
	}
	if r.FS == nil {
		r.FS = DefaultFS
	}
	return r
}

// RevokeCredential revokes the guest bindings fed by a credential-store entry
// everywhere codebox can reach, ahead of its removal from the store. entry
// is the store entry being deleted (usually a credential kind, but an Albert
// binding may resolve from a credentialRef naming something else); store names
// the store being edited ("native", "file", or "" for the normal order).
// Revocation targets only the instances bound to that entry in that store.
// See the file header for the per-runtime semantics.
// credentialRef supplies the project's explicit Albert credential reference
// when known: the plaintext-runtime guard must see that a differently-named
// entry feeds the Albert binding, or removing it would look harmless while a
// Tart/agent-vm guest still holds the readable credential.
func RevokeCredential(ctx context.Context, entry string, store, credentialRef string) (RevokeReport, error) {
	return (Revoker{Store: store, Config: Config{CredentialRef: credentialRef}}).withDefaults().Revoke(ctx, entry)
}

func (r Revoker) Revoke(ctx context.Context, entry string) (RevokeReport, error) {
	var rep RevokeReport

	sandboxes, err := r.enumerateSandboxes(ctx)
	if err != nil {
		return rep, err
	}
	// Decide the guest bindings each instance will lose before touching
	// anything, because the plaintext guard depends on them and must fail
	// closed ahead of the first removal. One entry can feed several guest
	// bindings at once (credentialRef plus an approved optional binding of
	// the same name); all of them are removed.
	var removals []pendingRemoval
	for _, sb := range sandboxes {
		verdict, guestEnvs := r.instanceEntryVerdict(ctx, entry, sb.Name)
		switch verdict {
		case bindingOther:
			rep.SkippedEnv = append(rep.SkippedEnv, sb.Name)
			continue
		case bindingPending:
			// The source could not be confirmed, so the persisted state
			// cannot be trusted to say which binding the entry fed. Proceed
			// and record the marker: a removal that cannot confirm what it
			// revoked must not silently report success.
			rep.Pending = append(rep.Pending, sb.Name)
		}
		if len(guestEnvs) == 0 {
			continue // an entry codebox never proxied: nothing to drop
		}
		for _, guestEnv := range guestEnvs {
			removals = append(removals, pendingRemoval{instance: sb.Name, guestEnv: guestEnv, live: sb.Status == "running"})
		}
	}

	// Tart and agent-vm transport the inference credentials in plaintext.
	// The guard keys on the guest binding, not the entry: an inference binding
	// fed by `credentialRef: "github"` still sits in cleartext in a running
	// VM, and removing the "github" entry would otherwise look harmless.
	//
	// Every running VM is inspected, each against its OWN persisted binding
	// record (the VM name is the instance name, so the per-instance state
	// file applies). The caller's project reference is one input, not the
	// only one: removing a global entry from project A must still see that
	// project B's VM resolved Albert from it.
	boundVMs := r.plaintextVMsFedBy(ctx, entry)
	if len(boundVMs) > 0 || r.entryFeedsInference(entry, removals) {
		var running []string
		for _, enumerate := range []func(context.Context) ([]string, error){r.TartRunning, r.AgentVMRunning} {
			vms, err := enumerate(ctx)
			if err != nil {
				if commandNotFound(err) {
					continue // runtime not installed on this host
				}
				return rep, err
			}
			running = append(running, vms...)
		}
		if len(running) > 0 {
			sort.Strings(running)
			sort.Strings(boundVMs)
			return RevokeReport{}, &ErrRevocationBlocked{Kind: CredentialKind(entry), Instances: running, BoundVMs: boundVMs}
		}
	}

	for _, rm := range removals {
		if err := r.MSB.RemoveSecrets(ctx, rm.instance, []string{rm.guestEnv}, rm.live); err != nil {
			return rep, fmt.Errorf("revoking %s from %s: %w", entry, rm.instance, err)
		}
		rep.Revolved = appendUniqueString(rep.Revolved, rm.guestEnv)
		if rm.live {
			rep.LiveRevoked = append(rep.LiveRevoked, rm.instance)
			rep.PlaceholderDangles = true
		} else {
			rep.StoppedCleared = append(rep.StoppedCleared, rm.instance)
		}
	}
	return rep, nil
}

// enumerateSandboxes lists every Microsandbox sandbox revocation must reach:
// managed instances plus the legacy singleton (which predates the ownership
// label and is invisible to List).
func (r Revoker) enumerateSandboxes(ctx context.Context) ([]msbSandboxInfo, error) {
	seen := map[string]bool{}
	var out []msbSandboxInfo
	sandboxes, err := r.MSB.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, sb := range sandboxes {
		seen[sb.Name] = true
		out = append(out, sb)
	}
	if legacy, exists, err := r.MSB.Lookup(ctx, msbSandbox); err != nil {
		return nil, err
	} else if exists && !seen[legacy.Name] {
		out = append(out, legacy)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// bindingVerdict classifies whether an instance's binding of a kind should be
// revoked by an edit of the store being removed from.
type bindingVerdict int

const (
	// bindingThisStore: the credential came from the store being edited (or
	// that cannot be ruled out) — revoke it.
	bindingThisStore bindingVerdict = iota
	// bindingOther: the credential demonstrably came from somewhere else
	// (the environment, or the other store) — leave it alone.
	bindingOther
	// bindingPending: it cannot be determined — revoke, and report.
	bindingPending
)

// instanceEntryVerdict decides the revocation verdict and guest binding for
// one instance, given the store entry being removed.
//
// The persisted BoundCredentials record is the authority. When it names the
// entry, it also names the guest binding that entry fed, which is what
// revocation must drop. When every record is the original bare form the store
// is unknown, so the entry is revoked (safe direction) by its own name. When
// no record mentions the entry at all — state written before entry tracking,
// or lost — the verdict falls back to re-resolving the credential: an env
// key still resolves, which proves the instance did not come from the store.
// Any resolution error then means the question cannot be answered, so the
// entry is revoked as pending rather than skipped on a guess.
func (r Revoker) instanceEntryVerdict(ctx context.Context, entry, instance string) (bindingVerdict, []string) {
	st, err := ReadInstanceState(r.FS, instanceStatePath(r.StateDir, instance))
	if err != nil || st == nil {
		return bindingPending, guestEnvsForEntry(entry)
	}
	verdict, bindings, known := entryVerdicts(st.BoundCredentials, entry, r.Store)
	if known {
		var envs []string
		for _, binding := range bindings {
			if env := guestEnvForBinding(binding); env != "" {
				envs = append(envs, env)
			}
		}
		return verdict, envs
	}
	// No record names the entry: either the credential came from elsewhere
	// (its env source still resolves, or a credentialRef points at another
	// entry), or the record predates entry tracking. Re-resolve to tell them
	// apart.
	read := r.ReadCredential
	if read == nil {
		read = readStoredWith
	}
	_, _, _, resolvedEntry, rerr := resolveAlbertWith(read, ctx, r.Config, r.Store)
	if rerr == nil && resolvedEntry != entry {
		return bindingOther, nil
	}
	return bindingPending, guestEnvsForEntry(entry)
}

// pendingRemoval is one queued revocation: the instance, the guest binding it
// will lose, and whether it is running.
type pendingRemoval struct {
	instance string
	guestEnv string
	live     bool
}

// entryFeedsInference reports whether removing the entry threatens an
// inference-platform credential: either the entry names a provider binding
// itself, or a bound
// instance's record shows the entry resolving into it (the credentialRef
// case). Without the second test, removing a non-"albert" entry that feeds the
// Albert binding would bypass the plaintext-runtime guard.
func (r Revoker) entryFeedsInference(entry string, removals []pendingRemoval) bool {
	if b, ok := bindingForEntry(CredentialKind(entry)); ok && b.Provider {
		return true
	}
	// The project's own credentialRef naming this entry is direct evidence
	// that the entry feeds the Albert binding, even before any instance
	// records it.
	if ref := strings.TrimSpace(r.Config.CredentialRef); ref != "" && ref == entry {
		return true
	}
	for _, rm := range removals {
		if isProviderGuestEnv(rm.guestEnv) {
			return true
		}
	}
	return false
}

// isProviderGuestEnv reports whether a guest variable carries an inference
// platform key.
func isProviderGuestEnv(env string) bool {
	for _, providerEnv := range AllProviderAPIKeyEnvs() {
		if env == providerEnv {
			return true
		}
	}
	return false
}

// plaintextVMsFedBy inspects every running Tart/agent-vm VM's persisted
// binding record for the entry being removed. The VM name is the instance
// name (ManagedVMName), so the per-instance state file applies to VMs the
// same way it applies to Microsandbox sandboxes. A VM whose record shows the
// entry feeding its Albert binding is named; a VM with no record at all is
// not (the general rule already refuses while any VM runs, so no record
// means "evidence unknown", which the blanket refusal covers).
func (r Revoker) plaintextVMsFedBy(ctx context.Context, entry string) []string {
	var bound []string
	for _, enumerate := range []func(context.Context) ([]string, error){r.TartRunning, r.AgentVMRunning} {
		vms, err := enumerate(ctx)
		if err != nil || len(vms) == 0 {
			continue
		}
		for _, vm := range vms {
			instance := strings.TrimPrefix(vm, managedVMPrefix)
			st, err := ReadInstanceState(r.FS, instanceStatePath(r.StateDir, instance))
			if err != nil || st == nil {
				continue
			}
			verdict, bindings, _ := entryVerdicts(st.BoundCredentials, entry, r.Store)
			if verdict != bindingThisStore {
				continue
			}
			for _, b := range bindings {
				if isProviderCredential(b) {
					bound = append(bound, vm)
					break
				}
			}
		}
	}
	return bound
}

func isProviderCredential(kind CredentialKind) bool {
	b, ok := bindingForKind(kind)
	return ok && b.Provider
}

// guestEnvForEntry returns the guest variable a credential-store entry feeds:
// the registered binding of the same name, or "" when the entry names
// something codebox never proxied.
func guestEnvForEntry(entry string) string {
	b, ok := bindingForEntry(CredentialKind(entry))
	if !ok {
		return ""
	}
	return b.GuestEnv
}

// guestEnvsForEntry is the multi-binding form of guestEnvForEntry: every
// registered binding an entry could feed. With no persisted record the
// evidence is the entry's own name only: the binding named by the entry, plus
// the Albert binding when the entry names a registered optional kind — a
// credentialRef could have fed Albert from it. An entry that names nothing
// codebox proxies (no registered binding, not optional) feeds nothing, so
// nothing is dropped: deleting it from the store is the whole revocation.
func guestEnvsForEntry(entry string) []string {
	var envs []string
	if env := guestEnvForEntry(entry); env != "" {
		envs = append(envs, env)
	}
	b, known := bindingForEntry(CredentialKind(entry))
	if known && b.Optional {
		// A credentialRef could have fed the Albert binding from this entry.
		// An unproxied entry (nothing registered under this name) feeds
		// nothing and must not inherit the Albert binding by guessing.
		if env := guestEnvForBinding(CredentialAlbert); env != "" && !containsStr(envs, env) {
			envs = append(envs, env)
		}
	}
	return envs
}

// guestEnvForBinding returns the guest variable of a guest binding kind.
func guestEnvForBinding(kind CredentialKind) string {
	b, ok := bindingForKind(kind)
	if !ok {
		return ""
	}
	return b.GuestEnv
}

// bindingForEntry returns the binding a credential-store entry feeds when the
// entry names a registered kind. A credentialRef can point the Albert binding
// at a differently-named entry, which this cannot see; the persisted record is
// what carries that case.
func bindingForEntry(kind CredentialKind) (msbSecretBinding, bool) {
	return bindingForKind(kind)
}

func appendUniqueString(list []string, value string) []string {
	for _, v := range list {
		if v == value {
			return list
		}
	}
	return append(list, value)
}

func containsStr(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

// BoundStoreMarkers returns the persisted "kind@store" markers of an
// instance, for tests and diagnostics.
func BoundStoreMarkers(fs FS, stateDir, instance string) []string {
	st, err := ReadInstanceState(fs, instanceStatePath(stateDir, instance))
	if err != nil || st == nil {
		return nil
	}
	return st.BoundCredentials
}

// RevokeInstanceBinding drops kind's proxy binding from a single Microsandbox
// instance: live when it is running, persisted for the next boot when it is
// stopped. It backs `bindings revoke`, which is project-scoped. revoked is
// false (with nil error) when the instance does not exist. A lookup failure
// (control plane unreachable) is returned as a LookupFailed error so callers
// can distinguish "cannot verify" from "verified absent": the distinction
// matters because a stale registration in a preserved sandbox is inert once
// the host approval is revoked, and the next reconcile strips it anyway
// (absence is authoritative in the binding refresh).
func RevokeInstanceBinding(ctx context.Context, instance string, kind CredentialKind) (revoked bool, live bool, err error) {
	binding, ok := bindingForKind(kind)
	if !ok {
		return false, false, fmt.Errorf("unknown credential kind %q", kind)
	}
	client := sdkMSBClient{}
	sb, exists, err := client.Lookup(ctx, instance)
	if err != nil {
		return false, false, fmt.Errorf("%w: %v", ErrLookupFailed, err)
	}
	if !exists {
		return false, false, nil
	}
	live = sb.Status == "running"
	if err := client.RemoveSecrets(ctx, instance, []string{binding.GuestEnv}, live); err != nil {
		return false, false, fmt.Errorf("revoking %s from %s: %w", kind, instance, err)
	}
	return true, live, nil
}

// ErrLookupFailed marks a revoke that could not determine whether the
// instance exists (Microsandbox control plane unreachable). It is not a
// verdict on the registration: callers treat it as "cannot verify" and
// decide whether to proceed.
var ErrLookupFailed = errors.New("microsandbox lookup failed")
