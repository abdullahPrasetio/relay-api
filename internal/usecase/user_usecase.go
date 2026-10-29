package usecase

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
)

// userTracer is named per-domain so usecases generated later (wapgo make:usecase)
// can declare their own tracer in this package without a redeclaration collision.
var userTracer = otel.Tracer("user-usecase")

type CreateUserRequest struct {
	Name     string `json:"name"     validate:"required,min=2,max=100"`
	Email    string `json:"email"    validate:"required,email"`
	Password string `json:"password" validate:"required,min=8,max=72"`
}

type UpdateUserRequest struct {
	Name  string `json:"name"  validate:"omitempty,min=2,max=100"`
	Email string `json:"email" validate:"omitempty,email"`
}

type UserUseCase interface {
	GetUser(ctx context.Context, id string) (*entity.User, error)
	ListUsers(ctx context.Context) ([]*entity.User, error)
	CreateUser(ctx context.Context, req *CreateUserRequest) (*entity.User, error)
	UpdateUser(ctx context.Context, id string, req *UpdateUserRequest) (*entity.User, error)
	DeleteUser(ctx context.Context, id string) error
}

type userUseCase struct {
	repo domainrepo.UserRepository
}

func NewUserUseCase(repo domainrepo.UserRepository) UserUseCase {
	return &userUseCase{repo: repo}
}

func (u *userUseCase) GetUser(ctx context.Context, id string) (*entity.User, error) {
	ctx, span := userTracer.Start(ctx, "GetUser")
	defer span.End()
	span.SetAttributes(attribute.String("user.id", id))

	uid, err := parseUUID(id)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	user, err := u.repo.FindByID(ctx, uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("get user: %w", err)
	}
	return user, nil
}

func (u *userUseCase) ListUsers(ctx context.Context) ([]*entity.User, error) {
	ctx, span := userTracer.Start(ctx, "ListUsers")
	defer span.End()

	users, err := u.repo.FindAll(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("list users: %w", err)
	}
	return users, nil
}

func (u *userUseCase) CreateUser(ctx context.Context, req *CreateUserRequest) (*entity.User, error) {
	ctx, span := userTracer.Start(ctx, "CreateUser")
	defer span.End()

	exists, err := u.repo.ExistsByEmail(ctx, req.Email)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("check email: %w", err)
	}
	if exists {
		return nil, ErrEmailConflict
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("hashing password: %w", err)
	}

	user := &entity.User{
		Name:     req.Name,
		Email:    req.Email,
		Password: string(hash),
	}
	if err := u.repo.Create(ctx, user); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("create user: %w", err)
	}
	return user, nil
}

func (u *userUseCase) UpdateUser(ctx context.Context, id string, req *UpdateUserRequest) (*entity.User, error) {
	ctx, span := userTracer.Start(ctx, "UpdateUser")
	defer span.End()
	span.SetAttributes(attribute.String("user.id", id))

	uid, err := parseUUID(id)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	user, err := u.repo.FindByID(ctx, uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("find user: %w", err)
	}

	if req.Name != "" {
		user.Name = req.Name
	}
	if req.Email != "" && req.Email != user.Email {
		exists, err := u.repo.ExistsByEmail(ctx, req.Email)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil, fmt.Errorf("check email: %w", err)
		}
		if exists {
			return nil, ErrEmailConflict
		}
		user.Email = req.Email
	}

	if err := u.repo.Update(ctx, user); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("update user: %w", err)
	}
	return user, nil
}

func (u *userUseCase) DeleteUser(ctx context.Context, id string) error {
	ctx, span := userTracer.Start(ctx, "DeleteUser")
	defer span.End()
	span.SetAttributes(attribute.String("user.id", id))

	uid, err := parseUUID(id)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	if _, err := u.repo.FindByID(ctx, uid); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("find user: %w", err)
	}

	if err := u.repo.Delete(ctx, uid); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}

var (
	ErrNotFound      = errors.New("not found")
	ErrEmailConflict = errors.New("email already in use")
	ErrInvalidUUID   = errors.New("invalid id format")
)

func parseUUID(id string) (uuid.UUID, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return uuid.Nil, ErrInvalidUUID
	}
	return uid, nil
}
