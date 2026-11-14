package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
	"github.com/etalab-ia/codebox/apps/codebox/internal/gatewayclient"
)

// Seams for the interactive login flow. Tests replace these and never open a
// browser or bind a real loopback port unless they explicitly ask for one.
var (
	secretsGatewayBaseURLFn = codebox.SecretsGatewayURL
	secretsGatewayStoreFn   = func() (codebox.CredentialStore, error) {
		store := codebox.DefaultCredentialStore()
		if store == nil {
			return nil, fmt.Errorf("no native credential store is available on this host")
		}
		return store, nil
	}
	secretsGatewayOpenURLFn           = openBrowserURL
	secretsGatewayHTTPFn              = func() *http.Client { return &http.Client{Timeout: 30 * time.Second} }
	secretsGatewayClientFn            = buildGatewayClient
	secretsGatewayLoginOut  io.Writer = os.Stdout
	secretsGatewayLoginIn   io.Reader = os.Stdin
)

// ErrSecretsGatewayNotConfigured means the CLI has no gateway URL. The local
// secret workflow remains available; this is not an error for projects that
// do not declare a remote source.
var ErrSecretsGatewayNotConfigured = errors.New("no secrets gateway configured; set CODEBOX_SECRETS_GATEWAY_URL or use a release build with a gateway endpoint")

func secretsRemoteCmd(args []string) (int, error) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printSecretsRemoteUsage()
		return 0, nil
	}
	switch args[0] {
	case "login":
		return secretsLoginCmd(args[1:])
	case "logout":
		return secretsLogoutCmd(args[1:])
	case "check":
		return secretsCheckCmd(args[1:])
	case "init":
		return secretsInitCmd(args[1:])
	case "push":
		return secretsPushCmd(args[1:])
	case "members":
		return secretsMembersCmd(args[1:])
	default:
		return 2, fmt.Errorf("unknown secrets %s command", args[0])
	}
}

func printSecretsRemoteUsage() {
	fmt.Println("Usage: codebox secrets login [--terminal] | logout | check | init | push | members <list|add|remove>")
	fmt.Println("Shared development secrets are gated by ProConnect and stored by the codebox secrets-gateway.")
	fmt.Println("No production secret is supported by this workflow.")
}

func gatewayBaseURL() (string, error) {
	base, err := secretsGatewayBaseURLFn()
	if err != nil {
		return "", err
	}
	if base == "" {
		return "", ErrSecretsGatewayNotConfigured
	}
	return base, nil
}

func gatewaySessionToken() (string, error) {
	store, err := secretsGatewayStoreFn()
	if err != nil {
		return "", err
	}
	token, err := store.Get(context.Background(), codebox.CredentialSecretsGateway)
	if err != nil {
		return "", fmt.Errorf("no gateway session; run codebox secrets login: %w", err)
	}
	return token, nil
}

func gatewayClient() (*gatewayclient.Client, error) {
	return secretsGatewayClientFn()
}

func buildGatewayClient() (*gatewayclient.Client, error) {
	base, err := gatewayBaseURL()
	if err != nil {
		return nil, err
	}
	token, err := gatewaySessionToken()
	if err != nil {
		return nil, err
	}
	return gatewayclient.New(base, token, secretsGatewayHTTPFn())
}

