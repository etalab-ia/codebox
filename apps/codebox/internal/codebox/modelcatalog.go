package codebox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// This file implements the P10 Albert model catalogue: validate the model
// selection against the live catalogue, and cache the last-known-good list
// so a network error never erases the working model set. Token limits come
// from the catalogue entry only — a model id absent from the catalogue has
// no fabricated limits.

// albertCatalogueURL is the models endpoint of the Albert API.
const albertCatalogueURL = "https://albert.api.etalab.gouv.fr/v1/models"

// AlbertModelChoice is a model curated for the setup wizard.
type AlbertModelChoice struct {
	ID             string
	OpenCodeID     string
	Reference      string
	Name           string
	Description    string
	Context        int
	Output         int
	Default        bool
	ToolChoiceAuto bool
	Vision         bool
}

// CuratedAlbertModels is the stable setup set. Availability is determined
// separately from the live or cached Albert catalogue.
func CuratedAlbertModels() []AlbertModelChoice {
	return []AlbertModelChoice{
		{ID: "Qwen/Qwen3-Coder-30B-A3B-Instruct", OpenCodeID: "qwen3-coder", Name: "Qwen3 Coder", Description: "orienté code et tâches agentiques, contexte long", Context: 262144},
		{ID: "deepseek-ai/DeepSeek-V4-Flash-0731", OpenCodeID: "deepseek-v4-flash", Name: "DeepSeek V4 Flash", Description: "spécialisé code, raisonnement", Context: 131072, Output: 65536, Default: true, ToolChoiceAuto: true},
		{ID: "openai/gpt-oss-120b", OpenCodeID: "gpt-oss-120b", Name: "gpt-oss 120b", Description: "généraliste, pour les tâches complexes", Context: 131072},
		{ID: "google/gemma-4-31B-it", OpenCodeID: "gemma-4", Name: "Gemma 4", Description: "tâches modérées, sait lire des images", Context: 262144, Vision: true},
		{ID: "ministral-3-8b-instruct-2512", OpenCodeID: "ministral-3-8b", Name: "Ministral 3 8B", Description: "petit modèle généraliste, adapté aux titres de session", Context: 262144, Vision: true},
	}
}

// MinistralSmallModelRef is the curated lightweight model offered by project
// init. An empty project small-model setting still means "follow the main
// model"; selecting this reference is the explicit lightweight alternative.
const MinistralSmallModelRef = "albert/ministral-3-8b"

// AlbertModelConfig builds the OpenCode provider definition for a curated
// choice, mapping its stable OpenCode ID to the API's model ID.
func (m AlbertModelChoice) AlbertModelConfig() AlbertModelConfig {
	config := AlbertModelConfig{ID: m.OpenCodeID, ModelID: m.ID, Name: m.Name, Context: m.Context, Output: m.Output, Vision: m.Vision}
	if m.ToolChoiceAuto {
		config.RequestBody = map[string]any{"tool_choice": "auto"}
	}
	return config
}

// Ref is the OpenCode model reference used in settings and overlays.
func (m AlbertModelChoice) Ref() string {
	if m.Reference != "" {
		return m.Reference
	}
	return "albert/" + m.OpenCodeID
}

// AlbertCatalogueURL exposes the models endpoint for the CLI.
func AlbertCatalogueURL() string { return albertCatalogueURL }

// CatalogueModel is one model entry of the Albert catalogue.
type CatalogueModel struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	// Alias joins the entry's aliases for display.
	Alias string
	// MaxContextLength is the model's context window, when the catalogue
	// reports one. Zero means "unknown" — never fabricated.
	MaxContextLength int `json:"max_context_length"`
}

// Catalogue is a parsed models listing.
type Catalogue struct {
	Models []CatalogueModel
}

