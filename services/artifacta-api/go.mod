module github.com/agarwalvivek29/here.now/services/artifacta-api

go 1.26.3

require (
	github.com/agarwalvivek29/here.now/packages/schema/generated/go v0.0.0
	github.com/coreos/go-oidc/v3 v3.20.0
	github.com/evanw/esbuild v0.28.2
	golang.org/x/oauth2 v0.36.0
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	golang.org/x/sys v0.0.0-20220715151400-c0bba94af5f8 // indirect
)

replace github.com/agarwalvivek29/here.now/packages/schema/generated/go => ../../packages/schema/generated/go
