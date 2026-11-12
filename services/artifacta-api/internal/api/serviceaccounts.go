package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	artifactav1 "github.com/agarwalvivek29/here.now/packages/schema/generated/go/artifacta/v1"
	"github.com/agarwalvivek29/here.now/services/artifacta-api/internal/domain"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ServiceAccountsPath is the single admin endpoint for service accounts and
// their keys (ADR-0028). Every operation stays on this path and selects its
// target with query params, so a fronting gateway needs exactly one route:
//
//	POST   /serviceaccounts                 {name, description?}  create account
//	GET    /serviceaccounts                                       list accounts
//	DELETE /serviceaccounts?name=N                                disable account
//	POST   /serviceaccounts?name=N          {ttl, description?}   mint key (shown once)
//	GET    /serviceaccounts?name=N                                list keys
//	DELETE /serviceaccounts?name=N&key_id=K                       revoke key
const ServiceAccountsPath = "/serviceaccounts"

// DefaultSAKeyTTLs are the key lifetimes allowed when none are configured.
var DefaultSAKeyTTLs = []string{"30d", "90d", "never"}

// missingGroupsHint is the 403 body for a caller whose token carries no groups
// claim at all: almost always a login from before `artifacta login` requested
// the groups scope, since a token refresh keeps the original scopes.
const missingGroupsHint = "forbidden: your login has no groups claim (it predates service-account admin). " +
	"Run `artifacta login <url>` again, then retry."

// saEnabled reports whether service-account management is configured at all.
func (s *Server) saEnabled() bool {
	return len(s.SAAdminGroups) > 0 || len(s.SAAdminEmails) > 0
}

func (s *Server) serviceAccounts(w http.ResponseWriter, r *http.Request) {
	if !s.saEnabled() {
		http.NotFound(w, r)
		return
	}
	// CLI-only (ADR-0028): a browser session cookie is never enough. Requiring
	// an Authorization header also means a cross-site page can't drive these
	// endpoints with an admin's cookie, since it can't set that header without
	// a CORS preflight this server never approves.
	if bearer(r) == "" {
		http.Error(w, "unauthorized: service-account admin needs a CLI login (artifacta login)", http.StatusUnauthorized)
		return
	}
	who, ok := s.Auth.Identify(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	name := r.URL.Query().Get("name")
	keyID := r.URL.Query().Get("key_id")
	if !domain.IsAdmin(who, s.SAAdminGroups, s.SAAdminEmails) {
		s.audit(who, saAuditSlug(name), artifactav1.AuditAction_AUDIT_ACTION_DENY, false)
		if !who.GetGroupsClaimPresent() && !domain.IsServiceAccount(who.GetSub()) && len(s.SAAdminGroups) > 0 {
			http.Error(w, missingGroupsHint, http.StatusForbidden)
			return
		}
		http.Error(w, "forbidden: service-account admin only", http.StatusForbidden)
		return
	}

	switch {
	case r.Method == http.MethodPost && name == "":
		s.saCreate(w, r, who)
	case r.Method == http.MethodGet && name == "":
		s.saList(w)
	case r.Method == http.MethodDelete && name != "" && keyID == "":
		s.saDisable(w, who, name)
	case r.Method == http.MethodPost && name != "":
		s.saKeyCreate(w, r, who, name)
	case r.Method == http.MethodGet && name != "":
		s.saKeyList(w, name)
	case r.Method == http.MethodDelete && name != "" && keyID != "":
		s.saKeyRevoke(w, who, name, keyID)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "unsupported service-account operation", http.StatusMethodNotAllowed)
	}
}

