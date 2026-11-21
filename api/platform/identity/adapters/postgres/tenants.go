package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	identitydb "chaskicode.dev/saas/gen/sqlc/identity"
	"chaskicode.dev/saas/platform/identity/domain"
	"chaskicode.dev/saas/platform/identity/usecases"
	"chaskicode.dev/saas/platform/kit/apperr"
	"chaskicode.dev/saas/platform/kit/authctx"
	"chaskicode.dev/saas/platform/kit/db"
	"chaskicode.dev/saas/platform/kit/pii"
)

// TenantStore implements usecases.TenantStore.
type TenantStore struct {
	db *db.DB
}

var _ usecases.TenantStore = (*TenantStore)(nil)

// NewTenantStore builds a TenantStore over a database that connects as app_runtime.
func NewTenantStore(database *db.DB) *TenantStore {
	return &TenantStore{db: database}
}

// AsUser implements usecases.TenantStore with db.WithUserTx.
func (s *TenantStore) AsUser(ctx context.Context, userID string, fn func(context.Context, usecases.TenantRepo) error) error {
	ctx, err := asUser(ctx, userID)
	if err != nil {
		return err
	}
	err = s.db.WithUserTx(ctx, func(ctx context.Context, q db.Querier) error {
		return fn(ctx, tenantRepo{repo{q: identitydb.New(q)}})
	})
	return db.MapError(err)
}

// InTenant implements usecases.TenantStore with db.WithTenantTx.
func (s *TenantStore) InTenant(ctx context.Context, p authctx.Principal, fn func(context.Context, usecases.TenantRepo) error) error {
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

func (r tenantRepo) RenameTenant(ctx context.Context, tenantID, name string) (string, usecases.Tenant, error) {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return "", usecases.Tenant{}, err
	}
	previous, err := r.q.LockTenantName(ctx, tid)
	if err != nil {
		return "", usecases.Tenant{}, fmt.Errorf("lock tenant: %w", err)
	}
	row, err := r.q.RenameTenant(ctx, identitydb.RenameTenantParams{ID: tid, Name: name})
	if err != nil {
		return "", usecases.Tenant{}, fmt.Errorf("rename tenant: %w", err)
	}
	return previous, usecases.Tenant{ID: row.ID.String(), Slug: row.Slug, Name: row.Name}, nil
}

func (r tenantRepo) ListMembers(ctx context.Context, tenantID, after string, limit int) ([]usecases.Member, error) {
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
	out := make([]usecases.Member, 0, len(rows))
	for _, row := range rows {
		out = append(out, member(row.ID, row.UserID, row.Email, row.DisplayName, row.Role, row.CreatedAt.Time))
	}
	return out, nil
}

func (r tenantRepo) MemberByUser(ctx context.Context, tenantID, userID string) (usecases.Member, bool, error) {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return usecases.Member{}, false, err
	}
	uid, err := parseUUID(userID)
	if err != nil {
		return usecases.Member{}, false, err
	}
	row, err := r.q.MemberByUser(ctx, identitydb.MemberByUserParams{TenantID: tid, UserID: uid})
	if errors.Is(err, pgx.ErrNoRows) {
		return usecases.Member{}, false, nil
	}
	if err != nil {
		return usecases.Member{}, false, fmt.Errorf("member by user: %w", err)
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

func member(id, userID pgtype.UUID, email, displayName, role string, joinedAt time.Time) usecases.Member {
	return usecases.Member{
		MembershipID: id.String(), UserID: userID.String(), Email: pii.Email(email),
		DisplayName: displayName, Role: role, JoinedAt: joinedAt,
	}
}

func (r tenantRepo) ShareLockTenant(ctx context.Context, tenantID string) error {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return err
	}
	if err := r.q.ShareLockTenant(ctx, tid); err != nil {
		return fmt.Errorf("share lock tenant: %w", err)
	}
	return nil
}

func (r tenantRepo) EmailIsMember(ctx context.Context, tenantID string, email pii.Email) (bool, error) {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return false, err
	}
	member, err := r.q.EmailIsMember(ctx, identitydb.EmailIsMemberParams{TenantID: tid, Email: email.Reveal()})
	if err != nil {
		return false, fmt.Errorf("email is member: %w", err)
	}
	return member, nil
}

func (r tenantRepo) PendingInvitationByEmail(ctx context.Context, tenantID string, email pii.Email) (usecases.Invitation, bool, error) {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return usecases.Invitation{}, false, err
	}
	row, err := r.q.PendingInvitationByEmail(ctx, identitydb.PendingInvitationByEmailParams{TenantID: tid, Email: email.Reveal()})
	if errors.Is(err, pgx.ErrNoRows) {
		return usecases.Invitation{}, false, nil
	}
	if err != nil {
		return usecases.Invitation{}, false, fmt.Errorf("pending invitation by email: %w", err)
	}
	return invitation(invitationRow(row)), true, nil
}

