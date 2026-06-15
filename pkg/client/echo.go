package client

import (
	"context"

	"github.com/labstack/echo/v4"
)

// Token returns the inbound request's Authorization header so it can be
// forwarded on an outbound call (context auth). Empty for public/USSD requests,
// in which case client.Do falls back to the service's system token.
//
// Usage in a client method:
//
//	client.Do[T](client.Context(ctx), c.client, client.Request{
//	    ..., Token: client.Token(ctx),
//	})
func Token(ctx echo.Context) string {
	if ctx == nil || ctx.Request() == nil {
		return ""
	}
	return ctx.Request().Header.Get("Authorization")
}

// Context returns the inbound request's context (for cancellation/deadline
// propagation), or context.Background() when there is no request.
func Context(ctx echo.Context) context.Context {
	if ctx == nil || ctx.Request() == nil {
		return context.Background()
	}
	return ctx.Request().Context()
}
