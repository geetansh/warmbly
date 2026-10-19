package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/warmbly/warmbly/internal/api/middleware"
	"github.com/warmbly/warmbly/internal/app/unibox"
)

type forgetCountingUnibox struct {
	unibox.UniboxService
	forgets int
}

func (f *forgetCountingUnibox) ForgetOverview(uuid.UUID) { f.forgets++ }

func TestForgetUniboxOverviewOnWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name, method, route, path string
		status, want              int
	}{
		{"a unibox write", http.MethodPatch, "/v1/unibox/seen", "/v1/unibox/seen", http.StatusOK, 1},
		{"a mailbox write", http.MethodDelete, "/v1/emails/:id", "/v1/emails/x", http.StatusOK, 1},
		{"a read", http.MethodGet, "/v1/unibox/overview", "/v1/unibox/overview", http.StatusOK, 0},
		{"a refused write", http.MethodPatch, "/v1/unibox/seen", "/v1/unibox/seen", http.StatusBadRequest, 0},
		{"a contact search", http.MethodPost, "/v1/contacts/search", "/v1/contacts/search", http.StatusOK, 0},
		{"a CRM summary", http.MethodPost, "/v1/crm/deals/summary", "/v1/crm/deals/summary", http.StatusOK, 0},
		{"a template render", http.MethodPost, "/v1/templates/:id/render", "/v1/templates/x/render", http.StatusOK, 0},
		{"a contact create", http.MethodPost, "/v1/contacts", "/v1/contacts", http.StatusCreated, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &forgetCountingUnibox{}
			h := &Handler{UniboxService: svc}
			r := gin.New()
			r.Use(func(c *gin.Context) { c.Set(middleware.OrganizationIDKey, uuid.New()) }, h.ForgetUniboxOverviewOnWrite)
			r.Handle(tt.method, tt.route, func(c *gin.Context) { c.Status(tt.status) })
			r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tt.method, tt.path, nil))
			if svc.forgets != tt.want {
				t.Errorf("forgets = %d, want %d", svc.forgets, tt.want)
			}
		})
	}
}