func (r tenantRepo) CreateInvitation(ctx context.Context, inv usecases.NewInvitation) (usecases.Invitation, error) {
	tid, err := parseUUID(inv.TenantID)
	if err != nil {
		return usecases.Invitation{}, err
	}
	by, err := parseUUID(inv.InvitedBy)
	if err != nil {
		return usecases.Invitation{}, err
	}
	row, err := r.q.CreateInvitation(ctx, identitydb.CreateInvitationParams{
		TenantID: tid, Email: inv.Email.Reveal(), Role: inv.Role, TokenHash: inv.TokenHash, InvitedBy: by,
		ExpiresAt: ts(inv.ExpiresAt),
	})
	if err != nil {
		// The partial unique index on (tenant_id, email) of pending invitations: a
		// concurrent invitation to the same email won.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == "invitations_pending_email_key" {
			e, _ := apperr.As(domain.ErrInvitationPending())
			return usecases.Invitation{}, apperr.Wrap(err, e.Kind, e.Code, e.Message)
		}
		return usecases.Invitation{}, fmt.Errorf("create invitation: %w", err)
	}
	return invitation(row), nil
}

func (r tenantRepo) InvitationByID(ctx context.Context, tenantID, id string) (usecases.Invitation, bool, error) {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return usecases.Invitation{}, false, err
	}
	iid, err := parseUUID(id)
	if err != nil {
		return usecases.Invitation{}, false, err
	}
	row, err := r.q.InvitationByID(ctx, identitydb.InvitationByIDParams{TenantID: tid, ID: iid})
	if errors.Is(err, pgx.ErrNoRows) {
		return usecases.Invitation{}, false, nil
	}
	if err != nil {
		return usecases.Invitation{}, false, fmt.Errorf("invitation by id: %w", err)
	}
	return invitation(invitationRow(row)), true, nil
}

func (r tenantRepo) RotateInvitation(ctx context.Context, id string, tokenHash []byte, expiresAt time.Time) error {
	iid, err := parseUUID(id)
	if err != nil {
		return err
	}
	if err := r.q.RotateInvitation(ctx, identitydb.RotateInvitationParams{ID: iid, TokenHash: tokenHash, ExpiresAt: ts(expiresAt)}); err != nil {
		return fmt.Errorf("rotate invitation: %w", err)
	}
	return nil
}

func (r tenantRepo) RevokeInvitation(ctx context.Context, id string) error {
	iid, err := parseUUID(id)
	if err != nil {
		return err
	}
	if err := r.q.RevokeInvitation(ctx, iid); err != nil {
		return fmt.Errorf("revoke invitation: %w", err)
	}
	return nil
}

func (r tenantRepo) ListInvitations(ctx context.Context, tenantID, after string, limit int) ([]usecases.Invitation, error) {
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
		return nil, fmt.Errorf("list invitations: limit %d out of range", limit)
	}
	rows, err := r.q.ListInvitations(ctx, identitydb.ListInvitationsParams{TenantID: tid, After: afterID, PageSize: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("list invitations: %w", err)
	}
	out := make([]usecases.Invitation, 0, len(rows))
	for _, row := range rows {
		out = append(out, invitation(invitationRow(row)))
	}
	return out, nil
}

func (r tenantRepo) AcceptInvitation(ctx context.Context, tokenHash []byte, ip netip.Prefix) (usecases.AcceptedInvitation, error) {
	row, err := r.q.AcceptInvitation(ctx, identitydb.AcceptInvitationParams{TokenHash: tokenHash, IpPrefix: prefix(ip)})
	if err != nil {
		return usecases.AcceptedInvitation{}, fmt.Errorf("accept invitation: %w", err)
	}
	return usecases.AcceptedInvitation{MembershipID: row.MembershipID.String(), TenantID: row.TenantID.String(), Role: row.Role}, nil
}

// invitationRow is the shape every invitation query returns; sqlc gives each query its
// own row type with the same fields.
type invitationRow = identitydb.CreateInvitationRow

func invitation(row invitationRow) usecases.Invitation {
	inv := usecases.Invitation{
		ID: row.ID.String(), TenantID: row.TenantID.String(), Email: pii.Email(row.Email), Role: row.Role,
		ExpiresAt: row.ExpiresAt.Time, CreatedAt: row.CreatedAt.Time,
		AcceptedAt: row.AcceptedAt.Time, RevokedAt: row.RevokedAt.Time,
	}
	if row.InvitedBy.Valid {
		inv.InvitedBy = row.InvitedBy.String()
	}
	return inv
}
