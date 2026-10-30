package route

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/abdullahPrasetio/relay-api/internal/delivery/http/handler"
	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	"github.com/abdullahPrasetio/relay-api/internal/usecase"
	"github.com/abdullahPrasetio/relay-api/pkg/auth"
)

type stubInviteUseCase struct{}

func (stubInviteUseCase) CreateInvite(context.Context, uuid.UUID, string, entity.Role, time.Duration) (string, time.Time, error) {
	return "", time.Time{}, nil
}

func (stubInviteUseCase) GetInvite(context.Context, string) (*entity.InviteInfo, error) {
	return &entity.InviteInfo{WorkspaceID: "ws_x", WorkspaceName: "Team", Role: entity.RoleViewer, ExpiresAt: time.Now()}, nil
}

func (stubInviteUseCase) AcceptInvite(context.Context, uuid.UUID, string) (*entity.Workspace, entity.Role, error) {
	return nil, "", nil
}

// TestSetupRegistersInviteAndAuditRoutes pins the phase-2 invite/audit surface
// and proves route registration does not conflict with the existing workspace
// or (separately mounted) realtime groups.
func TestSetupRegistersInviteAndAuditRoutes(t *testing.T) {
	app := fiber.New()
	Setup(
		app,
		&handler.UserHandler{},
		&handler.AuthHandler{},
		&handler.WorkspaceHandler{},
		handler.NewInviteHandler(stubInviteUseCase{}),
		&handler.RealtimeHandler{},
		&handler.HealthHandler{},
		&auth.Config{Secret: "test-secret-at-least-32-bytes-long!!"},
		"test",
	)

	registered := make(map[string]bool)
	for _, route := range app.GetRoutes() {
		registered[route.Method+" "+route.Path] = true
	}

	want := []string{
		"POST /api/v1/workspaces/:id/invites",
		"GET /api/v1/invites/:token",
		"POST /api/v1/invites/:token/accept",
		"GET /api/v1/workspaces/:id/audit",
		"GET /api/v1/workspaces/:id/stream",
		"POST /api/v1/workspaces/:id/presence",
		"GET /api/v1/workspaces/:id/presence",
	}
	for _, route := range want {
		if !registered[route] {
			t.Errorf("route %q is not registered", route)
		}
	}

	// The public metadata endpoint must not require a Bearer token, while
	// creation and acceptance must.
	res, err := app.Test(httptest.NewRequest("GET", "/api/v1/invites/token", nil))
	if err != nil {
		t.Fatalf("public invite request: %v", err)
	}
	if res.StatusCode != fiber.StatusOK {
		t.Fatalf("GET /invites/:token status = %d, want 200", res.StatusCode)
	}

	res, err = app.Test(httptest.NewRequest("POST", "/api/v1/invites/token/accept", nil))
	if err != nil {
		t.Fatalf("accept request: %v", err)
	}
	if res.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("POST /invites/:token/accept status = %d, want 401", res.StatusCode)
	}

	res, err = app.Test(httptest.NewRequest("POST", "/api/v1/workspaces/ws_x/invites", nil))
	if err != nil {
		t.Fatalf("create invite request: %v", err)
	}
	if res.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("POST /workspaces/:id/invites status = %d, want 401", res.StatusCode)
	}

	res, err = app.Test(httptest.NewRequest("GET", "/api/v1/workspaces/ws_x/audit", nil))
	if err != nil {
		t.Fatalf("audit request: %v", err)
	}
	if res.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("GET /workspaces/:id/audit status = %d, want 401", res.StatusCode)
	}
}

// Ensure the stub satisfies the interface at compile time.
var _ usecase.InviteUseCase = stubInviteUseCase{}
