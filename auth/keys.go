package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/steviebps/realm/pkg/storage"
)

// TokenPrefix begins every API key token, so leaked keys are easy to spot
// with secret scanners.
const TokenPrefix = "realm_"

// keysKey is where API keys are stored, beneath realm's reserved metadata
// prefix. Only hashes of the secrets are kept.
const keysKey = "/_realm/keys/"

// keyCacheTTL bounds how long a key revoked by another process (such as the
// `realm keys` CLI) keeps working in a running server.
const keyCacheTTL = 30 * time.Second

// keyRefreshInterval limits how often unknown key IDs trigger a reload, so
// guessed tokens cannot turn into a storage read per request.
const keyRefreshInterval = 5 * time.Second

// ErrInvalidKey is returned for a token that is malformed, unknown, or revoked.
var ErrInvalidKey = errors.New("invalid API key")

// Key is an API key. Its secret is shown once, when it is created.
type Key struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Access    Access    `json:"access"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"createdAt"`
	CreatedBy string    `json:"createdBy"`
	// Hash is the SHA-256 of the key's secret. It is never returned by the API.
	Hash string `json:"hash,omitempty"`
}

// Principal returns the principal a request authenticated by k acts as.
func (k *Key) Principal() *Principal {
	return &Principal{Kind: KindKey, ID: k.ID, Name: k.Name, Access: k.Access, Path: k.Path}
}

// KeyStore creates, lists, revokes, and verifies API keys kept in storage.
type KeyStore struct {
	strg storage.Storage

	mu       sync.Mutex
	keys     map[string]Key
	loadedAt time.Time
}

// NewKeyStore returns a KeyStore over raw (non-inheriting) storage.
func NewKeyStore(strg storage.Storage) *KeyStore {
	return &KeyStore{strg: strg}
}

// NewKeyRequest describes a key to create.
type NewKeyRequest struct {
	Name   string `json:"name"`
	Access Access `json:"access"`
	Path   string `json:"path"`
}

// Validate checks the request and normalizes its path.
func (r *NewKeyRequest) Validate() error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" || len(r.Name) > 64 {
		return errors.New("name must be between 1 and 64 characters")
	}
	if !r.Access.valid() {
		return fmt.Errorf("access must be %q or %q", AccessRead, AccessWrite)
	}
	if r.Path == "" {
		r.Path = "/"
	}
	if err := storage.ValidatePath(r.Path); err != nil {
		return err
	}
	r.Path = NormalizePath(r.Path)
	if strings.HasPrefix(r.Path, "/_realm/") {
		return errors.New("path must not be under /_realm/")
	}
	return nil
}

// Create stores a new key and returns it with its token, which is not
// recoverable afterwards.
func (s *KeyStore) Create(ctx context.Context, req NewKeyRequest, createdBy string) (Key, string, error) {
	if err := req.Validate(); err != nil {
		return Key{}, "", err
	}
	id, err := randomHex(8)
	if err != nil {
		return Key{}, "", err
	}
	secret, err := randomHex(32)
	if err != nil {
		return Key{}, "", err
	}
	key := Key{
		ID:        id,
		Name:      req.Name,
		Access:    req.Access,
		Path:      req.Path,
		CreatedAt: time.Now().UTC(),
		CreatedBy: createdBy,
		Hash:      hashSecret(secret),
	}
	err = s.update(ctx, func(keys map[string]Key) error {
		keys[id] = key
		return nil
	})
	if err != nil {
		return Key{}, "", err
	}
	key.Hash = ""
	return key, TokenPrefix + id + "_" + secret, nil
}

// List returns every key, oldest first, without secret hashes.
func (s *KeyStore) List(ctx context.Context) ([]Key, error) {
	keys, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	list := make([]Key, 0, len(keys))
	for _, k := range keys {
		k.Hash = ""
		list = append(list, k)
	}
	slices.SortFunc(list, func(a, b Key) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return list, nil
}

// Revoke deletes the key with the given ID.
func (s *KeyStore) Revoke(ctx context.Context, id string) error {
	return s.update(ctx, func(keys map[string]Key) error {
		if _, ok := keys[id]; !ok {
			return &storage.NotFoundError{Key: "key " + id}
		}
		delete(keys, id)
		return nil
	})
}

// Verify returns the key a token belongs to, or ErrInvalidKey.
func (s *KeyStore) Verify(ctx context.Context, token string) (*Key, error) {
	id, secret, ok := strings.Cut(strings.TrimPrefix(token, TokenPrefix), "_")
	if !ok || !strings.HasPrefix(token, TokenPrefix) || id == "" || secret == "" {
		return nil, ErrInvalidKey
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	key, found := s.keys[id]
	age := time.Since(s.loadedAt)
	if s.keys == nil || age > keyCacheTTL || (!found && age > keyRefreshInterval) {
		keys, err := s.load(ctx)
		if err != nil {
			return nil, err
		}
		s.keys, s.loadedAt = keys, time.Now()
		key, found = keys[id]
	}
	if !found || subtle.ConstantTimeCompare([]byte(key.Hash), []byte(hashSecret(secret))) != 1 {
		return nil, ErrInvalidKey
	}
	return &key, nil
}

// read returns the stored keys, refreshing the cache.
func (s *KeyStore) read(ctx context.Context) (map[string]Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	s.keys, s.loadedAt = keys, time.Now()
	return keys, nil
}

// update applies fn to the stored keys and persists the result.
func (s *KeyStore) update(ctx context.Context, fn func(map[string]Key) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, err := s.load(ctx)
	if err != nil {
		return err
	}
	if err := fn(keys); err != nil {
		return err
	}
	b, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	if err := s.strg.Put(ctx, storage.StorageEntry{Key: keysKey, Value: b}); err != nil {
		return err
	}
	s.keys, s.loadedAt = keys, time.Now()
	return nil
}

// load reads the stored keys. Callers must hold s.mu.
func (s *KeyStore) load(ctx context.Context) (map[string]Key, error) {
	keys := map[string]Key{}
	entry, err := s.strg.Get(ctx, keysKey)
	var nf *storage.NotFoundError
	if errors.As(err, &nf) {
		return keys, nil
	}
	if err != nil {
		return nil, fmt.Errorf("could not read API keys: %w", err)
	}
	if err := json.Unmarshal(entry.Value, &keys); err != nil {
		return nil, fmt.Errorf("could not read API keys: %w", err)
	}
	return keys, nil
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
