module github.com/steviebps/realm

go 1.27.3

require (
	cloud.google.com/go/storage v1.70.0
	github.com/NYTimes/gziphandler v1.1.1
	github.com/allegro/bigcache/v3 v3.2.0
	github.com/aws/aws-sdk-go-v2 v1.47.7
	github.com/aws/aws-sdk-go-v2/config v1.33.15
	github.com/aws/aws-sdk-go-v2/service/s3 v1.114.8
	github.com/coreos/go-oidc/v3 v3.21.0
	github.com/go-jose/go-jose/v4 v4.1.9
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.12.0
	github.com/rs/zerolog v1.35.1
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.0.10
	go.etcd.io/bbolt v1.5.0
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.72.0
	go.opentelemetry.io/otel v1.47.0
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.47.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.47.0
	go.opentelemetry.io/otel/exporters/stdout/stdouttrace v1.47.0
	go.opentelemetry.io/otel/metric v1.47.0
	go.opentelemetry.io/otel/sdk v1.47.0
	go.opentelemetry.io/otel/sdk/metric v1.47.0
	go.opentelemetry.io/otel/trace v1.47.0
	golang.org/x/mod v0.42.0
	golang.org/x/oauth2 v0.37.0
	golang.org/x/sync v0.24.0
	golang.org/x/sys v0.49.0
	google.golang.org/api v0.311.0
)

require (
	cel.dev/expr v0.25.3 // indirect
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/auth v0.24.3 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.3.1 // indirect
	cloud.google.com/go/compute/metadata v0.10.0 // indirect
	cloud.google.com/go/iam v1.15.1 // indirect
	cloud.google.com/go/monitoring v1.32.1 // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/metric v0.62.0 // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/internal/resourcemapping v0.62.0 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.26 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.20.15 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.20.7 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.10 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.10 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.10 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.24 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.11 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.10 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.10 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.12.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.39.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.44.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.51.8 // indirect
	github.com/aws/smithy-go v1.28.8 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cncf/xds/go v0.0.0-20260202195803-dba9d589def2 // indirect
	github.com/envoyproxy/go-control-plane/envoy v1.40.0 // indirect
	github.com/envoyproxy/protoc-gen-validate v1.3.3 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/s2a-go v0.1.11 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.25 // indirect
	github.com/googleapis/gax-go/v2 v2.27.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.33.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/mattn/go-colorable v0.1.16 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/planetscale/vtprotobuf v0.6.1-0.20240319094008-0393e58bdf10 // indirect
	github.com/spiffe/go-spiffe/v2 v2.9.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/detectors/gcp v1.47.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.72.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/proto/otlp v1.11.1 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	google.golang.org/genproto v0.0.0-20261104224025-d1e00e9f33ab // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20261104224025-d1e00e9f33ab // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261104224025-d1e00e9f33ab // indirect
	google.golang.org/grpc v1.85.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
