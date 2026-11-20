package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
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
	codeboxServerBaseURLFn = codebox.CodeboxServerURL
	codeboxServerStoreFn   = func() (codebox.CredentialStore, error) {
		store := codebox.DefaultCredentialStore()
		if store == nil {
			return nil, fmt.Errorf("aucun trousseau natif n'est disponible sur ce poste")
		}
		return store, nil
	}
	codeboxServerOpenURLFn           = openBrowserURL
	codeboxServerHTTPFn              = func() *http.Client { return &http.Client{Timeout: 30 * time.Second} }
	codeboxServerClientFn            = buildGatewayClient
	codeboxServerLoginOut  io.Writer = os.Stdout
	codeboxServerLoginIn   io.Reader = os.Stdin
)

// ErrCodeboxServerNotConfigured means the CLI has no gateway URL. The local
// secret workflow remains available; this is not an error for projects that
// do not declare a remote source.
var ErrCodeboxServerNotConfigured = errors.New("aucun serveur codebox configuré ; définissez CODEBOX_SERVER_URL ou utilisez une version publiée avec un point d'accès passerelle")

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
		return 2, fmt.Errorf("commande inconnue de secrets %s", args[0])
	}
}

func printSecretsRemoteUsage() {
	fmt.Println("Usage : codebox secrets login [--terminal] | logout [--terminal] | check | init | push | members <list|add|remove>")
	fmt.Println("Les secrets de développement partagés sont protégés par ProConnect et stockés par le codebox-server.")
	fmt.Println("Ce flux ne prend en charge aucun secret de production.")
}

func gatewayBaseURL() (string, error) {
	base, err := codeboxServerBaseURLFn()
	if err != nil {
		return "", err
	}
	if base == "" {
		return "", ErrCodeboxServerNotConfigured
	}
	return base, nil
}

func gatewaySessionToken() (string, error) {
	store, err := codeboxServerStoreFn()
	if err != nil {
		return "", err
	}
	token, err := store.Get(context.Background(), codebox.CredentialCodeboxServer)
	if err != nil {
		return "", fmt.Errorf("aucune session de passerelle ; lancez codebox secrets login : %w", err)
	}
	return token, nil
}

func gatewayClient() (*gatewayclient.Client, error) {
	return codeboxServerClientFn()
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
	return gatewayclient.New(base, token, codeboxServerHTTPFn())
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
			return 2, fmt.Errorf("option inconnue de secrets login %q", arg)
		}
	}
	base, err := gatewayBaseURL()
	if err != nil {
		return 1, err
	}
	store, err := codeboxServerStoreFn()
	if err != nil {
		return 1, err
	}
	urlBuilder := &gatewayclient.Client{}
	if terminal {
		loginURL := urlBuilder.TerminalLoginURLWithBase(base)
		fmt.Fprintf(codeboxServerLoginOut, "Ouvrez cette URL, terminez la connexion ProConnect, puis collez le code à usage unique :\n%s\n> ", loginURL)
		var code string
		if _, err := fmt.Fscan(codeboxServerLoginIn, &code); err != nil {
			return 1, fmt.Errorf("lecture du code de connexion : %w", err)
		}
		tokenResp, err := gatewayclient.ExchangeTicket(context.Background(), base, strings.TrimSpace(code), codeboxServerHTTPFn())
		if err != nil {
			return 1, err
		}
		if err := store.Put(context.Background(), codebox.CredentialCodeboxServer, tokenResp.Token); err != nil {
			return 1, fmt.Errorf("enregistrement de la session de passerelle : %w", err)
		}
		if tokenResp.IDToken != "" {
			if err := store.Put(context.Background(), codebox.CredentialCodeboxServerIDToken, tokenResp.IDToken); err != nil {
				return 1, fmt.Errorf("enregistrement du jeton ProConnect pour la déconnexion : %w", err)
			}
		}
		fmt.Fprintf(codeboxServerLoginOut, "Connecté en tant que %s (expire le %s).\n", tokenResp.Identity.Email, tokenResp.ExpiresAt.Format(time.RFC3339))
		return 0, nil
	}

	callbackState, err := newLoopbackLoginState()
	if err != nil {
		return 1, fmt.Errorf("création de l'état de connexion en boucle locale : %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 1, fmt.Errorf("ouverture de l'écouteur en boucle locale : %w", err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port
	loginURL := urlBuilder.LoginURLWithBase(base, port, callbackState)
	fmt.Fprintf(codeboxServerLoginOut, "Ouverture de ProConnect dans votre navigateur : %s\n", loginURL)
	if err := codeboxServerOpenURLFn(loginURL); err != nil {
		return 1, fmt.Errorf("ouverture d'un navigateur : %w", err)
	}

	type result struct {
		token   string
		idToken string
		err     error
	}
	ch := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(callbackState)) != 1 {
			// An unbound callback must not consume the result channel: the genuine
			// browser callback still needs to complete this login attempt.
			http.Error(w, "échec de la connexion : état invalide", http.StatusForbidden)
			return
		}
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "échec de la connexion : aucun jeton", http.StatusBadRequest)
			ch <- result{err: errors.New("le rappel de la passerelle n'a porté aucun jeton")}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><meta charset=\"utf-8\"><title>codebox</title><p>Connexion terminée. Fermez cet onglet et revenez au terminal.</p>"))
		ch <- result{token: token, idToken: r.URL.Query().Get("id_token")}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	select {
	case res := <-ch:
		if res.err != nil {
			return 1, res.err
		}
		if err := store.Put(context.Background(), codebox.CredentialCodeboxServer, res.token); err != nil {
			return 1, fmt.Errorf("enregistrement de la session de passerelle : %w", err)
		}
		if res.idToken != "" {
			if err := store.Put(context.Background(), codebox.CredentialCodeboxServerIDToken, res.idToken); err != nil {
				return 1, fmt.Errorf("enregistrement du jeton ProConnect pour la déconnexion : %w", err)
			}
		}
		fmt.Fprintln(codeboxServerLoginOut, "Connecté. La session est enregistrée dans le trousseau du système.")
		return 0, nil
	case <-time.After(5 * time.Minute):
		return 1, errors.New("délai dépassé pour la connexion ProConnect ; relancez codebox secrets login")
	}
}

