package services

import (
	"context"

	"interserverauth"
)

type quickSaveInterServerAuth struct {
	auth *interserverauth.Handler
}

func (a *quickSaveInterServerAuth) AuthorizationHeader(ctx context.Context) (string, error) {
	if !a.auth.IsValidAccessToken(ctx) {
		if err := a.auth.AcquireAccessToken(ctx); err != nil {
			return "", err
		}
	}

	return a.auth.GetAuthHeader(ctx), nil
}
