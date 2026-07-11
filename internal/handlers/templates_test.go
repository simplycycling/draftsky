package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/rsherman/draftsky/internal/db/sqlc"
	"github.com/rsherman/draftsky/internal/middleware"
)

// fakeTemplateStore is an in-memory templateStore for endpoint tests. It records the
// mutating calls so tests can assert what the handler did without a live PostgreSQL.
type fakeTemplateStore struct {
	user       db.User
	count      int64
	countCalls int
	created    *db.CreateTemplateParams
	updated    *db.UpdateTemplateParams
	deleted    *db.DeleteTemplateParams
}

func (f *fakeTemplateStore) GetUserByDID(_ context.Context, _ string) (db.User, error) {
	return f.user, nil
}

func (f *fakeTemplateStore) ListTemplatesByUser(_ context.Context, _ pgtype.Int4) ([]db.Template, error) {
	return nil, nil
}

func (f *fakeTemplateStore) CountTemplatesByUser(_ context.Context, _ pgtype.Int4) (int64, error) {
	f.countCalls++
	return f.count, nil
}

func (f *fakeTemplateStore) CreateTemplate(_ context.Context, arg db.CreateTemplateParams) (db.Template, error) {
	f.created = &arg
	return db.Template{ID: 99, Name: arg.Name, Suffix: arg.Suffix, Position: arg.Position}, nil
}

func (f *fakeTemplateStore) GetTemplate(_ context.Context, arg db.GetTemplateParams) (db.Template, error) {
	return db.Template{ID: arg.ID, Name: "existing", Suffix: "#x"}, nil
}

func (f *fakeTemplateStore) UpdateTemplate(_ context.Context, arg db.UpdateTemplateParams) (db.Template, error) {
	f.updated = &arg
	return db.Template{ID: arg.ID, Name: arg.Name, Suffix: arg.Suffix}, nil
}

func (f *fakeTemplateStore) UpdateTemplatePosition(_ context.Context, arg db.UpdateTemplatePositionParams) (db.Template, error) {
	return db.Template{ID: arg.ID, Position: arg.Position}, nil
}

func (f *fakeTemplateStore) DeleteTemplate(_ context.Context, arg db.DeleteTemplateParams) error {
	f.deleted = &arg
	return nil
}

// WithTx is unused by the create/update/delete paths under test (only reorder needs it).
func (f *fakeTemplateStore) WithTx(_ pgx.Tx) *db.Queries { return nil }

const tmplTestDID = "did:plc:tmpl"

// templateRouter mounts the template endpoints behind a seed middleware that stands in
// for RequireAuth by populating the DID in context. CSRF is exercised in its own suite,
// so it is omitted here to keep these tests focused on the free-tier cap.
func templateRouter(store templateStore) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := &TemplateHandler{queries: store}
	seed := func(c *gin.Context) { c.Set(middleware.ContextKeyDID, tmplTestDID) }
	g := r.Group("/api", seed)
	g.POST("/templates", h.HandleCreateTemplate)
	g.PUT("/templates/:id", h.HandleUpdateTemplate)
	g.DELETE("/templates/:id", h.HandleDeleteTemplate)
	return r
}

func jsonPost(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestCreateTemplate_FreeUnderCapCreates(t *testing.T) {
	store := &fakeTemplateStore{user: db.User{ID: 1, Did: tmplTestDID, Plan: "free"}, count: 4}
	r := templateRouter(store)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, jsonPost(http.MethodPost, "/api/templates", `{"name":"Fifth","suffix":"#a"}`))

	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d (body %s)", w.Code, w.Body.String())
	}
	if store.created == nil {
		t.Fatal("expected CreateTemplate to be called")
	}
}