func secretsLogoutCmd(args []string) (int, error) {
	terminal := false
	for _, arg := range args {
		switch arg {
		case "--terminal":
			terminal = true
		case "--help", "-h":
			printSecretsRemoteUsage()
			return 0, nil
		default:
			return 2, fmt.Errorf("option inconnue de secrets logout %q", arg)
		}
	}
	store, err := codeboxServerStoreFn()
	if err != nil {
		return 1, err
	}
	ctx := context.Background()
	session, sessionErr := store.Get(ctx, codebox.CredentialCodeboxServer)
	idToken, idTokenErr := store.Get(ctx, codebox.CredentialCodeboxServerIDToken)
	var logoutURL string
	var upstreamErr error
	if sessionErr == nil && session != "" && idTokenErr == nil && idToken != "" {
		client, err := gatewayClient()
		if err != nil {
			upstreamErr = err
		} else {
			logoutURL, upstreamErr = client.LogoutURL(ctx, idToken)
		}
	}

	// Local logout is never held hostage by discovery, ProConnect availability or
	// browser behavior. Remove both host credentials before opening the browser.
	for _, kind := range []codebox.CredentialKind{codebox.CredentialCodeboxServer, codebox.CredentialCodeboxServerIDToken} {
		if err := store.Remove(ctx, kind); err != nil && !errors.Is(err, codebox.ErrCredentialNotFound) {
			return 1, fmt.Errorf("suppression de l'identifiant local %s : %w", kind, err)
		}
	}
	fmt.Fprintln(codeboxServerLoginOut, "Déconnecté. Les identifiants de la passerelle ont été supprimés du trousseau du système.")
	if upstreamErr != nil {
		fmt.Fprintln(codeboxServerLoginOut, "La déconnexion ProConnect n'a pas pu être démarrée ; la session locale a néanmoins été supprimée.")
		return 0, nil
	}
	if logoutURL == "" {
		if sessionErr == nil && idTokenErr != nil {
			fmt.Fprintln(codeboxServerLoginOut, "Cette session antérieure ne contenait aucun jeton ProConnect pour la déconnexion distante.")
		}
		return 0, nil
	}
	if terminal {
		fmt.Fprintf(codeboxServerLoginOut, "Ouvrez cette URL pour terminer la déconnexion ProConnect :\n%s\n", logoutURL)
		return 0, nil
	}
	fmt.Fprintln(codeboxServerLoginOut, "Ouverture de la déconnexion ProConnect dans votre navigateur.")
	if err := codeboxServerOpenURLFn(logoutURL); err != nil {
		return 1, fmt.Errorf("ouverture d'un navigateur (la session locale est déjà supprimée) : %w", err)
	}
	return 0, nil
}

func secretsCheckCmd(args []string) (int, error) {
	if len(args) != 0 {
		return 2, fmt.Errorf("secrets check n'accepte aucun argument")
	}
	client, err := gatewayClient()
	if err != nil {
		return 1, err
	}
	who, err := client.WhoAmI(context.Background())
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(codeboxServerLoginOut, "Session de passerelle : %s (ACR %s, expire le %s).\n", who.Email, who.ACR, who.ExpiresAt.Format(time.RFC3339))
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

func newLoopbackLoginState() (string, error) {
	state := make([]byte, 32)
	if _, err := rand.Read(state); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(state), nil
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
		return "", errors.New("ce projet n'a aucune déclaration de secrets partagés ; lancez codebox secrets init")
	}
	return manifest.Secrets.ProjectID, nil
}
