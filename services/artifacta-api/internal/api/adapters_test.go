package api

import (
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/infra"
)

// Adapter contracts, in one place. Each line fails to compile the moment a
// concrete adapter stops satisfying the consumer-side port it plugs into — so a
// contributor adding or changing a backend sees the break AT the adapter, not
// later at the wiring site in cmd/cli. This is the enforcement behind the
// ports-and-adapters layering: infra never imports api (the dependency arrow
// points one way); these assertions live on the api side, which is the natural
// place to state "this concrete type satisfies my interface".
//
// New backend? Add its assertion here alongside the others.

// Blob backends (ADR-0005 streaming interface; ADR-0006 S3 adapter).
var (
	_ Blob = (*infra.BlobFS)(nil)
	_ Blob = (*infra.BlobS3)(nil)
)

// Metadata store backends (ADR-0009 selectable file/Postgres store).
var (
	_ Store = (*infra.FileStore)(nil)
	_ Store = (*infra.SQLStore)(nil)
)

// Identity providers (ADR-0007 pluggable auth: local dev token + OIDC SSO).
var (
	_ Auth = (*Local)(nil)
	_ Auth = (*OIDCProvider)(nil)
)