func TestCreateTemplate_FreeAtCapForbidden(t *testing.T) {
	store := &fakeTemplateStore{user: db.User{ID: 1, Did: tmplTestDID, Plan: "free"}, count: freeTemplateLimit}
	r := templateRouter(store)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, jsonPost(http.MethodPost, "/api/templates", `{"name":"Sixth","suffix":"#a"}`))

	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d (body %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), freeTemplateLimitMessage) {
		t.Errorf("body %q must contain the limit message %q", w.Body.String(), freeTemplateLimitMessage)
	}
	if store.created != nil {
		t.Errorf("free user at cap must not create: got %+v", *store.created)
	}
}

func TestCreateTemplate_PaidOverCapCreates(t *testing.T) {
	// Paid plan short-circuits the cap entirely — no count query, no block, even well
	// past the free limit.
	store := &fakeTemplateStore{user: db.User{ID: 1, Did: tmplTestDID, Plan: "paid"}, count: freeTemplateLimit + 3}
	r := templateRouter(store)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, jsonPost(http.MethodPost, "/api/templates", `{"name":"Many","suffix":"#a"}`))

	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d (body %s)", w.Code, w.Body.String())
	}
	if store.created == nil {
		t.Fatal("expected CreateTemplate to be called for paid user")
	}
	if store.countCalls != 0 {
		t.Errorf("paid user must not trigger a count query, got %d calls", store.countCalls)
	}
}

func TestUpdateTemplate_AtCapStillWorks(t *testing.T) {
	// A free user at the cap can still edit — the limit gates creation only, never edits.
	store := &fakeTemplateStore{user: db.User{ID: 1, Did: tmplTestDID, Plan: "free"}, count: freeTemplateLimit}
	r := templateRouter(store)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, jsonPost(http.MethodPut, "/api/templates/7", `{"name":"Renamed","suffix":"#z"}`))

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body %s)", w.Code, w.Body.String())
	}
	if store.updated == nil {
		t.Fatal("expected UpdateTemplate to be called at cap")
	}
	if store.countCalls != 0 {
		t.Errorf("edit must not consult the template cap, got %d count calls", store.countCalls)
	}
}

func TestDeleteTemplate_AtCapStillWorks(t *testing.T) {
	// A free user at the cap can still delete.
	store := &fakeTemplateStore{user: db.User{ID: 1, Did: tmplTestDID, Plan: "free"}, count: freeTemplateLimit}
	r := templateRouter(store)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/templates/7", nil))

	if w.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d (body %s)", w.Code, w.Body.String())
	}
	if store.deleted == nil {
		t.Fatal("expected DeleteTemplate to be called at cap")
	}
	if store.countCalls != 0 {
		t.Errorf("delete must not consult the template cap, got %d count calls", store.countCalls)
	}
}

// TestTemplatesPageRenders parses the templates page exactly as production does and
// executes the full "layout" for both the can-add and at-cap states. This catches
// parse/execution errors in the new CanAddTemplate/LimitMessage conditionals — an
// undefined field reference or escaping surprise a code-walk cannot.
func TestTemplatesPageRenders(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoRootFromTest(t)); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)

	h, err := NewUIHandler(nil, []byte("test-secret"), nil)
	if err != nil {
		t.Fatalf("NewUIHandler: %v", err)
	}

	for _, tc := range []struct {
		name   string
		canAdd bool
	}{
		{"can-add", true},
		{"at-cap", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := TemplatesPageData{
				LayoutData: LayoutData{
					User:      PageUser{DID: "did:plc:me", Handle: "me.bsky.social", Plan: "free"},
					Theme:     "ocean",
					CSRFToken: "tok",
				},
				Templates:      []templateResponse{{ID: 1, Name: "Devils", Suffix: "#NJDevils"}},
				CanAddTemplate: tc.canAdd,
				LimitMessage:   freeTemplateLimitMessage,
			}
			if err := h.tmplTemplates.ExecuteTemplate(io.Discard, "layout", data); err != nil {
				t.Fatalf("execute layout: %v", err)
			}
		})
	}
}
