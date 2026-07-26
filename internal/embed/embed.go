// Package embed provides Ken's pluggable embedding SPI for semantic search.
// It ships an OpenAI-compatible HTTP provider and a deterministic offline hash
// provider (for tests / air-gapped use). Embeddings are OFF unless KEN_EMBED_*
// is configured; the store computes cosine KNN in Go (brute-force), so no SQLite
// extension is required.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Embedder turns text into vectors. Implementations must return one vector per
// input, in order, each of length Dimension().
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dimension() int
	ID() string // stable model id, stored alongside each vector
}

// FromEnv builds an embedder from KEN_EMBED_* env vars, or (nil, nil) if none is
// configured (embeddings disabled).
func FromEnv() (Embedder, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("KEN_EMBED_PROVIDER"))) {
	case "", "none":
		return nil, nil
	case "hash":
		dim := envInt("KEN_EMBED_DIM", 256)
		if dim <= 0 {
			return nil, errors.New("hash embedder requires KEN_EMBED_DIM > 0")
		}
		return HashEmbedder{Dim: dim}, nil
	case "http":
		url := os.Getenv("KEN_EMBED_URL")
		model := os.Getenv("KEN_EMBED_MODEL")
		dim := envInt("KEN_EMBED_DIM", 0)
		if url == "" || model == "" || dim <= 0 {
			return nil, errors.New("http embedder requires KEN_EMBED_URL, KEN_EMBED_MODEL and KEN_EMBED_DIM")
		}
		return &HTTPEmbedder{
			url: strings.TrimRight(url, "/"), model: model, key: os.Getenv("KEN_EMBED_KEY"),
			dim: dim, client: &http.Client{Timeout: 30 * time.Second},
		}, nil
	default:
		return nil, fmt.Errorf("unknown KEN_EMBED_PROVIDER %q", os.Getenv("KEN_EMBED_PROVIDER"))
	}
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// --- HTTP: OpenAI-compatible POST <url>/embeddings {model, input} ---

type HTTPEmbedder struct {
	url, model, key string
	dim             int
	client          *http.Client
}

func (h *HTTPEmbedder) Dimension() int { return h.dim }
func (h *HTTPEmbedder) ID() string     { return "http:" + h.model }

func (h *HTTPEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": h.model, "input": texts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.key != "" {
		req.Header.Set("Authorization", "Bearer "+h.key)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("embeddings API returned status %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	res := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index >= 0 && d.Index < len(res) {
			res[d.Index] = d.Embedding
		}
	}
	for i := range res {
		if res[i] == nil {
			return nil, fmt.Errorf("embeddings API returned no vector for input %d", i)
		}
		if len(res[i]) != h.dim {
			return nil, fmt.Errorf("embedding dim %d != configured %d", len(res[i]), h.dim)
		}
	}
	return res, nil
}

// --- Hash: deterministic, offline (hashed bag-of-words, L2-normalized) ---

type HashEmbedder struct{ Dim int }

func (h HashEmbedder) Dimension() int { return h.Dim }
func (h HashEmbedder) ID() string     { return "hash-v1-" + strconv.Itoa(h.Dim) }

func (h HashEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, h.Dim)
		toks := strings.FieldsFunc(strings.ToLower(t), func(r rune) bool {
			return !('a' <= r && r <= 'z') && !('0' <= r && r <= '9')
		})
		for _, tok := range toks {
			if len(tok) < 3 {
				continue
			}
			hh := fnv.New32a()
			_, _ = hh.Write([]byte(tok))
			idx := int(hh.Sum32() % uint32(h.Dim))
			v[idx] += 1
		}
		var norm float64
		for _, x := range v {
			norm += float64(x) * float64(x)
		}
		if norm > 0 {
			n := float32(math.Sqrt(norm))
			for j := range v {
				v[j] /= n
			}
		}
		out[i] = v
	}
	return out, nil
}