func secretsLoginCmd(args []string) (int, error) {
	terminal := false
	for _, arg := range args {
		switch arg {
		case "--terminal":
			terminal = true
		case "--help", "-h":
			printSecretsRemoteUsage()
			return 0, nil
		default:
			return 2, fmt.Errorf("unknown secrets login option %q", arg)
		}
	}
	base, err := gatewayBaseURL()
	if err != nil {
		return 1, err
	}
	store, err := secretsGatewayStoreFn()
	if err != nil {
		return 1, err
	}
	urlBuilder := &gatewayclient.Client{}
	if terminal {
		loginURL := urlBuilder.TerminalLoginURLWithBase(base)
		fmt.Fprintf(secretsGatewayLoginOut, "Open this URL, complete the ProConnect login, then paste the one-time code:\n%s\n> ", loginURL)
		var code string
		if _, err := fmt.Fscan(secretsGatewayLoginIn, &code); err != nil {
			return 1, fmt.Errorf("read login code: %w", err)
		}
		tokenResp, err := gatewayclient.ExchangeTicket(context.Background(), base, strings.TrimSpace(code), secretsGatewayHTTPFn())
		if err != nil {
			return 1, err
		}
		if err := store.Put(context.Background(), codebox.CredentialSecretsGateway, tokenResp.Token); err != nil {
			return 1, fmt.Errorf("store gateway session: %w", err)
		}
		fmt.Fprintf(secretsGatewayLoginOut, "Logged in as %s (expires %s).\n", tokenResp.Identity.Email, tokenResp.ExpiresAt.Format(time.RFC3339))
		return 0, nil
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 1, fmt.Errorf("open loopback listener: %w", err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port
	loginURL := urlBuilder.LoginURLWithBase(base, port)
	fmt.Fprintf(secretsGatewayLoginOut, "Opening ProConnect in your browser: %s\n", loginURL)
	if err := secretsGatewayOpenURLFn(loginURL); err != nil {
		return 1, fmt.Errorf("open a browser: %w", err)
	}

	type result struct {
		token string
		err   error
	}
	ch := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			ch <- result{err: errors.New("the gateway callback carried no token")}
			http.Error(w, "login failed: no token", http.StatusBadRequest)
			return
		}
		ch <- result{token: token}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><meta charset=\"utf-8\"><title>codebox</title><p>Connexion terminée. Fermez cet onglet et revenez au terminal.</p>"))
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Close() }()

	select {
	case res := <-ch:
		if res.err != nil {
			return 1, res.err
		}
		if err := store.Put(context.Background(), codebox.CredentialSecretsGateway, res.token); err != nil {
			return 1, fmt.Errorf("store gateway session: %w", err)
		}
		fmt.Fprintln(secretsGatewayLoginOut, "Logged in. The session is stored in the OS credential store.")
		return 0, nil
	case <-time.After(5 * time.Minute):
		return 1, errors.New("ProConnect login timed out; run codebox secrets login again")
	}
}

func secretsLogoutCmd(args []string) (int, error) {
	if len(args) != 0 {
		return 2, fmt.Errorf("secrets logout takes no arguments")
	}
	store, err := secretsGatewayStoreFn()
	if err != nil {
		return 1, err
	}
	err = store.Remove(context.Background(), codebox.CredentialSecretsGateway)
	if err != nil && !errors.Is(err, codebox.ErrCredentialNotFound) {
		return 1, fmt.Errorf("remove gateway session: %w", err)
	}
	fmt.Fprintln(secretsGatewayLoginOut, "Logged out. The gateway session was removed from the OS credential store.")
	return 0, nil
}

func secretsCheckCmd(args []string) (int, error) {
	if len(args) != 0 {
		return 2, fmt.Errorf("secrets check takes no arguments")
	}
	client, err := gatewayClient()
	if err != nil {
		return 1, err
	}
	who, err := client.WhoAmI(context.Background())
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(secretsGatewayLoginOut, "Gateway session: %s (ACR %s, expires %s).\n", who.Email, who.ACR, who.ExpiresAt.Format(time.RFC3339))
	return 0, nil
}

func openBrowserURL(raw string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", raw)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", raw)
	default:
		cmd = exec.Command("xdg-open", raw)
	}
	return cmd.Start()
}

func projectSecretsManifest(root string) (*codebox.ProjectManifest, error) {
	path := codebox.ProjectManifestPath(root)
	manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, path)
	if err != nil {
		return nil, err
	}
	return &manifest, nil
}

func requiredProjectID(root string) (string, error) {
	manifest, err := projectSecretsManifest(root)
	if err != nil {
		return "", err
	}
	if manifest.Secrets == nil || manifest.Secrets.ProjectID == "" {
		return "", errors.New("this project has no shared secrets declaration; run codebox secrets init")
	}
	return manifest.Secrets.ProjectID, nil
}
