package ws

import (
	"context"
	"strings"
	"time"

	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// authMode records how a connection authenticated, so it can be rechecked.
type authMode int

const (
	authModeNone authMode = iota
	// Auth disabled entirely, the client acts as the system user
	authModeSystem
	// Session JWT or dp_ API token
	authModeToken
	// Anonymous access enabled by the admin
	authModeAnonymous
)

const revalidateTimeout = 5 * time.Second

func (c *Client) setIdentity(user *v1.User, mode authMode, token string) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	c.user = user
	c.authenticated = user != nil
	c.authMode = mode
	c.token = token
}

func (c *Client) identity() (*v1.User, bool) {
	c.authMu.RLock()
	defer c.authMu.RUnlock()
	return c.user, c.authenticated
}

// revalidate rechecks the connection's credentials and refreshes the user
// (so role changes apply to open sockets). Previously a socket stayed
// authorised forever after its first AUTH message, surviving logout, session
// expiry, user deletion and role removal. Returns false once the
// connection must be closed. Unauthenticated sockets are left alone.
func (c *Client) revalidate() bool {
	c.authMu.RLock()
	mode, token, authed := c.authMode, c.token, c.authenticated
	c.authMu.RUnlock()
	if !authed {
		return true
	}

	am := c.hub.authManager
	var user *v1.User
	switch mode {
	case authModeSystem:
		if am.IsAnyAuthEnabled() {
			return c.revoke()
		}
		user = am.SystemUser()
	case authModeAnonymous:
		if am.IsAnyAuthEnabled() && !am.IsAnonymousAccessEnabled() {
			return c.revoke()
		}
		user = am.AnonymousUser()
	case authModeToken:
		ctx, cancel := context.WithTimeout(context.Background(), revalidateTimeout)
		defer cancel()
		var err error
		if strings.HasPrefix(token, "dp_") {
			user, err = am.ValidateApiToken(ctx, token)
		} else {
			user, err = am.ValidateSession(ctx, token)
		}
		if err != nil || user == nil {
			return c.revoke()
		}
	default:
		return true
	}
	c.setIdentity(user, mode, token)
	c.dropForbiddenSubscriptions(user)
	return true
}

// dropForbiddenSubscriptions ends log and metrics streams the refreshed user
// may no longer read (role removed or narrowed), instead of letting them
// run until the client reconnects.
func (c *Client) dropForbiddenSubscriptions(user *v1.User) {
	allowed := func(serverID string) bool {
		ok, err := c.hub.enforcer.Enforce(user.Roles, optionsv1.ResourceType_RESOURCE_TYPE_SERVERS, optionsv1.ActionType_ACTION_TYPE_READ, serverID)
		return err == nil && ok
	}
	c.subscriptionsMu.Lock()
	defer c.subscriptionsMu.Unlock()
	for id, ch := range c.subscriptions {
		if !allowed(id) {
			delete(c.subscriptions, id)
			c.hub.logStreamer.Unsubscribe(id, ch)
		}
	}
	for id := range c.metricsSubs {
		if !allowed(id) {
			delete(c.metricsSubs, id)
		}
	}
}

func (c *Client) revoke() bool {
	c.setIdentity(nil, authModeNone, "")
	return false
}
