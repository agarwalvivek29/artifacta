module github.com/agarwalvivek29/here.now/services/artifacta-api

go 1.26.3

require (
	github.com/agarwalvivek29/here.now/packages/schema/generated/go v0.0.0
	github.com/coreos/go-oidc/v3 v3.20.0
	github.com/evanw/esbuild v0.28.2
	github.com/jackc/pgx/v5 v5.5.5
	golang.org/x/oauth2 v0.36.0
	google.golang.org/protobuf v1.36.11
	gorm.io/driver/postgres v1.5.11
	gorm.io/gorm v1.30.1
)

require (
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20221227161230-091c0ba34f0a // indirect
	github.com/jackc/puddle/v2 v2.2.1 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	golang.org/x/crypto v0.17.0 // indirect
	golang.org/x/sync v0.9.0 // indirect
	golang.org/x/sys v0.15.0 // indirect
	golang.org/x/text v0.20.0 // indirect
)

replace github.com/agarwalvivek29/here.now/packages/schema/generated/go => ../../packages/schema/generated/go
