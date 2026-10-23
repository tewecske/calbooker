package service

import (
	"context"
)

type oauthStateRepositoryStub struct {
	created       OAuthState
	consumed      OAuthState
	consumedState string
	consumedAt    int64
	consumeErr    error
}

func (r *oauthStateRepositoryStub) CreateOAuthState(_ context.Context, state OAuthState) error {
	r.created = state
	return nil
}

func (r *oauthStateRepositoryStub) ConsumeOAuthState(_ context.Context, state string, now int64) (OAuthState, error) {
	r.consumedState = state
	r.consumedAt = now
	if r.consumeErr != nil {
		return OAuthState{}, r.consumeErr
	}
	return r.consumed, nil
}

type oauthIdentityRepositoryStub struct {
	found       OAuthIdentity
	findErr     error
	created     OAuthIdentity
	createErr   error
	createCalls int
	listed      []OAuthIdentity
	listErr     error
	deletedUser int64
	deletedID   int64
	deleteErr   error
}

func (r *oauthIdentityRepositoryStub) FindOAuthIdentity(context.Context, string, string) (OAuthIdentity, error) {
	if r.findErr != nil {
		return OAuthIdentity{}, r.findErr
	}
	return r.found, nil
}

func (r *oauthIdentityRepositoryStub) CreateOAuthIdentity(_ context.Context, identity OAuthIdentity) error {
	r.createCalls++
	if r.createErr != nil {
		return r.createErr
	}
	r.created = identity
	return nil
}

func (r *oauthIdentityRepositoryStub) ListOAuthIdentities(context.Context, int64) ([]OAuthIdentity, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return append([]OAuthIdentity(nil), r.listed...), nil
}

type oauthUserRepositoryStub struct {
	byID         User
	byEmail      User
	created      User
	updated      User
	createErr    error
	findIDErr    error
	findEmailErr error
	updateErr    error
	createCalls  int
	updateCalls  int
}

func (r *oauthUserRepositoryStub) CreateUser(_ context.Context, user User) (User, error) {
	r.createCalls++
	if r.createErr != nil {
		return User{}, r.createErr
	}
	r.created = user
	r.created.ID = 99
	return r.created, nil
}

func (r *oauthUserRepositoryStub) FindUserByID(context.Context, int64) (User, error) {
	if r.findIDErr != nil {
		return User{}, r.findIDErr
	}
	if r.byID.ID == 0 {
		return User{}, ErrUserNotFound
	}
	return r.byID, nil
}

func (r *oauthUserRepositoryStub) FindUserByEmail(context.Context, string) (User, error) {
	if r.findEmailErr != nil {
		return User{}, r.findEmailErr
	}
	if r.byEmail.ID == 0 {
		return User{}, ErrUserNotFound
	}
	return r.byEmail, nil
}

func (r *oauthUserRepositoryStub) FindUserByUsername(context.Context, string) (User, error) {
	return User{}, ErrUserNotFound
}

func (r *oauthUserRepositoryStub) UpdateUser(_ context.Context, user User, _ int64) (User, error) {
	r.updateCalls++
	if r.updateErr != nil {
		return User{}, r.updateErr
	}
	r.updated = user
	r.updated.Version++
	return r.updated, nil
}

func (r *oauthUserRepositoryStub) DeleteUser(context.Context, int64, int64) error {
	return ErrUserNotFound
}
