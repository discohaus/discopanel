package auth

import (
	"context"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

type contextKey string

const UserContextKey contextKey = "authenticated_user"

// Authenticated user from context, nil when absent
func GetUserFromContext(ctx context.Context) *v1.User {
	user, ok := ctx.Value(UserContextKey).(*v1.User)
	if !ok {
		return nil
	}
	return user
}

// Adds the authenticated user to context
func WithUser(ctx context.Context, user *v1.User) context.Context {
	return context.WithValue(ctx, UserContextKey, user)
}