// TextGenerationModels lists the usable chat models, sorted by id. Albert
// exposes some multimodal chat models as image-text-to-text, but that type
// also covers OCR-only models. Admit only curated multimodal chat entries:
// embeddings, rerankers and OCR models are never selectable.
func (c Catalogue) TextGenerationModels() []CatalogueModel {
	curatedVision := make(map[string]bool)
	for _, choice := range CuratedAlbertModels() {
		if choice.Vision {
			curatedVision[choice.ID] = true
		}
	}
	var out []CatalogueModel
	for _, m := range c.Models {
		if m.Type == "text-generation" || (m.Type == "image-text-to-text" && curatedVision[m.ID]) {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Find resolves a model id or alias within the catalogue. The Albert
// provider prefix ("albert/") is stripped before matching, and aliases are
// matched like ids.
func (c Catalogue) Find(model string) (CatalogueModel, bool) {
	id := strings.TrimPrefix(model, "albert/")
	for _, m := range c.Models {
		if m.ID == id {
			return m, true
		}
	}
	for _, m := range c.Models {
		for _, a := range strings.Split(m.Alias, ",") {
			if a != "" && a == id {
				return m, true
			}
		}
	}
	return CatalogueModel{}, false
}

// catalogueEntryJSON mirrors the wire and cache entry format.
type catalogueEntryJSON struct {
	ID               string   `json:"id"`
	Type             string   `json:"type"`
	Aliases          []string `json:"aliases"`
	MaxContextLength int      `json:"max_context_length"`
}

type catalogueJSON struct {
	Data []catalogueEntryJSON `json:"data"`
}

// FetchCatalogue retrieves and parses the models listing. It performs no
// caching itself; the caller composes cache-then-network.
func FetchCatalogue(ctx context.Context, client *http.Client, apiKey, url string) (Catalogue, error) {
	// Same contract as the validation probe: a redirect is never followed,
	// so the Authorization header cannot be replayed to another host (the
	// host fetches this outside the secret proxy).
	client = noRedirectClient(client)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Catalogue{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return Catalogue{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Catalogue{}, fmt.Errorf("model catalogue request failed: HTTP %d", resp.StatusCode)
	}
	var raw catalogueJSON
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&raw); err != nil {
		return Catalogue{}, fmt.Errorf("parse model catalogue: %w", err)
	}
	return catalogueFromEntries(raw.Data), nil
}

func catalogueFromEntries(entries []catalogueEntryJSON) Catalogue {
	c := Catalogue{Models: make([]CatalogueModel, 0, len(entries))}
	for _, m := range entries {
		c.Models = append(c.Models, CatalogueModel{
			ID:               m.ID,
			Type:             m.Type,
			Alias:            strings.Join(m.Aliases, ","),
			MaxContextLength: m.MaxContextLength,
		})
	}
	return c
}

// CatalogueCache is the last-known-good persisted catalogue. A fetch
// failure falls back to it (stale beats broken: the model set keeps
// working); a fetch success replaces it.
type CatalogueCache struct {
	FS    FS
	Dir   string // host state dir; cache lives under <dir>/catalogue.json
	Fetch func(ctx context.Context) (Catalogue, error)
}

// catalogueCachePath is the last-known-good file location.
func catalogueCachePath(stateDir string) string {
	return filepath.Join(stateDir, "catalogue.json")
}

// cacheRecordJSON is the persisted form: the catalogue plus a fetch time.
type cacheRecordJSON struct {
	FetchedAt time.Time            `json:"fetchedAt"`
	Models    []catalogueEntryJSON `json:"models"`
}

// Load returns the effective catalogue: network first, cache on network
// failure. fresh reports whether the returned catalogue came from a
// successful fetch; the zero Catalogue with an error means neither source
// produced a usable list.
func (c CatalogueCache) Load(ctx context.Context) (catalogue Catalogue, fresh bool, err error) {
	if c.Fetch != nil {
		cat, ferr := c.Fetch(ctx)
		if ferr == nil {
			_ = c.persist(cat)
			return cat, true, nil
		}
		// Fall back to the cache; the fetch error is reported only if the
		// cache is unusable too.
		cached, cerr := c.readCache()
		if cerr == nil && len(cached.Models) > 0 {
			return cached, false, nil
		}
		return Catalogue{}, false, ferr
	}
	cached, cerr := c.readCache()
	return cached, false, cerr
}

func (c CatalogueCache) readCache() (Catalogue, error) {
	readFile := os.ReadFile
	if c.FS != nil {
		readFile = c.FS.ReadFile
	}
	data, err := readFile(catalogueCachePath(c.Dir))
	if err != nil {
		return Catalogue{}, err
	}
	var raw cacheRecordJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return Catalogue{}, fmt.Errorf("parse catalogue cache: %w", err)
	}
	return catalogueFromEntries(raw.Models), nil
}

func (c CatalogueCache) persist(cat Catalogue) error {
	if c.FS == nil {
		return nil
	}
	rec := cacheRecordJSON{FetchedAt: time.Now().UTC()}
	for _, m := range cat.Models {
		aliases := strings.Split(m.Alias, ",")
		if m.Alias == "" {
			aliases = nil
		}
		rec.Models = append(rec.Models, catalogueEntryJSON{
			ID: m.ID, Type: m.Type, Aliases: aliases, MaxContextLength: m.MaxContextLength,
		})
	}
	data, err := json.MarshalIndent(&rec, "", "  ")
	if err != nil {
		return err
	}
	if err := c.FS.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	return atomicWrite(c.FS, catalogueCachePath(c.Dir), append(data, '\n'), 0o600)
}
