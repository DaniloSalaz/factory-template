package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	identitydb "github.com/DaniloSalaz/factory-template/api/gen/sqlc/identity"
	"github.com/DaniloSalaz/factory-template/api/internal/modules/identity/app"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/authctx"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/db"
	"github.com/DaniloSalaz/factory-template/api/internal/platform/pii"
)

// TenantStore implements app.TenantStore.
type TenantStore struct {
	db *db.DB
}

var _ app.TenantStore = (*TenantStore)(nil)

// NewTenantStore builds a TenantStore over a database that connects as app_runtime.
func NewTenantStore(database *db.DB) *TenantStore {
	return &TenantStore{db: database}
}

// AsUser implements app.TenantStore with db.WithUserTx.
func (s *TenantStore) AsUser(ctx context.Context, userID string, fn func(context.Context, app.TenantRepo) error) error {
	ctx, err := asUser(ctx, userID)
	if err != nil {
		return err
	}
	err = s.db.WithUserTx(ctx, func(ctx context.Context, q db.Querier) error {
		return fn(ctx, tenantRepo{repo{q: identitydb.New(q)}})
	})
	return db.MapError(err)
}

// InTenant implements app.TenantStore with db.WithTenantTx.
func (s *TenantStore) InTenant(ctx context.Context, p authctx.Principal, fn func(context.Context, app.TenantRepo) error) error {
	err := s.db.WithTenantTx(inTenant(ctx, p), func(ctx context.Context, q db.Querier) error {
		return fn(ctx, tenantRepo{repo{q: identitydb.New(q)}})
	})
	return db.MapError(err)
}

// tenantRepo shares repo's queries and adds those of tenant and member management.
type tenantRepo struct {
	repo
}

func (r tenantRepo) CreateTenant(ctx context.Context, name, slug string, maxTenants int, ip netip.Prefix) (string, error) {
	if maxTenants < 1 || maxTenants > math.MaxInt32 {
		return "", fmt.Errorf("create tenant: limit %d out of range", maxTenants)
	}
	row, err := r.q.CreateTenant(ctx, identitydb.CreateTenantParams{
		Name: name, Slug: slug, MaxTenants: int32(maxTenants), IpPrefix: prefix(ip),
	})
	if err != nil {
		return "", fmt.Errorf("create tenant: %w", err)
	}
	return row.TenantID.String(), nil
}

func (r tenantRepo) RenameTenant(ctx context.Context, tenantID, name string) (string, app.Tenant, error) {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return "", app.Tenant{}, err
	}
	previous, err := r.q.LockTenantName(ctx, tid)
	if err != nil {
		return "", app.Tenant{}, fmt.Errorf("lock tenant: %w", err)
	}
	row, err := r.q.RenameTenant(ctx, identitydb.RenameTenantParams{ID: tid, Name: name})
	if err != nil {
		return "", app.Tenant{}, fmt.Errorf("rename tenant: %w", err)
	}
	return previous, app.Tenant{ID: row.ID.String(), Slug: row.Slug, Name: row.Name}, nil
}

func (r tenantRepo) ListMembers(ctx context.Context, tenantID, after string, limit int) ([]app.Member, error) {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return nil, err
	}
	var afterID pgtype.UUID
	if after != "" {
		if afterID, err = parseUUID(after); err != nil {
			return nil, err
		}
	}
	if limit < 1 || limit > math.MaxInt32 {
		return nil, fmt.Errorf("list members: limit %d out of range", limit)
	}
	rows, err := r.q.ListMembers(ctx, identitydb.ListMembersParams{TenantID: tid, After: afterID, PageSize: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	out := make([]app.Member, 0, len(rows))
	for _, row := range rows {
		out = append(out, member(row.ID, row.UserID, row.Email, row.DisplayName, row.Role, row.CreatedAt.Time))
	}
	return out, nil
}

func (r tenantRepo) MemberByUser(ctx context.Context, tenantID, userID string) (app.Member, bool, error) {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return app.Member{}, false, err
	}
	uid, err := parseUUID(userID)
	if err != nil {
		return app.Member{}, false, err
	}
	row, err := r.q.MemberByUser(ctx, identitydb.MemberByUserParams{TenantID: tid, UserID: uid})
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Member{}, false, nil
	}
	if err != nil {
		return app.Member{}, false, fmt.Errorf("member by user: %w", err)
	}
	return member(row.ID, row.UserID, row.Email, row.DisplayName, row.Role, row.CreatedAt.Time), true, nil
}

func (r tenantRepo) ChangeMemberRole(ctx context.Context, userID, role, expectedActorRole, expectedMemberRole string, offerableRoles []string, ip netip.Prefix) error {
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}
	if err := r.q.ChangeMemberRole(ctx, identitydb.ChangeMemberRoleParams{
		Member: uid, Role: role, ExpectedActorRole: expectedActorRole, ExpectedMemberRole: expectedMemberRole,
		OfferableRoles: offerableRoles, IpPrefix: prefix(ip),
	}); err != nil {
		return fmt.Errorf("change member role: %w", err)
	}
	return nil
}

func (r tenantRepo) RemoveMember(ctx context.Context, userID, expectedActorRole, expectedMemberRole string, ip netip.Prefix) error {
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}
	if err := r.q.RemoveMember(ctx, identitydb.RemoveMemberParams{
		Member: uid, ExpectedActorRole: expectedActorRole, ExpectedMemberRole: expectedMemberRole, IpPrefix: prefix(ip),
	}); err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	return nil
}

func (r tenantRepo) LeaveTenant(ctx context.Context, ip netip.Prefix) error {
	if err := r.q.LeaveTenant(ctx, prefix(ip)); err != nil {
		return fmt.Errorf("leave tenant: %w", err)
	}
	return nil
}

func member(id, userID pgtype.UUID, email, displayName, role string, joinedAt time.Time) app.Member {
	return app.Member{
		MembershipID: id.String(), UserID: userID.String(), Email: pii.Email(email),
		DisplayName: displayName, Role: role, JoinedAt: joinedAt,
	}
}
