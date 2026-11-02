package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/steviebps/realm/helper/logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// S3Storage stores entries as objects in an Amazon S3 (or S3-compatible)
// bucket, using the same object layout as GCSStorage.
type S3Storage struct {
	client *s3.Client
	bucket string
	prefix string
	tracer trace.Tracer
}

var (
	_ Storage = (*S3Storage)(nil)
)

const s3EntryKey string = "entry"

// NewS3Storage creates S3-backed storage. Options:
//
//	bucket          (required) bucket name
//	region          AWS region; defaults to the SDK's resolution (AWS_REGION, profile, …)
//	prefix          optional key prefix so several realms can share a bucket
//	endpoint        optional custom endpoint for S3-compatible stores (MinIO, LocalStack)
//	force_path_style "true" to use path-style addressing (needed by most S3-compatible stores)
//
// Credentials come from the standard AWS chain (environment, shared config,
// IRSA / pod identity on Kubernetes, instance role).
func NewS3Storage(conf map[string]string) (Storage, error) {
	if conf["bucket"] == "" {
		return nil, fmt.Errorf("'bucket' must be set")
	}

	var loadOpts []func(*awsconfig.LoadOptions) error
	if region := conf["region"]; region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("could not load AWS config: %w", err)
	}

	forcePathStyle := false
	if v := conf["force_path_style"]; v != "" {
		if forcePathStyle, err = strconv.ParseBool(v); err != nil {
			return nil, fmt.Errorf("'force_path_style' must be a boolean: %w", err)
		}
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint := conf["endpoint"]; endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
		o.UsePathStyle = forcePathStyle
	})

	return &S3Storage{
		client: client,
		bucket: conf["bucket"],
		prefix: strings.Trim(conf["prefix"], "/"),
		tracer: otel.Tracer("github.com/steviebps/realm"),
	}, nil
}

func (s *S3Storage) Get(ctx context.Context, logicalPath string) (*StorageEntry, error) {
	ctx, span := s.tracer.Start(ctx, "S3Storage Get", trace.WithAttributes(attribute.String("realm.s3.logicalPath", logicalPath)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("get operation")

	if err := ValidatePath(logicalPath); err != nil {
		span.RecordError(err)
		return nil, err
	}

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(logicalPath)),
	})
	if err != nil {
		span.RecordError(err)
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, &NotFoundError{logicalPath}
		}
		return nil, err
	}
	defer out.Body.Close()

	value, err := io.ReadAll(out.Body)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}

	return &StorageEntry{Key: logicalPath, Value: value}, nil
}

func (s *S3Storage) Put(ctx context.Context, e StorageEntry) error {
	ctx, span := s.tracer.Start(ctx, "S3Storage Put", trace.WithAttributes(attribute.String("realm.s3.entry.key", e.Key)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", e.Key).Msg("put operation")

	if err := ValidatePath(e.Key); err != nil {
		span.RecordError(err)
		return err
	}

	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(s.objectKey(e.Key)),
		Body:        bytes.NewReader(e.Value),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to put: %w", err)
	}

	return nil
}

func (s *S3Storage) Delete(ctx context.Context, logicalPath string) error {
	ctx, span := s.tracer.Start(ctx, "S3Storage Delete", trace.WithAttributes(attribute.String("realm.s3.logicalPath", logicalPath)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("logicalPath", logicalPath).Msg("delete operation")

	if err := ValidatePath(logicalPath); err != nil {
		span.RecordError(err)
		return err
	}

	key := s.objectKey(logicalPath)
	// S3 deletes are idempotent, so check existence to report NotFoundError
	// consistently with the other backends.
	if _, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}); err != nil {
		span.RecordError(err)
		var nf *types.NotFound
		if errors.As(err, &nf) {
			return &NotFoundError{logicalPath}
		}
		return err
	}

	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}); err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to delete: %w", err)
	}

	return nil
}

func (s *S3Storage) List(ctx context.Context, prefix string) ([]string, error) {
	ctx, span := s.tracer.Start(ctx, "S3Storage List", trace.WithAttributes(attribute.String("realm.s3.prefix", prefix)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("prefix", prefix).Msg("list operation")

	if err := ValidatePath(prefix); err != nil {
		span.RecordError(err)
		return nil, err
	}

	listPrefix := s.withPrefix(strings.TrimPrefix(prefix, "/"))
	if listPrefix != "" && !strings.HasSuffix(listPrefix, "/") {
		listPrefix += "/"
	}

	keys := make([]string, 0)
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket:    aws.String(s.bucket),
		Prefix:    aws.String(listPrefix),
		Delimiter: aws.String("/"),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			span.RecordError(err)
			return nil, fmt.Errorf("failed to list objects: %w", err)
		}
		for _, cp := range page.CommonPrefixes {
			// "subdirectory"
			keys = append(keys, strings.TrimPrefix(aws.ToString(cp.Prefix), listPrefix))
		}
		for _, obj := range page.Contents {
			if strings.HasSuffix(aws.ToString(obj.Key), "_"+s3EntryKey) {
				// file
				keys = append(keys, ".")
			}
		}
	}

	slices.Sort(keys)

	return keys, nil
}

func (s *S3Storage) Close(ctx context.Context) error {
	return nil
}

// objectKey maps a logical path such as "/a/b/" to its object key "a/b/_entry"
// (under the configured prefix).
func (s *S3Storage) objectKey(logicalPath string) string {
	k := logicalPath + s3EntryKey
	dir := strings.TrimPrefix(path.Dir(k), "/")
	return s.withPrefix(path.Join(dir, "_"+path.Base(k)))
}

// withPrefix places a relative object key under the configured prefix.
func (s *S3Storage) withPrefix(key string) string {
	if s.prefix == "" {
		return key
	}
	return s.prefix + "/" + key
}