func (s *Server) saCreate(w http.ResponseWriter, r *http.Request, who *artifactav1.Identity) {
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decodeSABody(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if !domain.ValidSAName(name) {
		http.Error(w, "invalid name: use 2-63 lowercase letters, digits or '-', starting with a letter or digit", http.StatusBadRequest)
		return
	}
	sa := &artifactav1.ServiceAccount{
		Id: domain.NewSlug(), Name: name, Description: strings.TrimSpace(body.Description),
		CreatedBySub: who.GetSub(), CreatedByEmail: who.GetEmail(), CreatedAt: timestamppb.Now(),
	}
	if err := s.Store.CreateServiceAccount(sa); err != nil {
		if errors.Is(err, domain.ErrSAExists) {
			http.Error(w, "service account "+name+" already exists (names are never reused)", http.StatusConflict)
			return
		}
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	s.audit(who, saAuditSlug(name), artifactav1.AuditAction_AUDIT_ACTION_SA_CREATE, true)
	writeSAJSON(w, http.StatusCreated, saView(sa, nil))
}

func (s *Server) saList(w http.ResponseWriter) {
	sas, err := s.Store.ListServiceAccounts()
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(sas))
	for _, sa := range sas {
		keys, err := s.Store.ListAPIKeys(sa.GetId())
		if err != nil {
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
		out = append(out, saView(sa, keys))
	}
	writeSAJSON(w, http.StatusOK, map[string]any{"service_accounts": out})
}

func (s *Server) saDisable(w http.ResponseWriter, who *artifactav1.Identity, name string) {
	done, err := s.Store.DisableServiceAccount(name, who.GetSub(), time.Now())
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if !done {
		http.Error(w, "no active service account named "+name, http.StatusNotFound)
		return
	}
	s.audit(who, saAuditSlug(name), artifactav1.AuditAction_AUDIT_ACTION_SA_DISABLE, true)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) saKeyCreate(w http.ResponseWriter, r *http.Request, who *artifactav1.Identity, name string) {
	var body struct {
		TTL         string `json:"ttl"`
		Description string `json:"description"`
	}
	if !decodeSABody(w, r, &body) {
		return
	}
	ttl, err := domain.ParseTTL(body.TTL, s.saKeyTTLs())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sa, ok, err := s.Store.ServiceAccountByName(name)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if !ok || sa.GetDisabledAt() != nil {
		http.Error(w, "no active service account named "+name, http.StatusNotFound)
		return
	}
	raw, hash, prefix := domain.NewAPIKey()
	now := time.Now()
	k := &artifactav1.ApiKey{
		Id: domain.NewSlug(), ServiceAccountId: sa.GetId(), KeyHash: hash, Prefix: prefix,
		Description: strings.TrimSpace(body.Description), CreatedAt: timestamppb.New(now),
		CreatedBySub: who.GetSub(), CreatedByEmail: who.GetEmail(),
	}
	if ttl > 0 {
		k.ExpiresAt = timestamppb.New(now.Add(ttl))
	}
	if err := s.Store.PutAPIKey(k); err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	s.audit(who, saAuditSlug(name), artifactav1.AuditAction_AUDIT_ACTION_SA_KEY_CREATE, true)
	view := keyView(k, now)
	view["api_key"] = raw // the only time the raw key ever leaves the server
	writeSAJSON(w, http.StatusCreated, view)
}

func (s *Server) saKeyList(w http.ResponseWriter, name string) {
	sa, ok, err := s.Store.ServiceAccountByName(name)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no service account named "+name, http.StatusNotFound)
		return
	}
	keys, err := s.Store.ListAPIKeys(sa.GetId())
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	now := time.Now()
	out := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, keyView(k, now))
	}
	writeSAJSON(w, http.StatusOK, map[string]any{"api_keys": out})
}

func (s *Server) saKeyRevoke(w http.ResponseWriter, who *artifactav1.Identity, name, keyID string) {
	sa, ok, err := s.Store.ServiceAccountByName(name)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no service account named "+name, http.StatusNotFound)
		return
	}
	done, err := s.Store.RevokeAPIKey(sa.GetId(), keyID, time.Now())
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if !done {
		http.Error(w, "no active key "+keyID+" on service account "+name, http.StatusNotFound)
		return
	}
	s.audit(who, saAuditSlug(name), artifactav1.AuditAction_AUDIT_ACTION_SA_KEY_REVOKE, true)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) saKeyTTLs() []string {
	if len(s.SAKeyTTLs) > 0 {
		return s.SAKeyTTLs
	}
	return DefaultSAKeyTTLs
}

// saAuditSlug is the audit-trail slug for a service-account operation:
// "sa:<name>", which can never collide with a random artifact slug.
func saAuditSlug(name string) string {
	if name == "" {
		return domain.SASubPrefix + "*"
	}
	return domain.SASub(name)
}

func decodeSABody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "bad request: expected a JSON body", http.StatusBadRequest)
		return false
	}
	return true
}

func writeSAJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store") // a key-create response carries a secret
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// saView is the API shape of a service account, including how many of its
// keys are usable and the most recent use across all of them.
func saView(sa *artifactav1.ServiceAccount, keys []*artifactav1.ApiKey) map[string]any {
	createdBy := sa.GetCreatedByEmail()
	if createdBy == "" {
		createdBy = sa.GetCreatedBySub()
	}
	now := time.Now()
	active := 0
	var lastUsed *timestamppb.Timestamp
	for _, k := range keys {
		if domain.KeyUsable(k, now) {
			active++
		}
		if lu := k.GetLastUsedAt(); lu != nil && (lastUsed == nil || lu.AsTime().After(lastUsed.AsTime())) {
			lastUsed = lu
		}
	}
	return map[string]any{
		"id": sa.GetId(), "name": sa.GetName(), "description": sa.GetDescription(),
		"created_by": createdBy, "created_at": tsOrNil(sa.GetCreatedAt()),
		"disabled_at": tsOrNil(sa.GetDisabledAt()),
		"active_keys": active, "last_used_at": tsOrNil(lastUsed),
	}
}

// keyView is the API shape of a key. It never includes key_hash.
func keyView(k *artifactav1.ApiKey, now time.Time) map[string]any {
	createdBy := k.GetCreatedByEmail()
	if createdBy == "" {
		createdBy = k.GetCreatedBySub()
	}
	return map[string]any{
		"id": k.GetId(), "prefix": k.GetPrefix(), "description": k.GetDescription(),
		"created_by": createdBy, "created_at": tsOrNil(k.GetCreatedAt()),
		"expires_at": tsOrNil(k.GetExpiresAt()), "revoked_at": tsOrNil(k.GetRevokedAt()),
		"last_used_at": tsOrNil(k.GetLastUsedAt()), "status": domain.KeyStatus(k, now),
	}
}

func tsOrNil(t *timestamppb.Timestamp) any {
	if t == nil {
		return nil
	}
	return t.AsTime().UTC().Format(time.RFC3339)
}
