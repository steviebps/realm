package storage

import (
	"context"

	realm "github.com/steviebps/realm/pkg"
)

type Storage interface {
	Get(ctx context.Context, key string) (*realm.Chamber, error)
	Put(ctx context.Context, c *realm.Chamber) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
}
