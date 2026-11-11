package codebox

import (
	"context"
	"testing"
)

func catalogueFixture() Catalogue {
	return catalogueFromEntries([]catalogueEntryJSON{
		{ID: "gpt-oss-120b", Type: "text-generation", MaxContextLength: 131072, Aliases: []string{"openai/gpt-oss-120b"}},
		{ID: "deepseek-v4-flash-0731", Type: "text-generation", MaxContextLength: 131072, Aliases: []string{"deepseek-ai/DeepSeek-V4-Flash-0731", "deepseek-v4-flash"}},
		{ID: "bge-m3", Type: "text-embeddings-inference", MaxContextLength: 8192},
		{ID: "ministral-3-8b-instruct-2512", Type: "image-text-to-text", MaxContextLength: 262144, Aliases: []string{"mistralai/Ministral-3-8B-Instruct-2512", "openweight-small"}},
		{ID: "lightonocr-2-1b", Type: "image-text-to-text", MaxContextLength: 16384, Aliases: []string{"openweight-ocr"}},
		{ID: "mistral-medium-3-5", Type: "image-text-to-text", MaxContextLength: 262144},
	})
}

func TestCatalogueFindAndFilter(t *testing.T) {
	c := catalogueFixture()
	models := c.TextGenerationModels()
	if len(models) != 3 {
		t.Fatalf("chat models = %v", models)
	}
	if models[2].ID != "ministral-3-8b-instruct-2512" {
		t.Fatalf("Ministral must be a selectable multimodal chat model: %v", models)
	}
	for _, model := range models {
		if model.ID == "lightonocr-2-1b" || model.ID == "mistral-medium-3-5" {
			t.Fatalf("non-curated multimodal model leaked into chat choices: %v", model)
		}
	}
	// Provider-prefixed ids resolve.
	if m, ok := c.Find("albert/deepseek-v4-flash-0731"); !ok || m.ID != "deepseek-v4-flash-0731" {
		t.Fatalf("find by id = %+v", m)
	}
	// Aliases resolve like ids.
	if m, ok := c.Find("deepseek-v4-flash"); !ok || m.MaxContextLength != 131072 {
		t.Fatalf("find by alias = %+v", m)
	}
	// An absent model is absent — no fabricated entry.
	if _, ok := c.Find("albert/gpt-5-turbo"); ok {
		t.Fatal("an unknown model must not resolve")
	}
}

func TestCuratedAlbertModelDefaultsAndIDs(t *testing.T) {
	models := CuratedAlbertModels()
	if len(models) != 5 {
		t.Fatalf("curated model count = %d, want 5", len(models))
	}
	if models[4].Ref() != MinistralSmallModelRef || models[4].ID != "ministral-3-8b-instruct-2512" || models[4].Default {
		t.Fatalf("Ministral small-model choice = %+v", models[4])
	}
	var defaults []string
	for _, model := range models {
		if model.Default {
			defaults = append(defaults, model.Name)
		}
	}
	if len(defaults) != 1 || defaults[0] != "DeepSeek V4 Flash" {
		t.Fatalf("default models = %v", defaults)
	}
	if models[1].AlbertModelConfig().RequestBody["tool_choice"] != "auto" {
		t.Fatal("DeepSeek config must set tool_choice=auto")
	}
}

func TestCatalogueCacheNetworkFirst(t *testing.T) {
	dir := t.TempDir()
	fetched := 0
	cache := CatalogueCache{
		FS:  DefaultFS,
		Dir: dir,
		Fetch: func(context.Context) (Catalogue, error) {
			fetched++
			return catalogueFixture(), nil
		},
	}
	cat, fresh, err := cache.Load(context.Background())
	if err != nil || !fresh || len(cat.Models) != 6 {
		t.Fatalf("load = %+v, fresh=%v, err=%v", cat, fresh, err)
	}
	// The cache was persisted: a Load without a fetch reads it.
	cache.Fetch = nil
	cat, fresh, err = cache.Load(context.Background())
	if err != nil || fresh || len(cat.TextGenerationModels()) != 3 {
		t.Fatalf("cache read = %+v, fresh=%v, err=%v", cat, fresh, err)
	}
}

func TestCatalogueCacheFallsBackWhenNetworkFails(t *testing.T) {
	dir := t.TempDir()
	// Seed the cache with a successful fetch.
	seed := CatalogueCache{FS: DefaultFS, Dir: dir, Fetch: func(context.Context) (Catalogue, error) {
		return catalogueFixture(), nil
	}}
	if _, _, err := seed.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A failing network falls back to the last-known-good list.
	cache := CatalogueCache{FS: DefaultFS, Dir: dir, Fetch: func(context.Context) (Catalogue, error) {
		return Catalogue{}, context.DeadlineExceeded
	}}
	cat, fresh, err := cache.Load(context.Background())
	if err != nil || fresh || len(cat.TextGenerationModels()) != 3 {
		t.Fatalf("network failure must fall back to the cache: %+v, fresh=%v, err=%v", cat, fresh, err)
	}
	// Without a cache, the fetch error surfaces.
	empty := CatalogueCache{FS: DefaultFS, Dir: t.TempDir(), Fetch: func(context.Context) (Catalogue, error) {
		return Catalogue{}, context.DeadlineExceeded
	}}
	if _, _, err := empty.Load(context.Background()); err == nil {
		t.Fatal("no cache and no network must be an error, not an empty catalogue")
	}
}
